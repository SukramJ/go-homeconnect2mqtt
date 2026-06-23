// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// publishTimeout bounds a single MQTT publish, independent of the worker
// context so the final offline publish still goes out during shutdown.
const publishTimeout = 5 * time.Second

// Device is one appliance worker: appliance + reconnect manager + topics.
type Device struct {
	name    string
	app     *homeconnect.Appliance
	manager *homeconnect.Manager
	topics  deviceTopics
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

	var socket homeconnect.Socket
	switch dc.ConnectionType {
	case profile.ConnectionAES:
		iv, err := homeconnect.DecodeKey(dc.IV64)
		if err != nil {
			return nil, fmt.Errorf("bridge: device %q iv64: %w", dc.Name, err)
		}
		aes, err := homeconnect.NewAESSocket(host, psk, iv)
		if err != nil {
			return nil, fmt.Errorf("bridge: device %q: %w", dc.Name, err)
		}
		socket = aes
	case profile.ConnectionTLS:
		return nil, fmt.Errorf("bridge: device %q: TLS-PSK transport is not yet supported", dc.Name)
	default:
		return nil, fmt.Errorf("bridge: device %q: unknown connection type %q", dc.Name, dc.ConnectionType)
	}

	session := homeconnect.NewSession(socket, homeconnect.SessionConfig{
		AppName:          b.cfg.AppName,
		AppID:            b.cfg.AppID,
		SendTimeout:      b.cfg.SendTimeoutDuration(),
		HandshakeTimeout: b.cfg.HandshakeTimeoutDuration(),
		Logger:           b.logger.With(slog.String("device", dc.Name)),
	})
	app := homeconnect.NewAppliance(session, spec.Description, b.logger.With(slog.String("device", dc.Name)))

	dev := &Device{
		name:   dc.Name,
		app:    app,
		topics: newDeviceTopics(b.cfg.MQTTTopic, dc.Name),
	}
	app.OnUpdate(func(e *homeconnect.Entity) { b.onUpdate(dev, e) })
	dev.manager = homeconnect.NewManager(app, homeconnect.ReconnectConfig{
		InitialBackoff: b.cfg.ReconnectInitialDuration(),
		MaxBackoff:     b.cfg.ReconnectMaxDuration(),
		Jitter:         b.cfg.ReconnectJitterDuration(),
		Logger:         b.logger.With(slog.String("device", dc.Name)),
		OnState:        func(s homeconnect.ConnectionState) { b.onState(dev, s) },
	})
	return dev, nil
}

// run drives the device's reconnect loop, isolating panics so one device
// can never take down the others (FK-1/FK-3).
func (d *Device) run(ctx context.Context, logger *slog.Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("bridge.device_panic", slog.String("device", d.name), slog.Any("panic", r))
			err = nil // isolate: do not propagate to siblings
		}
	}()
	return d.manager.Run(ctx)
}

// onUpdate publishes a changed entity's value to its state topic.
func (b *Bridge) onUpdate(d *Device, e *homeconnect.Entity) {
	if !e.HasValue() {
		return
	}
	b.publish(d.topics.state(e), []byte(payloadFor(e)))
}

// onState publishes the connection state and availability of a device and,
// on a fresh connection, (re)publishes Home Assistant discovery.
func (b *Bridge) onState(d *Device, s homeconnect.ConnectionState) {
	b.publish(d.topics.connectionState(), []byte(s))
	avail := availOffline
	if s == homeconnect.StateConnected {
		avail = availOnline
		b.publishDiscovery(d)
	}
	b.publish(d.topics.availability(), []byte(avail))
}

// publishDiscovery emits Home Assistant discovery configs for a device, if
// discovery is enabled.
func (b *Bridge) publishDiscovery(d *Device) {
	if b.hass == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	b.hass.PublishDevice(ctx, d.name, d.app.Info(), d.app.Entities())
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
