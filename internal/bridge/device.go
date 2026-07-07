// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

// publishTimeout bounds a single MQTT publish, independent of the worker
// context so the final offline publish still goes out during shutdown.
const publishTimeout = 5 * time.Second

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
	b.publish(d.topics.connectionState(), []byte(s))
	avail := availOffline
	if s == homeconnect.StateConnected {
		avail = availOnline
		b.publishDiscovery(context.Background(), d)
	}
	b.publish(d.topics.availability(), []byte(avail))
	if b.state != nil {
		b.state.SetConnectionState(d.name, string(s), s == homeconnect.StateConnected)
	}
}

// publishDiscovery emits Home Assistant discovery configs for a device, if
// discovery is enabled.
func (b *Bridge) publishDiscovery(parent context.Context, d *Device) {
	if b.hass == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, publishTimeout)
	defer cancel()
	published := b.hass.PublishDevice(ctx, d.name, d.app.Info(), d.app.Entities())
	// Clear our own retained configs for this device that we no longer publish.
	b.reconcileOrphans(parent, d.name, published)
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

// publish performs a single retained publish, logging (never failing) on
// error so a transient MQTT issue can't crash a worker.
func (b *Bridge) publish(topic string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := b.mqtt.Publish(ctx, topic, payload, b.qos, b.retain); err != nil {
		b.logger.Warn("bridge.publish", slog.String("topic", topic), slog.String("err", err.Error()))
	}
}
