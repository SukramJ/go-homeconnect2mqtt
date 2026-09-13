// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
)

func TestRunVersion(t *testing.T) {
	var errBuf bytes.Buffer
	if code := run([]string{"--version"}, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(errBuf.String(), "go-homeconnect2mqtt") {
		t.Errorf("version output = %q", errBuf.String())
	}
}

func TestRunBadFlag(t *testing.T) {
	var errBuf bytes.Buffer
	if code := run([]string{"--nope"}, &errBuf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunNoConfig(t *testing.T) {
	// With no config file found, the daemon fails fast with a non-zero code.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var errBuf bytes.Buffer
	if code := run([]string{"--config", "/nonexistent/config.yaml"}, &errBuf); code == 0 {
		t.Fatalf("expected non-zero exit for missing config")
	}
}

// failingPublisher always reports a broker-side failure so the breaker
// counts every publish against its threshold.
type failingPublisher struct{ calls int }

func (p *failingPublisher) Publish(context.Context, string, []byte, mqtt.QoS, bool, ...mqtt.PublishOption) error {
	p.calls++
	return mqtt.ErrNotConnected
}

// recordingSubscriber captures Subscribe/Unsubscribe filters so the
// test can prove the session delegates them to the raw client.
type recordingSubscriber struct {
	subscribed   []string
	unsubscribed []string
}

func (s *recordingSubscriber) Subscribe(_ context.Context, filter string, _ mqtt.QoS, _ mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.subscribed = append(s.subscribed, filter)
	return mqtt.SubscribeResult{}, nil
}

func (s *recordingSubscriber) Unsubscribe(_ context.Context, filter string) error {
	s.unsubscribed = append(s.unsubscribed, filter)
	return nil
}

// TestMQTTSessionPublishIsCircuitGated proves the bridge-facing session
// (mqtt.SplitClient over the breaker and the raw client)
// routes Publish through the breaker: once the failure threshold is
// reached, publishes fail fast with ErrCircuitOpen and no longer hit
// the underlying client.
func TestMQTTSessionPublishIsCircuitGated(t *testing.T) {
	t.Parallel()

	pub := &failingPublisher{}
	session := mqtt.SplitClient(
		mqtt.NewBreaker(pub, mqtt.BreakerConfig{FailureThreshold: 1}),
		&recordingSubscriber{},
	)

	err := session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	if !errors.Is(err, mqtt.ErrNotConnected) {
		t.Fatalf("first publish: got %v, want ErrNotConnected", err)
	}
	err = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	if !errors.Is(err, mqtt.ErrCircuitOpen) {
		t.Fatalf("second publish: got %v, want ErrCircuitOpen", err)
	}
	if pub.calls != 1 {
		t.Fatalf("underlying publisher saw %d calls, want 1 (open circuit must fail fast)", pub.calls)
	}
}

// TestMQTTSessionSubscribeBypassesBreaker proves subscriptions are not
// affected by the publish-side circuit state.
func TestMQTTSessionSubscribeBypassesBreaker(t *testing.T) {
	t.Parallel()

	sub := &recordingSubscriber{}
	session := mqtt.SplitClient(
		mqtt.NewBreaker(&failingPublisher{}, mqtt.BreakerConfig{FailureThreshold: 1}),
		sub,
	)

	// Trip the circuit open on the publish side.
	_ = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	_ = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)

	if _, err := session.Subscribe(t.Context(), "cmd/#", mqtt.QoS1, func(*mqtt.Message) {}); err != nil {
		t.Fatalf("subscribe with open circuit: %v", err)
	}
	if err := session.Unsubscribe(t.Context(), "cmd/#"); err != nil {
		t.Fatalf("unsubscribe with open circuit: %v", err)
	}
	if len(sub.subscribed) != 1 || sub.subscribed[0] != "cmd/#" {
		t.Fatalf("subscriber saw %v, want [cmd/#]", sub.subscribed)
	}
	if len(sub.unsubscribed) != 1 || sub.unsubscribed[0] != "cmd/#" {
		t.Fatalf("unsubscriber saw %v, want [cmd/#]", sub.unsubscribed)
	}
}

// testPlane is the composition root's own plane, built exactly as serve
// builds it, so a will asserted here is the will the daemon ships.
func testPlane(cfg *config.Config) *haplane.Plane {
	return haplane.New(&haplane.Transport{}, haplane.Config{
		Prefix:      cfg.HASSBaseTopic,
		StatusTopic: layout.Bridge(cfg.MQTTTopic),
		Layout:      hass.NewLayout(cfg.MQTTTopic),
		QoS:         haplane.QoS(cfg.MQTTQoS),
		Retain:      cfg.RetainEnabled(),
		Logger:      slog.New(slog.DiscardHandler),
	})
}

func testWill(t *testing.T, cfg *config.Config) publisher.Will {
	t.Helper()
	will, err := testPlane(cfg).Will()
	if err != nil {
		t.Fatalf("Will: %v", err)
	}
	return will
}

// TestWillIsTheAvailabilitySourceEveryEntityReads is F1 on the publisher
// side. The Last Will is the one publish this daemon never makes itself,
// so it is the one the tests could not see: it is asserted here off the
// mqtt.TCPConfig the transport is handed, not off the constants that went
// into it.
//
// Until F1 the will wrote <MQTT_TOPIC>/status, which no discovery payload
// referenced — so a killed daemon left every entity showing its last
// retained value forever. The topic did not move (it was already in the
// daemon's own publish root, not in Home Assistant's discovery tree,
// which is why no retained copy needed retracting); what changed is that
// every payload now declares it.
func TestWillIsTheAvailabilitySourceEveryEntityReads(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: 1}
	will := mqttClientConfig(cfg, testWill(t, cfg), slog.New(slog.DiscardHandler)).Will
	if will == nil {
		t.Fatal("no Last Will configured: a killed daemon would leave every entity available forever")
	}
	if want := layout.Bridge(cfg.MQTTTopic); will.Topic != want {
		t.Errorf("will topic = %q, want %q", will.Topic, want)
	}
	if strings.HasPrefix(will.Topic, cfg.HASSBaseTopic+"/") {
		t.Errorf("will topic %q is inside Home Assistant's discovery tree", will.Topic)
	}
	if string(will.Payload) != hass.PayloadNotAvailable {
		t.Errorf("will payload = %q, want %q", will.Payload, hass.PayloadNotAvailable)
	}
	if !will.Retain {
		t.Error("will is not retained: a subscriber connecting after the death sees nothing")
	}
	if will.QoS != mqtt.QoS1 {
		t.Errorf("will qos = %v, want %v (MQTT_QOS, the birth publish's guarantee)", will.QoS, mqtt.QoS1)
	}
}

// TestWillIsCopiedFromTheRuntimeNotSpelledAgain is the seam a literal at
// the call site would hide. publisher.Runtime.AnnounceOnline and
// AnnounceOffline write Config.StatusTopic at Config.QoS retained, and
// the will has to be the same three; a will nobody reads is
// indistinguishable from no will at all, which is the defect this daemon
// shipped until F1 and which two of its sibling bridges still had when
// go-hamqtt's publisher package was written.
//
// It is asserted by PERTURBING the runtime's answer rather than by
// comparing two constants: a mqttClientConfig that ignored its argument
// and rebuilt the will from cfg would pass any comparison against cfg.
func TestWillIsCopiedFromTheRuntimeNotSpelledAgain(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: 1}
	perturbed := publisher.Will{
		Topic:   "somewhere/else/entirely",
		Payload: []byte("not-a-marker"),
		QoS:     2,
		Retain:  false,
	}
	got := mqttClientConfig(cfg, perturbed, slog.New(slog.DiscardHandler)).Will
	if got.Topic != perturbed.Topic || !bytes.Equal(got.Payload, perturbed.Payload) ||
		byte(got.QoS) != perturbed.QoS || got.Retain != perturbed.Retain {
		t.Errorf("will = %+v, want every field copied from the runtime's %+v — "+
			"a field spelled again here is a field that can drift from the "+
			"announcements and from every entity's availability list", got, perturbed)
	}
}

// TestMQTTQoSZeroStaysQoSZero is F9's pin at the translation point.
// MQTT_QOS: 0 is an operator-facing promise of at-most-once, and the
// go-hamqtt publisher vocabulary this migration moved onto reads QoS(0)
// as *unset*, resolving it to QoS 1. The whole chain is pinned here — the
// mapping, the runtime the plane builds out of it, and the will that
// runtime states — so the upgrade cannot happen silently at any of the
// three.
func TestMQTTQoSZeroStaysQoSZero(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]mqtt.QoS{0: mqtt.QoS0, 1: mqtt.QoS1} {
		cfg := &config.Config{MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: in}
		will := testWill(t, cfg)
		if got := mqtt.QoS(will.QoS); got != want {
			t.Errorf("MQTT_QOS: %d -> runtime will qos %v, want %v", in, got, want)
		}
		if got := mqttClientConfig(cfg, will, slog.New(slog.DiscardHandler)).Will.QoS; got != want {
			t.Errorf("MQTT_QOS: %d -> client will qos %v, want %v", in, got, want)
		}
	}
}

// recordingClient is an mqtt.Client that records what reached it.
type recordingClient struct {
	recordingSubscriber
	topics []string
}

func (c *recordingClient) Publish(_ context.Context, topic string, _ []byte, _ mqtt.QoS, _ bool, _ ...mqtt.PublishOption) error {
	c.topics = append(c.topics, topic)
	return nil
}

// TestHATransportKeepsTheAvailabilityMarkersOffTheBreaker pins the two
// policies of the transport the Home Assistant plane runs on, which serve()
// cannot be driven to demonstrate.
//
// The asymmetry is the point and it is easy to lose: publisher.Runtime has
// ONE transport, so wiring it straight through the breaker would put the
// birth and death markers behind the same circuit as the 687 discovery
// configs. mqtt.Breaker counts ErrNotConnected and ErrConnectionLost as
// failures, so a connection drop is exactly what opens it — and the first
// thing a reconnected daemon does is announce itself online.
func TestHATransportKeepsTheAvailabilityMarkersOffTheBreaker(t *testing.T) {
	t.Parallel()
	// The status topic is taken from the SAME expression serve() takes it
	// from — the plane's own answer — rather than from a literal here. A
	// test that supplies its own constant cannot see the two halves of the
	// composition root drift apart, and they did: this file's own const
	// meant appending "/x" to serve()'s second spelling of the topic
	// survived the whole suite. #44 caught that once as M41 and it came
	// back one line away.
	cfg := &config.Config{MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: 1}
	plane := haplane.New(&haplane.Transport{}, haPlaneConfig(cfg, nil, slog.New(slog.DiscardHandler)))
	status := plane.StatusTopic()
	if status != layout.Bridge(cfg.MQTTTopic) {
		t.Fatalf("the plane's status topic is %q, the layout renders %q", status, layout.Bridge(cfg.MQTTTopic))
	}
	client := &recordingClient{}
	failing := &failingPublisher{}
	// A breaker whose underlying publisher always fails, tripped open, so
	// a publish that goes THROUGH it is refused and one that bypasses it
	// reaches the client. That is the state a reconnect actually finds.
	breaker := mqtt.NewBreaker(failing, mqtt.BreakerConfig{FailureThreshold: 1})
	// haPlaneTransport, not haTransport: the derivation of the bypass topic
	// is the part that can be wrong, so it has to be inside what is driven.
	tr := haPlaneTransport(plane, breaker, client)
	_ = tr.Publish(t.Context(), "homeassistant/sensor/x/y/config", []byte("{}"), 1, true)

	if err := tr.Publish(t.Context(), "homeassistant/sensor/x/y/config", []byte("{}"), 1, true); !errors.Is(err, mqtt.ErrCircuitOpen) {
		t.Fatalf("a discovery config did not go through the breaker: %v", err)
	}
	if err := tr.Publish(t.Context(), status, []byte("online"), 1, true); err != nil {
		t.Errorf("the birth marker was refused by the open breaker (%v) — every entity would "+
			"sit unavailable until a recovery probe happened to succeed", err)
	}
	if len(client.topics) != 1 || client.topics[0] != status {
		t.Errorf("the client saw %v, want only %q", client.topics, status)
	}
	if err := tr.Subscribe(t.Context(), "homeassistant/#", 1, nil); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if len(client.subscribed) != 1 {
		t.Errorf("subscriptions did not reach the client: %v — they must bypass the publish-side "+
			"breaker, or a brownout would stop the daemon resubscribing", client.subscribed)
	}
}

// TestThePlaneConfigStatesEveryFieldTheMigrationDependsOn drives the
// composition root's own value.
//
// This is the blind-spot shape the sibling project's migration shipped with:
// a value spelled once in main.go and once in a test fixture, with nothing
// comparing them. Dropping publisher.Config.LegacyEntityTopics from
// go-mtec2mqtt's composition root was caught by NOTHING, because its
// fixture went on stating the right thing while the daemon published a
// device document into a tree it had retracted none of.
//
// So every field the device-document migration depends on is read back off
// haPlaneConfig here, and the two that decide whether the migration works
// at all — the retraction form and the packet-size hook — are read off a
// real haplane.Plane built from it.
func TestThePlaneConfigStatesEveryFieldTheMigrationDependsOn(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect",
		HASSBaseTopic: "homeassistant", MQTTQoS: 0, MQTTRetain: boolPtr(false),
	}
	var asked bool
	got := haPlaneConfig(cfg, func() (uint32, bool) { asked = true; return 1024, true }, slog.New(slog.DiscardHandler))

	if got.Prefix != cfg.HASSBaseTopic {
		t.Errorf("Prefix = %q, want %q", got.Prefix, cfg.HASSBaseTopic)
	}
	if want := layout.Bridge(cfg.MQTTTopic); got.StatusTopic != want {
		t.Errorf("StatusTopic = %q, want %q", got.StatusTopic, want)
	}
	if got.Layout == nil {
		t.Error("no Layout stated: publisher.New could not then check StatusTopic against anything")
	}
	// MQTT_QOS 0 must reach the plane as the deliberate at-most-once
	// sentinel, not as the zero value the library reads as "unset" (F9).
	if got.QoS != haplane.QoS(0) || got.QoS == 0 {
		t.Errorf("QoS = %v, want the QoS(0) sentinel", got.QoS)
	}
	if got.Retain != cfg.RetainEnabled() {
		t.Errorf("Retain = %v, want %v", got.Retain, cfg.RetainEnabled())
	}
	if got.BrokerMaxPacketSize == nil {
		t.Fatal("no BrokerMaxPacketSize hook: a device document would be published against a " +
			"limit nobody measured, and the failure lands AFTER the retraction")
	}
	if size, known := got.BrokerMaxPacketSize(); !asked || !known || size != 1024 {
		t.Errorf("the hook was not the one handed in: (%d, %v)", size, known)
	}

	// The retraction form, read off a real plane rather than off the
	// absent field. Nil means publisher.LegacyTopicWithNodeID alone, which
	// is the five-segment form this bridge's fleet is on (687 of 687,
	// step 4). Naming any form REPLACES the default rather than extending
	// it, so an addition made in good faith stops retracting the shape
	// that exists — and the failure looks clean.
	plane := haplane.New(&haplane.Transport{}, got)
	if forms := plane.LegacyForms(); len(forms) != 1 ||
		!strings.Contains(forms[0], "LegacyTopicWithNodeID") {
		t.Errorf("the plane retracts under %v, want the five-segment node-id form alone", forms)
	}
}

// TestBrokerMaximumIsUnknownBeforeTheClientExists. The hook is built before
// the MQTT client is, because the client's Last Will comes off the plane.
// Until then — and on an MQTT 3.1.1 link, which carries no property block —
// the answer is "not known", and haplane publishes on "not known". Reading
// it as a small limit would refuse the migration outright.
func TestBrokerMaximumIsUnknownBeforeTheClientExists(t *testing.T) {
	t.Parallel()
	if size, known := brokerMaxPacketSize(nil); known || size != 0 {
		t.Errorf("brokerMaxPacketSize(nil) = (%d, %v), want (0, false)", size, known)
	}
	client := mqtt.NewTCPClient(mqtt.TCPConfig{BrokerURL: "tcp://127.0.0.1:1", ClientID: "x"})
	if size, known := brokerMaxPacketSize(client); known || size != 0 {
		t.Errorf("an unconnected client answered (%d, %v), want (0, false)", size, known)
	}
}

func boolPtr(b bool) *bool { return &b }

// recordingTransport is a publisher.Transport that records the topics
// crossing it, so the shutdown ordering can be read rather than assumed.
type recordingTransport struct {
	mu     sync.Mutex
	topics []string
}

func (r *recordingTransport) Publish(_ context.Context, topic string, _ []byte, _ byte, _ bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topics = append(r.topics, topic)
	return nil
}

func (r *recordingTransport) Subscribe(context.Context, string, byte, publisher.Handler) error {
	return nil
}
func (r *recordingTransport) Unsubscribe(context.Context, string) error { return nil }

func (r *recordingTransport) seen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.topics)
}

// stopperSpy records how much had been published at the moment discovery
// was stopped. The COUNT is the assertion: "before" is an ordering, and an
// ordering asserted by a boolean is an ordering asserted by nothing.
type stopperSpy struct {
	tr         *recordingTransport
	calls      int
	seenAtStop int
}

func (s *stopperSpy) StopDiscovery() {
	s.calls++
	s.seenAtStop = s.tr.seen()
}

// TestShutdownStopsDiscoveryBeforeTheOfflineMarker drives the shutdown
// ordering serve() cannot be driven to demonstrate.
//
// The offline marker is the only availability signal a graceful stop
// produces — a clean DISCONNECT suppresses the Last Will — so a discovery
// publish that lands after it leaves a retained "online"-era config behind
// on a broker this daemon has told Home Assistant it left. Two things
// publish discovery asynchronously and outlive the call that started them,
// and neither can be stopped from outside except by this call.
func TestShutdownStopsDiscoveryBeforeTheOfflineMarker(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: 1}
	tr := &recordingTransport{}
	plane := haplane.New(tr, haPlaneConfig(cfg, nil, slog.New(slog.DiscardHandler)))
	spy := &stopperSpy{tr: tr}

	shutdownHAPlane(t.Context(), spy, plane, slog.New(slog.DiscardHandler))

	if spy.calls != 1 {
		t.Fatalf("StopDiscovery called %d times, want 1 — nothing else closes the window "+
			"between the last discovery publish and the offline marker", spy.calls)
	}
	if spy.seenAtStop != 0 {
		t.Errorf("%d messages had already gone out when discovery was stopped, want 0", spy.seenAtStop)
	}
	want := layout.Bridge(cfg.MQTTTopic)
	if len(tr.topics) != 1 || tr.topics[0] != want {
		t.Fatalf("the shutdown wrote %v, want exactly [%s]", tr.topics, want)
	}
}
