// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/mqtt"
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
}

// Bridge owns the per-device workers and the shared MQTT publish settings.
type Bridge struct {
	cfg     *config.Config
	mqtt    mqtt.Client
	logger  *slog.Logger
	qos     mqtt.QoS
	retain  bool
	devices []*Device
	hass    *hass.Discovery
	state   *state.Store

	// Command write-window retry budget (FK-5).
	cmdRetries    int
	cmdRetryDelay time.Duration
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
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	b := &Bridge{
		cfg:           deps.Config,
		mqtt:          deps.MQTT,
		logger:        logger,
		qos:           mqtt.QoS(deps.Config.MQTTQoS),
		retain:        deps.Config.RetainEnabled(),
		hass:          deps.HASS,
		state:         deps.State,
		cmdRetries:    3,
		cmdRetryDelay: time.Second,
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
	g, gctx := errgroup.WithContext(ctx)
	for _, d := range b.devices {
		g.Go(func() error {
			return d.run(gctx, b.logger)
		})
	}
	b.logger.Info("bridge.started", slog.Int("devices", len(b.devices)))
	err := g.Wait()
	b.logger.Info("bridge.stopped")
	return err
}
