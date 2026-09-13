// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command homeconnect2mqtt is the daemon entry point: it bridges local
// Home Connect appliances to MQTT. This file owns flag parsing, logging
// setup, dependency wiring and process lifecycle.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/bridge"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/mapping"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/version"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/web"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("homeconnect2mqtt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath  = fs.String("config", "", "path to config.yaml (auto-located when empty)")
		devicesPath = fs.String("devices", "devices.yaml", "path to the device inventory file")
		mappingPath = fs.String("mapping", "mapping.yaml", "path to the optional enrichment catalogue")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stderr, version.String())
		return 0
	}

	if err := serve(*configPath, *devicesPath, *mappingPath, stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0 // graceful shutdown
		}
		_, _ = fmt.Fprintln(stderr, "fatal:", err)
		return 1
	}
	return 0
}

func serve(configPath, devicesPath, mappingPath string, stderr io.Writer) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	// RedactAttr enforces the redaction contract at the handler: attrs
	// keyed like secrets (psk/iv/serialNumber/mac/shipSki/deviceID,
	// docs/03-profile-format.md §6) are masked before they reach the log.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level, ReplaceAttr: profile.RedactAttr}))
	// Route slog.Default() through the same guard: library fallbacks (e.g.
	// a nil SessionConfig.Logger) must not bypass redaction.
	slog.SetDefault(logger)
	logger.Info("starting", slog.String("version", version.Version))

	specs, err := loadDeviceSpecs(devicesPath, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The daemon's own availability topic: the broker-side will covers
	// ungraceful death, the OnConnect hook below publishes the matching
	// "online" birth, and the shutdown path re-publishes "offline" because
	// a graceful DISCONNECT suppresses the will. Every discovery payload
	// declares this same string as its bridge-level availability source,
	// which is why both sides read it from one function (F1).
	//
	// It stays at <MQTT_TOPIC>/status, where it has always been. It is
	// already in the daemon's own publish root rather than in Home
	// Assistant's discovery tree, so there is nothing to move and no
	// retained copy to retract; an operator automation watching it keeps
	// working. It is bridge-level and not under a device because this
	// daemon mirrors several appliances over one broker connection.
	statusTopic := layout.Bridge(cfg.MQTTTopic)
	client := mqtt.NewTCPClient(mqttClientConfig(cfg, logger))
	lc := mqtt.NewLifecycle(mqtt.LifecycleConfig{
		InitialBackoff: cfg.ReconnectInitialDuration(),
		MaxBackoff:     cfg.ReconnectMaxDuration(),
		Jitter:         cfg.ReconnectJitterDuration(),
		Logger:         logger,
	}, client)
	lc.OnConnect(func(ctx context.Context) {
		_ = client.Publish(ctx, statusTopic, []byte(hass.PayloadAvailable), mqttQoS(cfg), true)
	})
	if err := lc.Start(ctx); err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}
	// Circuit breaker between the bridge and the broker: during a
	// degraded-broker phase (TCP link up, acks missing) publishes fail
	// fast with mqtt.ErrCircuitOpen instead of each stalling on the ack
	// timeout, and bounded half-open probes test recovery. Defaults: 5
	// consecutive broker-side failures open the circuit, recovery is
	// probed after 30s. The lifecycle's reconnect loop stays in charge
	// of the link itself; the status-topic publishes below intentionally
	// bypass the breaker for the same reason.
	breaker := mqtt.NewBreaker(client, mqtt.BreakerConfig{
		OnStateChange: func(from, to mqtt.BreakerState) {
			logger.Warn("homeconnect2mqtt.mqtt_breaker_state",
				slog.String("from", from.String()),
				slog.String("to", to.String()))
		},
	})
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Publish(stopCtx, statusTopic, []byte(hass.PayloadNotAvailable), mqttQoS(cfg), true)
		_ = lc.Stop(stopCtx)
	}()

	var disc *hass.Discovery
	if cfg.HASSEnable {
		disc = hass.New(breaker, cfg.HASSBaseTopic, cfg.MQTTTopic, mqttQoS(cfg), cfg.Language, cfg.HASSDiscovery == "curated", logger)
		if cat, err := mapping.Load(mappingPath); err != nil {
			logger.Warn("mapping.load", slog.String("err", err.Error()))
		} else {
			disc.SetEnricher(cat)
			logger.Info("mapping.loaded", slog.Int("features", cat.Len()))
		}
	}

	// The MQTT surface handed to the bridge: Publish is gated by the
	// circuit breaker, while Subscribe/Unsubscribe go straight to the
	// client — subscriptions are startup-path calls with their own
	// SUBACK-bounded wait and must not be rejected during a publish-side
	// broker brownout. mqtt.SplitClient (go-mqtt v1.4.0) is exactly this
	// shape; it replaces the struct this file used to hand-roll.
	session := mqtt.SplitClient(breaker, client)

	var store *state.Store
	if cfg.WebEnable {
		store = state.New(nil)
	}

	br, err := bridge.New(bridge.Deps{Config: cfg, MQTT: session, Logger: logger, Devices: specs, HASS: disc, State: store})
	if err != nil {
		return err
	}

	if !cfg.WebEnable {
		return br.Run(ctx)
	}

	webSrv := web.New(web.Config{Bind: cfg.WebBind, User: cfg.WebUser, Password: cfg.WebPassword},
		store, br,
		web.VersionInfo{Version: version.Version, Commit: version.Commit, BuildDate: version.BuildDate},
		client.IsConnected, logger)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return br.Run(gctx) })
	g.Go(func() error { return webSrv.Run(gctx) })
	return g.Wait()
}

// mqttClientConfig builds the broker client configuration, including the
// Last Will. It is a function rather than a literal inside run() so the
// will — the one publish this daemon never makes itself — can be asserted
// off the value the transport is handed, rather than off the constants
// that went into it.
func mqttClientConfig(cfg *config.Config, logger *slog.Logger) mqtt.TCPConfig {
	return mqtt.TCPConfig{
		BrokerURL:  cfg.MQTTServer,
		ClientID:   config.ClientID,
		Username:   cfg.MQTTLogin,
		Password:   cfg.MQTTPassword,
		CleanStart: true,
		Will: &mqtt.Will{
			// The same topic every discovery payload declares as its
			// bridge-level availability source (F1).
			Topic:   layout.Bridge(cfg.MQTTTopic),
			Payload: []byte(hass.PayloadNotAvailable),
			// Matched to the birth and shutdown publishes. It was left at
			// the zero value (QoS 0) while those went out at MQTT_QOS,
			// which was cosmetic only while nothing read the topic; every
			// entity now does.
			QoS:    mqttQoS(cfg),
			Retain: true,
		},
		Logger: logger,
	}
}

// mqttQoS translates the operator's MQTT_QOS (validated to 0..1) into the
// transport's QoS. It is written out rather than cast at each call site
// because the value 0 is load-bearing and its meaning is not portable: it
// is QoS 0 here, and in the go-hamqtt publisher vocabulary this migration
// moves onto, QoS(0) means *unset* and resolves to QoS 1. A deliberate
// QoS 0 has to survive that translation, so there is one place to change.
func mqttQoS(cfg *config.Config) mqtt.QoS {
	return mqtt.QoS(cfg.MQTTQoS) //nolint:gosec // MQTT_QOS is validated to 0..1
}

func loadConfig(configPath string) (*config.Config, error) {
	if configPath == "" {
		if located, ok := config.Locate(config.OSEnv{}); ok {
			configPath = located
		} else {
			return nil, errors.New("no config file found (pass --config)")
		}
	}
	cfg, err := config.LoadFile(configPath, config.OSEnv{})
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func loadDeviceSpecs(devicesPath string, logger *slog.Logger) ([]bridge.DeviceSpec, error) {
	devices, err := profile.LoadDevices(devicesPath)
	if err != nil {
		return nil, err
	}
	specs := make([]bridge.DeviceSpec, 0, len(devices))
	for _, dc := range devices {
		if dc.Description == "" {
			return nil, fmt.Errorf("device %q has no description path", dc.Name)
		}
		desc, err := profile.LoadDescriptionJSON(dc.Description, logger)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", dc.Name, err)
		}
		specs = append(specs, bridge.DeviceSpec{Config: dc, Description: desc})
	}
	return specs, nil
}
