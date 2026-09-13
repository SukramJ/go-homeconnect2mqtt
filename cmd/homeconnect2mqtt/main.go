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
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/bridge"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
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

	// The daemon's own availability topic and the whole Home Assistant
	// publish plane.
	//
	// The ordering here is a knot and the placeholder transport is what
	// unties it: the Last Will is part of CONNECT, so the MQTT client
	// needs publisher.Runtime.Will BEFORE it exists — while the runtime
	// needs a transport wrapping that very client. The plane is therefore
	// built over haplane.Transport and the real adapter wired into it
	// below, before the lifecycle starts.
	//
	// The status topic stays at <MQTT_TOPIC>/status, where it has always
	// been: already in the daemon's own publish root rather than in Home
	// Assistant's discovery tree, so there is nothing to move and no
	// retained copy to retract, and an operator automation watching it
	// keeps working. It is bridge-level and not under a device because
	// this daemon mirrors several appliances over one broker connection.
	//
	// Both StatusTopic and Layout are stated, and that IS the assertion:
	// publisher.New fills an empty StatusTopic from the layout and refuses
	// one that disagrees with it. Every discovery payload declares this
	// same string as its bridge-level availability source (F1), so the two
	// sides cannot drift — under `availability_mode: all` a typo would
	// grey out the whole fleet with nothing on the wire naming the cause.
	haLink := &haplane.Transport{}
	// The broker's Maximum Packet Size is read through a holder filled in
	// below, for the same ordering reason haLink exists: the plane must be
	// built before the client, because the client's Last Will comes off the
	// plane. An atomic rather than a plain variable because the hook is
	// called from every device worker's publish path while this goroutine
	// is still wiring; it answers "not known" until the client exists,
	// which is the same answer as a link that has not connected.
	var clientRef atomic.Pointer[mqtt.TCPClient]
	plane := haplane.New(haLink, haPlaneConfig(cfg, func() (uint32, bool) {
		return brokerMaxPacketSize(clientRef.Load())
	}, logger))
	will, err := plane.Will()
	if err != nil {
		return fmt.Errorf("mqtt: last will: %w", err)
	}

	client := mqtt.NewTCPClient(mqttClientConfig(cfg, will, logger))
	clientRef.Store(client)
	lc := mqtt.NewLifecycle(mqtt.LifecycleConfig{
		InitialBackoff: cfg.ReconnectInitialDuration(),
		MaxBackoff:     cfg.ReconnectMaxDuration(),
		Jitter:         cfg.ReconnectJitterDuration(),
		Logger:         logger,
	}, client)
	// Circuit breaker between the bridge and the broker: during a
	// degraded-broker phase (TCP link up, acks missing) publishes fail
	// fast with mqtt.ErrCircuitOpen instead of each stalling on the ack
	// timeout, and bounded half-open probes test recovery. Defaults: 5
	// consecutive broker-side failures open the circuit, recovery is
	// probed after 30s. The lifecycle's reconnect loop stays in charge
	// of the link itself.
	breaker := mqtt.NewBreaker(client, mqtt.BreakerConfig{
		OnStateChange: func(from, to mqtt.BreakerState) {
			logger.Warn("homeconnect2mqtt.mqtt_breaker_state",
				slog.String("from", from.String()),
				slog.String("to", to.String()))
		},
	})
	haLink.Wire(haTransport(layout.Bridge(cfg.MQTTTopic), breaker, client))

	// The MQTT surface handed to the bridge, same split.
	session := mqtt.SplitClient(breaker, client)

	var disc *hass.Discovery
	if cfg.HASSEnable {
		disc = hass.New(plane, cfg.HASSBaseTopic, cfg.MQTTTopic, cfg.Language, cfg.HASSDiscovery == "curated", logger)
		if cat, err := mapping.Load(mappingPath); err != nil {
			logger.Warn("mapping.load", slog.String("err", err.Error()))
		} else {
			disc.SetEnricher(cat)
			logger.Info("mapping.loaded", slog.Int("features", cat.Len()))
		}
	}

	var store *state.Store
	if cfg.WebEnable {
		store = state.New(nil)
	}

	br, err := bridge.New(bridge.Deps{
		Config: cfg, MQTT: session, Logger: logger,
		Devices: specs, HASS: disc, State: store, Plane: plane,
	})
	if err != nil {
		return err
	}

	// Registered before Start so the very first connect takes it too, and
	// on EVERY (re)connect rather than only at boot: the broker publishes
	// the will on the drop, so a reconnected daemon that does not
	// re-announce stays offline in Home Assistant while happily publishing
	// state nobody displays. PublishOnline also rebuilds the discovery
	// runtime and opens the state plane's dedup gate, both of which
	// describe a broker connection rather than this process.
	lc.OnConnect(br.PublishOnline)
	if err := lc.Start(ctx); err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		// Drain the discovery runtime's birth-replay worker BEFORE the
		// offline marker: a replay that landed after it would write
		// "online"-era configs to a broker this daemon has already told
		// Home Assistant it left.
		plane.Close()
		if err := plane.AnnounceOffline(stopCtx); err != nil {
			logger.Warn("homeconnect2mqtt.offline_failed", slog.String("err", err.Error()))
		}
		_ = lc.Stop(stopCtx)
	}()

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

// haTransport is the transport the Home Assistant plane publishes and
// subscribes through.
//
// A function rather than a literal inside serve() for the same reason
// mqttClientConfig is one: serve needs a broker and cannot be driven by a
// test, so a policy spelled inline there is a policy nothing checks. This
// one carries two, and both are deliberate:
//
//   - Publish through the breaker, subscribe around it. Subscriptions are
//     startup-path calls with their own SUBACK-bounded wait and must not
//     be rejected during a publish-side broker brownout.
//   - The two availability markers on statusTopic bypass the breaker, as
//     they always have. See haplane.BypassFor for why that asymmetry is
//     load-bearing rather than an oversight.
func haTransport(statusTopic string, breaker mqtt.Publisher, client mqtt.Client) publisher.Transport {
	return haplane.BypassFor(statusTopic,
		hagomqtt.Split(breaker, client),
		hagomqtt.Transport(client))
}

// shutdownTimeout bounds the final offline marker and the DISCONNECT.
const shutdownTimeout = 5 * time.Second

// haPlaneConfig builds the Home Assistant publish plane's configuration.
//
// A function rather than a literal inside serve() for the reason
// mqttClientConfig and haTransport are functions: serve needs a broker and
// an appliance and cannot be driven by a test, so a value spelled there is
// a value nothing checks. That is not a style preference — go-mtec2mqtt's
// equivalent step spelled publisher.Config.LegacyEntityTopics once in
// main.go and once in its test fixture, and dropping it from the
// composition root was caught by nothing at all, because the fixture went
// on stating the right thing while the daemon published into a tree it had
// retracted nothing from (its PR #53's worst first-pass blind spot).
//
// Every field this daemon depends on is therefore stated HERE, once, and
// asserted off this function. Note what is deliberately NOT stated:
// publisher.Config.LegacyEntityTopics stays nil, which means the
// five-segment publisher.LegacyTopicWithNodeID form alone — the form this
// bridge's whole installed fleet is on, measured 687 of 687 at step 4.
// Naming any form REPLACES that default rather than extending it, so the
// well-meant addition of a second shape would silently stop retracting the
// first. TestThePlaneStatesTheDefaultLegacyTopicForm reads that back off
// this value.
func haPlaneConfig(cfg *config.Config, brokerMax func() (uint32, bool), logger *slog.Logger) haplane.Config {
	return haplane.Config{
		Prefix:              cfg.HASSBaseTopic,
		StatusTopic:         layout.Bridge(cfg.MQTTTopic),
		Layout:              hass.NewLayout(cfg.MQTTTopic),
		QoS:                 haplane.QoS(cfg.MQTTQoS),
		Retain:              cfg.RetainEnabled(),
		BrokerMaxPacketSize: brokerMax,
		Logger:              logger,
	}
}

// brokerMaxPacketSize reports the largest packet the BROKER said it would
// accept, and whether that answer is known.
//
// It reads mqtt.ConnectResult.MaximumPacketSize, the MQTT 5.0 Maximum
// Packet Size property (0x27) off the CONNACK. It is emphatically not
// mqtt.TCPConfig.MaximumPacketSize, which is the largest packet this CLIENT
// accepts INBOUND and defaults to 1 MiB regardless of what the broker will
// take; confusing the two is how a device document gets published against a
// limit nobody measured.
//
// Unknown is reported as unknown. A nil client, a connection that has not
// completed, an MQTT 3.1.1 link with no property block to carry the value,
// and a broker that deliberately set no limit all come back as "not known"
// or zero — and haplane.Plane publishes in every one of those cases. An
// unknown limit read as a small one would refuse the migration outright on
// every v3.1.1 broker.
func brokerMaxPacketSize(client *mqtt.TCPClient) (uint32, bool) {
	if client == nil {
		return 0, false
	}
	res, ok := client.ConnectResult()
	if !ok {
		return 0, false
	}
	return res.MaximumPacketSize, true
}

// mqttClientConfig builds the broker client configuration, including the
// Last Will. It is a function rather than a literal inside run() so the
// will — the one publish this daemon never makes itself — can be asserted
// off the value the transport is handed, rather than off the constants
// that went into it.
//
// Every field of the will is COPIED from publisher.Will and none is
// spelled again here. That is what makes the two halves agree by
// construction: the broker writes the same topic, the same payload and
// the same guarantee the runtime's own AnnounceOnline/AnnounceOffline
// use, and the same topic every discovery payload declares as its
// bridge-level availability source (F1). A literal at this call site is
// exactly how a will nobody reads gets configured — which is the defect
// go-hamqtt's publisher package exists to stop reproducing, and which
// this daemon had until F1.
func mqttClientConfig(cfg *config.Config, will publisher.Will, logger *slog.Logger) mqtt.TCPConfig {
	return mqtt.TCPConfig{
		BrokerURL:  cfg.MQTTServer,
		ClientID:   config.ClientID,
		Username:   cfg.MQTTLogin,
		Password:   cfg.MQTTPassword,
		CleanStart: true,
		Will: &mqtt.Will{
			Topic:   will.Topic,
			Payload: will.Payload,
			QoS:     mqtt.QoS(will.QoS),
			Retain:  will.Retain,
		},
		Logger: logger,
	}
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
