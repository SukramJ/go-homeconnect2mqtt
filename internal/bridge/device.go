// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

// publishTimeout bounds a single MQTT publish, independent of the worker
// context so the final offline publish still goes out during shutdown.
const publishTimeout = 5 * time.Second

// bundlePublishTimeout bounds one whole device-document migration: the
// retraction of every per-entity config the document supersedes, and then
// the document itself.
//
// It is an order of magnitude above publishTimeout because it bounds an
// order of magnitude more round trips. On the migrating connection the
// retraction is 687 retained publishes for this fixture's appliance, each
// waiting on its own acknowledgement at MQTT_QOS 1, and only then does the
// ~460 KB document go out. Cutting that off halfway is the one outcome the
// ordering exists to avoid — the per-entity configs gone, the document not
// written, and the appliance with no discovery config at all — so the
// budget is set to tolerate a slow broker rather than to keep a worker
// responsive. It costs nothing on every later connection, where the
// retractions are a no-op the library skips and the document is
// deduplicated against what it already published.
const bundlePublishTimeout = 60 * time.Second

// Panic-restart backoff for a device worker (docs/05-resilience.md: only
// ctx cancel stops a worker).
const (
	restartInitialBackoff = time.Second
	restartMaxBackoff     = 30 * time.Second
	// restartStableRun is how long a run must survive before the restart
	// backoff resets to its initial value.
	restartStableRun = time.Minute
)

// Device is one appliance worker: appliance + reconnect manager + topics.
type Device struct {
	name    string
	app     *homeconnect.Appliance
	manager *homeconnect.Manager
	topics  deviceTopics
	pub     *devicePublisher

	// Injectable for deterministic tests; default to manager.Run and the
	// real clock (mirrors homeconnect.ReconnectConfig's injection style).
	runFn func(ctx context.Context) error
	sleep func(time.Duration) <-chan time.Time
	now   func() time.Time
}

// Name returns the logical device name.
func (d *Device) Name() string { return d.name }

// buildDevice constructs the appliance, session and reconnect manager for a
// device spec and wires the publish callbacks into b.
func buildDevice(b *Bridge, spec DeviceSpec) (*Device, error) {
	dc := spec.Config
	host := dc.Host
	if host == "" {
		return nil, fmt.Errorf("bridge: device %q has no host", dc.Name)
	}
	psk, err := homeconnect.DecodeKey(dc.PSK64)
	if err != nil {
		return nil, fmt.Errorf("bridge: device %q psk64: %w", dc.Name, err)
	}
	var iv []byte
	if dc.IV64 != "" {
		if iv, err = homeconnect.DecodeKey(dc.IV64); err != nil {
			return nil, fmt.Errorf("bridge: device %q iv64: %w", dc.Name, err)
		}
	}
	socket, err := homeconnect.NewSocket(homeconnect.ConnectionType(dc.ConnectionType), host, psk, iv)
	if err != nil {
		return nil, fmt.Errorf("bridge: device %q: %w", dc.Name, err)
	}
	if dc.ConnectionType == profile.ConnectionTLS && !homeconnect.TLSPSKSupported {
		b.logger.Warn("bridge.tls_device", slog.String("device", dc.Name),
			slog.String("note", "TLS-PSK needs the 'tlspsk' (cgo) build; this device will report offline in the CGo-free build"))
	}

	session := homeconnect.NewSession(socket, homeconnect.SessionConfig{
		AppName:          b.cfg.AppName,
		AppID:            b.cfg.AppID,
		SendTimeout:      b.cfg.SendTimeoutDuration(),
		HandshakeTimeout: b.cfg.HandshakeTimeoutDuration(),
		Heartbeat:        b.cfg.HeartbeatDuration(),
		Logger:           b.logger.With(slog.String("device", dc.Name)),
	})
	app := homeconnect.NewAppliance(session, spec.Description, b.logger.With(slog.String("device", dc.Name)))

	dev := &Device{
		name:   dc.Name,
		app:    app,
		topics: newDeviceTopics(b.cfg.MQTTTopic, dc.Name),
		pub:    newDevicePublisher(b.logger.With(slog.String("device", dc.Name))),
		sleep:  time.After,
		now:    time.Now,
	}
	app.OnUpdate(func(e *homeconnect.Entity) { b.onUpdate(dev, e) })
	dev.manager = homeconnect.NewManager(app, homeconnect.ReconnectConfig{
		InitialBackoff: b.cfg.ReconnectInitialDuration(),
		MaxBackoff:     b.cfg.ReconnectMaxDuration(),
		Jitter:         b.cfg.ReconnectJitterDuration(),
		Logger:         b.logger.With(slog.String("device", dc.Name)),
		OnState:        func(s homeconnect.ConnectionState) { b.onState(dev, s) },
	})
	dev.runFn = dev.manager.Run
	return dev, nil
}

// run drives the device's reconnect loop, isolating panics so one device
// can never take down the others (FK-1/FK-3). A panicked run is restarted
// with exponential backoff — per docs/05-resilience.md only ctx cancel may
// stop a worker; a normal manager return still ends the worker. Between
// attempts the device is marked offline so retained availability never
// advertises stale values.
func (d *Device) run(ctx context.Context, b *Bridge) error {
	backoff := restartInitialBackoff
	for {
		started := d.now()
		panicked, err := d.runOnce(ctx, b.logger)
		if !panicked {
			return err
		}
		b.onState(d, homeconnect.StateOffline) //nolint:contextcheck // publish is bounded by publishTimeout on purpose, independent of the worker ctx
		if d.now().Sub(started) >= restartStableRun {
			backoff = restartInitialBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.sleep(backoff):
		}
		backoff *= 2
		if backoff > restartMaxBackoff {
			backoff = restartMaxBackoff
		}
	}
}

// runOnce executes one manager run, converting a panic into a flag so the
// restart loop in run can recover it without propagating to siblings.
func (d *Device) runOnce(ctx context.Context, logger *slog.Logger) (panicked bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("bridge.device_panic", slog.String("device", d.name), slog.Any("panic", r))
			panicked = true
			err = nil // isolate: do not propagate to siblings
		}
	}()
	return false, d.runFn(ctx)
}

// maxQueuedPublishes bounds a device's publish backlog while the broker is
// unreachable; entity updates are low-rate, so the cap is only hit during a
// long brownout.
const maxQueuedPublishes = 1024

type queuedPublish struct {
	topic   string
	payload []byte
}

// devicePublisher decouples entity-state publishes from the appliance
// receive goroutine: onUpdate enqueues and one goroutine per device drains
// in order, so a broker brownout never stalls frame processing and one
// device's stuck publish never blocks another (docs/05-resilience.md).
// Every update is preserved in order — event entities pulse (Present →
// Off) and edge-triggered consumers need both transitions — so the queue
// is FIFO, bounded, and overflow drops the oldest entry with a warning,
// never silently.
type devicePublisher struct {
	logger  *slog.Logger
	mu      sync.Mutex
	queue   []queuedPublish
	dropped int
	wake    chan struct{}
}

func newDevicePublisher(logger *slog.Logger) *devicePublisher {
	return &devicePublisher{logger: logger, wake: make(chan struct{}, 1)}
}

// enqueue appends an update and wakes the drain goroutine. It never
// blocks; when the backlog cap is reached the oldest update is dropped
// and logged.
func (p *devicePublisher) enqueue(topic string, payload []byte) {
	p.mu.Lock()
	if len(p.queue) >= maxQueuedPublishes {
		dropped := p.queue[0]
		p.queue = p.queue[1:]
		p.dropped++
		if p.dropped == 1 || p.dropped%100 == 0 {
			p.logger.Warn("bridge.publish_backlog_overflow",
				slog.String("topic", dropped.topic), slog.Int("dropped", p.dropped))
		}
	}
	p.queue = append(p.queue, queuedPublish{topic: topic, payload: payload})
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// next pops the oldest queued update.
func (p *devicePublisher) next() (q queuedPublish, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		// Between bursts the queue is empty: drop the backing array so a
		// long brownout's backlog does not stay pinned, and reset the
		// overflow episode counter.
		p.queue = nil
		p.dropped = 0
		return queuedPublish{}, false
	}
	q = p.queue[0]
	p.queue[0] = queuedPublish{}
	p.queue = p.queue[1:]
	return q, true
}

// run drains the queue until ctx is cancelled. Nothing is flushed after
// cancel so shutdown never hangs on a wedged broker.
func (p *devicePublisher) run(ctx context.Context, publish func(topic string, payload []byte)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			q, ok := p.next()
			if !ok {
				break
			}
			publish(q.topic, q.payload)
		}
	}
}

// onUpdate enqueues a changed entity's value for the device's async
// publisher (so a slow broker never blocks the appliance receive
// goroutine) and feeds the optional state store, which is in-memory and
// stays synchronous.
func (b *Bridge) onUpdate(d *Device, e *homeconnect.Entity) {
	if !e.HasValue() {
		return
	}
	d.pub.enqueue(d.topics.state(e), []byte(payloadFor(e, b.cfg.Language)))
	if b.state != nil {
		b.state.UpdateFeature(d.name, b.featureView(d, e))
	}
}

// featureView builds the web/state representation of an entity.
func (b *Bridge) featureView(d *Device, e *homeconnect.Entity) state.Feature {
	f := state.Feature{
		Feature:      e.Name(),
		Topic:        d.topics.state(e),
		UID:          e.UID(),
		Value:        e.Value(),
		ValueRaw:     e.ValueRaw(),
		ProtocolType: string(e.Desc.ProtocolType),
		ContentType:  e.Desc.ContentType,
		Access:       e.Access(),
		Available:    e.Available(),
		Writable:     e.Writable(),
	}
	if e.Desc.IsEnum() {
		for _, name := range e.Desc.Enumeration {
			f.Options = append(f.Options, name)
		}
	}
	bd := e.Bounds()
	if bd.HasMin {
		f.Min = &bd.Min
	}
	if bd.HasMax {
		f.Max = &bd.Max
	}
	if bd.HasStep {
		f.Step = &bd.Step
	}
	return f
}

// onState publishes the connection state and availability of a device and,
// on a fresh connection, (re)publishes Home Assistant discovery.
func (b *Bridge) onState(d *Device, s homeconnect.ConnectionState) {
	b.publish(d.topics.ConnectionState(), []byte(s))
	avail := availOffline
	if s == homeconnect.StateConnected {
		avail = availOnline
		b.publishDiscovery(context.Background(), d)
	}
	b.publish(d.topics.Availability(), []byte(avail))
	if b.state != nil {
		b.state.SetConnectionState(d.name, string(s), s == homeconnect.StateConnected)
	}
}

// publishDiscovery emits one retained Home Assistant device document for a
// device, if discovery is enabled, and then clears this daemon's own
// retained per-entity configs that nothing claims any more.
//
// The sweep is gated on the document having been PUBLISHED, not on its
// having been BUILT, and the difference is the whole reason the two steps
// are written apart. A build that succeeds and a publish that fails is a
// completely ordinary outcome — an open circuit breaker, a broker that
// refuses the packet size, a context that expired mid-retraction — and a
// sweep that ran there would clear every per-entity config the previous
// release published and put nothing in their place. go-mtec2mqtt shipped
// exactly that guard testing the wrong value, logging "no device document
// was published" while asking whether one had been built (its PR #54,
// finding F3).
//
// The test is publisher.Runtime's own claim set rather than a bool this
// package keeps, because that is the record the retraction pass already
// subtracts against: if the runtime does not name the document among its
// declared topics, the document is not on the broker as far as anything
// else in this daemon is concerned either.
func (b *Bridge) publishDiscovery(parent context.Context, d *Device) {
	if b.hass == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, bundlePublishTimeout)
	topic, err := b.hass.PublishDeviceBundle(ctx, d.name, d.app.Info(), d.app.Entities())
	cancel()
	if err != nil {
		// Every failure path in PublishDeviceBundle has already logged what
		// it refused and why. What matters here is only that the sweep must
		// not run: see the doc comment.
		b.logger.Warn("bridge.discovery_sweep_skipped",
			slog.String("device", d.name),
			slog.String("reason", "the device document was not published"))
		return
	}
	if !b.documentIsDeclared(topic) {
		b.logger.Warn("bridge.discovery_sweep_skipped",
			slog.String("device", d.name), slog.String("topic", topic),
			slog.String("reason", "the device document was built but the runtime does not claim it"))
		return
	}
	b.reconcileOrphans(parent, d.name, map[string]bool{topic: true})
}

// documentIsDeclared reports whether the publish plane claims topic — i.e.
// whether the document reached the broker on THIS connection. The plane's
// runtime is rebuilt on every (re)connect, so a claim is a statement about
// the live connection rather than about the life of the process.
func (b *Bridge) documentIsDeclared(topic string) bool {
	return slices.Contains(b.plane.Declared(), topic)
}

// safePublish is publish with panic isolation: a panic in the MQTT client
// drops that one publish instead of the whole process, mirroring the device
// worker's recover (docs/05-resilience.md).
func (b *Bridge) safePublish(topic string, payload []byte) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("bridge.publish_panic", slog.String("topic", topic), slog.Any("panic", r))
		}
	}()
	b.publish(topic, payload)
}

// publish writes one state-plane payload through publisher.StatePublisher,
// logging (never failing) on error so a transient MQTT issue can't crash a
// worker.
//
// The QoS and the retain flag are no longer arguments: they are the
// plane's stated policy, MQTT_QOS and MQTT_RETAIN, resolved once in
// internal/haplane. What the plane adds over the bare client call it
// replaces is the dedup gate — an appliance re-reporting an unchanged
// value costs one byte comparison instead of one retained broker write
// and one Home Assistant state evaluation, and this daemon's appliances
// re-report on every NOTIFY whether or not anything moved.
func (b *Bridge) publish(topic string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := b.plane.PublishState(ctx, topic, payload); err != nil {
		b.logger.Warn("bridge.publish", slog.String("topic", topic), slog.String("err", err.Error()))
	}
}
