// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

// DeviceSpec pairs a device's runtime config with its parsed description.
type DeviceSpec struct {
	Config      profile.DeviceConfig
	Description *profile.Description
}

// Deps are the bridge's collaborators.
type Deps struct {
	Config  *config.Config
	MQTT    mqtt.Client
	Logger  *slog.Logger
	Devices []DeviceSpec
	// HASS is the optional Home Assistant discovery publisher (nil disables).
	HASS *hass.Discovery
	// State is the optional in-memory cache feeding the web UI (nil disables).
	State *state.Store
	// Plane is the go-hamqtt publish runtime: the state plane every
	// worker publishes through, and the discovery plane the orphan sweep
	// reads. Required.
	Plane *haplane.Plane
}

// Bridge owns the per-device workers and the shared MQTT publish settings.
type Bridge struct {
	cfg     *config.Config
	mqtt    mqtt.Client
	logger  *slog.Logger
	qos     mqtt.QoS
	devices []*Device
	hass    *hass.Discovery
	state   *state.Store
	plane   *haplane.Plane

	// commands is the inbound half: one route per device, handlers run on
	// router workers rather than on the transport's read loop.
	commands *publisher.CommandRouter

	// Command write-window retry budget (FK-5).
	cmdRetries    int
	cmdRetryDelay time.Duration

	// Per-device discovery orphan-cleanup gate (skips a re-entrant reconcile).
	reconcileMu sync.Mutex
	reconciling map[string]bool

	// started is closed by Run once the one-shot HASS_DISCOVERY_REFRESH
	// migration has finished. The (re)connect republish waits on it, so the
	// first connect's republish cannot race the flag that exists to clear
	// what it is about to write.
	started  chan struct{}
	startOne sync.Once

	// discoveryStopped closes the shutdown window. See [Bridge.StopDiscovery].
	discoveryStopped atomic.Bool

	// republishMu serialises the (re)connect republish passes and
	// republishPending coalesces them. A flapping link fires OnConnect
	// repeatedly, and two CONCURRENT passes over the same fleet buy
	// nothing but a second snapshot window per appliance.
	//
	// Coalescing rather than skipping, and the difference is load-bearing:
	// a pass running when the link drops has already published some
	// appliances' documents against the runtime of the connection that
	// died, and a later pass that simply gave up because one was in flight
	// would leave exactly those appliances unpublished on the new
	// connection. The flag is set BEFORE the lock is taken, so whoever
	// holds the lock takes the flag and a request made while a pass is
	// running is always honoured by another pass.
	republishMu      sync.Mutex
	republishPending atomic.Bool

	// prior is what the retained device documents declared before this
	// connection published anything — the memory a removal needs, and the
	// only thing this daemon has that can tell a component it REMOVED from
	// one it never had. See [priorDocuments] here and
	// internal/hass/tombstone.go for what is done with it.
	prior priorDocuments
}

// New builds the bridge and all device workers. It fails fast on a
// misconfigured device so startup errors surface immediately.
func New(deps Deps) (*Bridge, error) {
	if deps.Config == nil {
		return nil, fmt.Errorf("bridge: nil config")
	}
	if deps.MQTT == nil {
		return nil, fmt.Errorf("bridge: nil mqtt client")
	}
	if deps.Plane == nil {
		return nil, fmt.Errorf("bridge: nil publish plane")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	b := &Bridge{
		cfg:           deps.Config,
		mqtt:          deps.MQTT,
		logger:        logger,
		qos:           mqtt.QoS(deps.Config.QoSLevel()), //nolint:gosec // MQTT_QOS is validated to 0..1
		hass:          deps.HASS,
		state:         deps.State,
		plane:         deps.Plane,
		cmdRetries:    3,
		cmdRetryDelay: time.Second,
		reconciling:   map[string]bool{},
		started:       make(chan struct{}),
	}
	for _, spec := range deps.Devices {
		dev, err := buildDevice(b, spec)
		if err != nil {
			return nil, err
		}
		b.devices = append(b.devices, dev)
		if b.state != nil {
			info := dev.app.Info()
			b.state.RegisterDevice(dev.name, "", info.Brand, info.Type, "", map[string]any{
				"brand": info.Brand, "type": info.Type, "model": info.Model, "version": info.Version,
			})
		}
	}
	if len(b.devices) == 0 {
		return nil, fmt.Errorf("bridge: no devices configured")
	}
	return b, nil
}

// Devices returns the configured device workers.
func (b *Bridge) Devices() []*Device { return b.devices }

// Run starts one isolated worker per device and blocks until the context
// is cancelled. Each worker reconnects independently; a single device
// failure never stops the others (FK-1).
func (b *Bridge) Run(ctx context.Context) error {
	if err := b.subscribeCommands(ctx); err != nil {
		return fmt.Errorf("bridge: subscribe commands: %w", err)
	}
	// Stopped before the daemon's "offline" marker goes out: a command
	// accepted after this daemon has announced itself gone would be
	// executed by nobody and acknowledged by nothing. Stop drains, so it
	// blocks on whatever a handler is doing.
	defer b.stopCommands()      //nolint:contextcheck // the drain is deliberately bounded independently of ctx: Run's ctx is cancelled by the time this fires, and a handler mid-write still has to finish
	b.refreshDiscoveryOnce(ctx) // one-shot HASS_DISCOVERY_REFRESH migration
	// Released only now: PublishOnline's republish waits here, so the very
	// first connect's republish cannot publish a device document into the
	// window HASS_DISCOVERY_REFRESH is about to clear.
	b.startOne.Do(func() { close(b.started) })
	g, gctx := errgroup.WithContext(ctx)
	for _, d := range b.devices {
		// One drain goroutine per device: entity-state publishes are
		// decoupled from the appliance receive loop and one device's
		// stuck publish never blocks another (docs/05-resilience.md).
		// safePublish gives it the same panic blast-radius guarantee as
		// the device worker.
		g.Go(func() error {
			d.pub.run(gctx, b.safePublish)
			return nil
		})
		g.Go(func() error {
			return d.run(gctx, b)
		})
	}
	b.logger.Info("bridge.started", slog.Int("devices", len(b.devices)))
	err := g.Wait()
	b.logger.Info("bridge.stopped")
	return err
}

// PublishOnline is the (re)connect hook: it rebuilds the Home Assistant
// plane for the new broker connection and announces this daemon online.
//
// Both halves belong to the connection rather than to the process. The
// discovery runtime's superseded/declared/announced maps and the state
// plane's dedup cache are statements about a BROKER, and a reconnect may
// be to one that applied none of them — see haplane.Plane.Reconnect.
func (b *Bridge) PublishOnline(ctx context.Context) {
	b.plane.Reconnect()
	if err := b.plane.AnnounceOnline(ctx); err != nil {
		b.logger.Warn("bridge.online_failed", slog.String("err", err.Error()))
	}
	// Off the hook's goroutine: mqtt.Lifecycle runs OnConnect callbacks
	// inline on the reconnect loop, and a callback that blocks stalls every
	// later reconnect attempt. The republish is a snapshot window and a
	// ~460 KB document per appliance.
	go b.republishDiscovery(ctx)
}

// republishDiscovery re-publishes every appliance's device document.
//
// It is the ONE asynchronous fleet-wide discovery pass this daemon has:
// the (re)connect hook above and the Home Assistant birth handler (see
// [Bridge.subscribeBirth]) both call it, rather than each looping over the
// devices itself. That is not tidiness. The gate below — waiting for
// `started`, so the one-shot HASS_DISCOVERY_REFRESH migration cannot be
// raced — is an invariant of the DAEMON, not of one caller, and the birth
// handler used to walk past it: Home Assistant's status topic is retained,
// so the broker replays `online` inline on the SUBSCRIBE that Run performs
// before the refresh runs. The mutex additionally serialises the two
// callers against each other, so one pass's document write can no longer
// land between the other's retraction and the runtime bookkeeping behind
// it.
//
// This is also the other half of the dedup-gate question, and it is the half
// that is easy to answer wrongly by doing nothing. Plane.Reconnect throws
// the discovery runtime away, so the gate that would suppress a repeat
// publish is OPEN on the new connection — but an open gate that nothing
// walks through publishes exactly as little as a closed one. Nothing else
// re-drives discovery on a reconnect: onState publishes when an APPLIANCE
// connects, which a broker reconnect does not disturb, and the Home
// Assistant birth handler fires when HOME ASSISTANT restarts, which it also
// does not. So a broker that came back without its retained store — a
// restart without persistence, a failover to a fresh node — kept every
// appliance's entities missing until the daemon itself was restarted. That
// is go-mtec2mqtt's finding F2, and with one document per appliance instead
// of 687 topics it costs the whole fleet at once rather than piecemeal.
//
// publisher.Runtime.Republish is deliberately NOT the mechanism. It re-sends
// the bytes the runtime cached, which is the wrong half: the runtime here is
// brand new and has cached nothing, and more importantly a cached replay
// would skip the supersede step that retracts the per-entity configs — the
// step whose completeness on THIS connection is the entire point of
// rebuilding the runtime. Re-running the publish re-runs both.
func (b *Bridge) republishDiscovery(ctx context.Context) {
	if b.hass == nil || b.discoveryStopped.Load() {
		return
	}
	select {
	case <-b.started:
	case <-ctx.Done():
		return
	}
	b.republishPending.Store(true)
	b.republishMu.Lock()
	defer b.republishMu.Unlock()
	if !b.republishPending.CompareAndSwap(true, false) {
		return // a pass that started after this request was made has done the work
	}
	for _, d := range b.devices {
		if ctx.Err() != nil {
			return
		}
		b.publishDiscovery(ctx, d)
	}
}

// StopDiscovery stops this daemon writing discovery, permanently.
//
// Call it before the final retained "offline" marker. That marker is the
// only availability signal a graceful shutdown produces at all — a clean
// DISCONNECT suppresses the Last Will — and two things here START a
// discovery pass that outlives the call that started it: the Home
// Assistant birth handler, which reacts to a message that can arrive at any
// moment, and the (re)connect hook. Both run [Bridge.republishDiscovery],
// which reads the flag this sets before it waits on anything, so a pass
// requested during shutdown returns instead of parking on a gate that may
// never open. Either landing after the offline
// marker writes "online"-era configs to a broker this daemon has already
// told Home Assistant it left, and a retained config is not a transient
// mistake.
//
// publisher.Runtime.Close does NOT cover this, and the comment that said it
// did was describing a drain this daemon does not have: the runtime's
// birth-replay worker exists only once publisher.Runtime.WatchBirth has been
// called, and this daemon watches Home Assistant's birth topic itself (see
// subscribeBirth). Close is kept because the runtime is the library's to
// finish with, not because it closes this window.
func (b *Bridge) StopDiscovery() { b.discoveryStopped.Store(true) }

// stopCommands drains the command router.
//
// The timeout is its own, deliberately not derived from Run's context:
// that context is already cancelled when this runs, and a Stop on a
// cancelled context would abandon a handler mid-write rather than let it
// finish. Stop blocks on whatever a handler is currently doing, which is
// the point — a command accepted and then dropped is a button press that
// did nothing, with no error anywhere to explain it.
func (b *Bridge) stopCommands() {
	if b.commands == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandDrainTimeout)
	defer cancel()
	if err := b.commands.Stop(ctx); err != nil {
		b.logger.Warn("bridge.command_stop", slog.String("err", err.Error()))
	}
}
