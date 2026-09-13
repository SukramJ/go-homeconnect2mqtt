// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
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
		qos:           mqtt.QoS(deps.Config.MQTTQoS), //nolint:gosec // MQTT_QOS is validated to 0..1
		hass:          deps.HASS,
		state:         deps.State,
		plane:         deps.Plane,
		cmdRetries:    3,
		cmdRetryDelay: time.Second,
		reconciling:   map[string]bool{},
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
}

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
