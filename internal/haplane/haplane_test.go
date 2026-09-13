// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package haplane

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"
	hatopic "github.com/SukramJ/go-hamqtt/topic"
)

// capture is a publisher.Transport that records what crossed it. Every
// assertion in this file reads the QoS BYTE and the retain FLAG off these
// records rather than off the constants that produced them: the whole
// point of this package is that the two vocabularies disagree about zero,
// and a test that compares one constant against another cannot see that.
type capture struct {
	mu   sync.Mutex
	pubs []record
	subs []record
}

type record struct {
	topic   string
	payload []byte
	qos     byte
	retain  bool
}

func (c *capture) Publish(_ context.Context, topic string, payload []byte, qos byte, retain bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pubs = append(c.pubs, record{topic, append([]byte(nil), payload...), qos, retain})
	return nil
}

func (c *capture) Subscribe(_ context.Context, filter string, qos byte, _ publisher.Handler) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs = append(c.subs, record{topic: filter, qos: qos})
	return nil
}

func (c *capture) Unsubscribe(context.Context, string) error { return nil }

func (c *capture) records() []record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]record(nil), c.pubs...)
}

// testLayout is a minimal topic.Layout: this package must not depend on
// internal/hass (which depends on nothing here), and the layout's own
// agreement with the daemon's builders is pinned in internal/hass.
type testLayout struct{}

func (testLayout) State(model.Slot) string        { return "homeconnect/dev/x/state" }
func (testLayout) Command(model.Slot) string      { return "homeconnect/dev/x/set" }
func (testLayout) Availability(model.Slot) string { return "homeconnect/dev/availability" }
func (testLayout) Bridge() string                 { return "homeconnect/status" }

var _ hatopic.Layout = testLayout{}

func newPlane(t *testing.T, mqttQoS int, retain bool) (*Plane, *capture) {
	t.Helper()
	c := &capture{}
	return New(c, Config{
		Prefix:      "homeassistant",
		StatusTopic: "homeconnect/status",
		Layout:      testLayout{},
		QoS:         QoS(mqttQoS),
		Retain:      retain,
		Logger:      slog.New(slog.DiscardHandler),
	}), c
}

// TestQoSTranslatesZeroToTheDeliberateSentinel is F9 at the one point the
// two vocabularies meet.
//
// publisher.QoS(0) is QoSUnset, which every runtime type in that package
// resolves to QoS 1. An operator who set MQTT_QOS: 0 asked for
// at-most-once, and the sentinel for saying so deliberately is
// QoSAtMostOnce — 0x80, outside the wire's 0-2 range precisely so the two
// cannot be written the same way.
func TestQoSTranslatesZeroToTheDeliberateSentinel(t *testing.T) {
	t.Parallel()
	if got := QoS(0); got != publisher.QoSAtMostOnce {
		t.Errorf("QoS(0) = %v, want QoSAtMostOnce", got)
	}
	if QoS(0) == publisher.QoSUnset {
		t.Error("QoS(0) is the zero value, which the library reads as *unset* and resolves to QoS 1")
	}
	for in, want := range map[int]byte{0: 0, 1: 1, 2: 2} {
		wire, ok := QoS(in).Wire()
		if !ok || wire != want {
			t.Errorf("QoS(%d).Wire() = (%d, %v), want (%d, true)", in, wire, ok, want)
		}
	}
}

// TestQoSRefusesAValueNobodyChose: a garbage level is a composition-root
// mistake, and it fails at wiring time rather than becoming a publish at a
// level the operator never asked for.
func TestQoSRefusesAValueNobodyChose(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("QoS(7) did not panic — a silent coercion is exactly what this package exists to remove")
		}
	}()
	QoS(7)
}

// TestEveryPublishCarriesTheStatedQoS reads the wire byte off the
// transport for each plane in turn — discovery, state, birth — which is
// the only place a silently-defaulted field becomes visible.
func TestEveryPublishCarriesTheStatedQoS(t *testing.T) {
	t.Parallel()
	for _, mqttQoS := range []int{0, 1} {
		p, c := newPlane(t, mqttQoS, true)
		ctx := t.Context()
		if _, err := p.Publish(ctx, "homeassistant/sensor/dev/x/config", []byte(`{"a":1}`)); err != nil {
			t.Fatalf("discovery publish: %v", err)
		}
		if err := p.PublishState(ctx, "homeconnect/dev/x/state", []byte("42")); err != nil {
			t.Fatalf("state publish: %v", err)
		}
		if err := p.AnnounceOnline(ctx); err != nil {
			t.Fatalf("announce: %v", err)
		}
		if err := p.Retract(ctx, "homeassistant/sensor/dev/gone/config"); err != nil {
			t.Fatalf("retract: %v", err)
		}
		recs := c.records()
		if len(recs) != 4 {
			t.Fatalf("MQTT_QOS %d: %d publishes, want 4", mqttQoS, len(recs))
		}
		for _, r := range recs {
			if int(r.qos) != mqttQoS {
				t.Errorf("MQTT_QOS %d: %s reached the transport at QoS %d — the operator's "+
					"delivery guarantee moved (F9)", mqttQoS, r.topic, r.qos)
			}
		}
		will, err := p.Will()
		if err != nil {
			t.Fatalf("will: %v", err)
		}
		if int(will.QoS) != mqttQoS {
			t.Errorf("MQTT_QOS %d: will qos = %d", mqttQoS, will.QoS)
		}
	}
}

// TestPublishStateHonoursMQTTRetain pins all four combinations of the
// operator's MQTT_RETAIN against an empty and a non-empty payload.
//
// Neither library call can express the flag on its own:
// publisher.StatePublisher.Publish is unconditionally retained and Pulse
// unconditionally not, so a plane that reached for the first would
// silently retain a fleet an operator deliberately runs non-retained, and
// one that reached for the second would leave every entity blank until
// its datapoint next changed.
func TestPublishStateHonoursMQTTRetain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		retain  bool
		payload []byte
		wantRet bool
	}{
		{"retained, a value", true, []byte("42"), true},
		{"retained, no value: the retraction the old code performed by accident", true, nil, true},
		{"not retained, a value", false, []byte("42"), false},
		{"not retained, no value", false, nil, false},
	}
	for _, tc := range cases {
		p, c := newPlane(t, 1, tc.retain)
		if err := p.PublishState(t.Context(), "homeconnect/dev/x/state", tc.payload); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		recs := c.records()
		if len(recs) != 1 {
			t.Fatalf("%s: %d publishes, want 1", tc.name, len(recs))
		}
		if recs[0].retain != tc.wantRet {
			t.Errorf("%s: retain = %v, want %v (MQTT_RETAIN is %v)",
				tc.name, recs[0].retain, tc.wantRet, tc.retain)
		}
		if len(recs[0].payload) != len(tc.payload) {
			t.Errorf("%s: payload %q, want %q", tc.name, recs[0].payload, tc.payload)
		}
	}
}

// TestPublishStateDeduplicatesARetainedRepeat is the one behaviour this
// step ADDS rather than preserves, so it is pinned in both directions.
//
// Every NOTIFY from an appliance reaches onUpdate whether or not anything
// moved, so an unchanged value used to cost one retained broker write and
// one Home Assistant state evaluation each time. A changed value must
// still go out, which is the half a too-eager gate would break.
func TestPublishStateDeduplicatesARetainedRepeat(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1, true)
	const topic = "homeconnect/dev/x/state"
	for _, payload := range []string{"Run", "Run", "Run", "Inactive", "Run"} {
		if err := p.PublishState(t.Context(), topic, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	recs := c.records()
	got := make([]string, 0, len(recs))
	for _, r := range recs {
		got = append(got, string(r.payload))
	}
	want := []string{"Run", "Inactive", "Run"}
	if len(got) != len(want) {
		t.Fatalf("wire carried %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wire carried %v, want %v", got, want)
		}
	}
}

// TestNonRetainedStateIsNeverDeduplicated: an operator running
// MQTT_RETAIN: false has edge-triggered consumers, and a gate that
// swallowed a repeat would swallow an event.
func TestNonRetainedStateIsNeverDeduplicated(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1, false)
	for range 3 {
		if err := p.PublishState(t.Context(), "homeconnect/dev/x/state", []byte("Present")); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(c.records()); n != 3 {
		t.Errorf("wire carried %d publishes, want 3 — a pulse is an event, not a state", n)
	}
}

// TestReconnectOpensTheDedupGateAndRebuildsTheDiscoveryRuntime is the
// lead finding of go-mtec2mqtt's post-mortem, pinned in both halves.
//
// Everything a publisher.Runtime remembers is a statement about a BROKER:
// which legacy topics it superseded, which configs it declared, which are
// in flight. A QoS 0 publish "succeeds" when the bytes reach a socket, so
// after a reconnect the broker may have applied none of it — and a
// process-lifetime runtime's in-process retry then skips retractions the
// broker never performed and publishes anyway. The state plane's dedup
// cache is the same statement: a broker that came back without a
// persistent retained store holds nothing while the cache still answers
// "already published".
func TestReconnectOpensTheDedupGateAndRebuildsTheDiscoveryRuntime(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1, true)
	ctx := t.Context()
	const (
		stateTopic  = "homeconnect/dev/x/state"
		configTopic = "homeassistant/sensor/dev/x/config"
	)
	if err := p.PublishState(ctx, stateTopic, []byte("Run")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Publish(ctx, configTopic, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	first := p.Runtime()
	if len(p.Declared()) != 1 {
		t.Fatalf("declared %v, want the one config", p.Declared())
	}

	p.Reconnect()

	if p.Runtime() == first {
		t.Error("the discovery runtime survived the reconnect — its superseded/declared/announced " +
			"maps describe a broker this process may no longer be talking to")
	}
	if got := p.Declared(); len(got) != 0 {
		t.Errorf("the new runtime already claims %v — it inherited the old connection's beliefs", got)
	}
	before := len(c.records())
	if err := p.PublishState(ctx, stateTopic, []byte("Run")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Publish(ctx, configTopic, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if n := len(c.records()) - before; n != 2 {
		t.Errorf("the reconnect re-sent %d of the 2 unchanged payloads — a broker that came back "+
			"without its retained store holds neither, and every entity stays blank", n)
	}
}

// TestTransportRefusesUseBeforeItIsWired closes the ordering knot's own
// failure mode. The Last Will is part of CONNECT, so the runtime is built
// before the client exists; a publish in that window is a programming
// error and must say so rather than reach a nil transport.
func TestTransportRefusesUseBeforeItIsWired(t *testing.T) {
	t.Parallel()
	var tr Transport
	if err := tr.Publish(t.Context(), "t", nil, 1, true); !errors.Is(err, ErrTransportNotWired) {
		t.Errorf("Publish before Wire = %v, want ErrTransportNotWired", err)
	}
	if err := tr.Subscribe(t.Context(), "t", 1, func(string, []byte, bool) {}); !errors.Is(err, ErrTransportNotWired) {
		t.Errorf("Subscribe before Wire = %v, want ErrTransportNotWired", err)
	}
	if err := tr.Unsubscribe(t.Context(), "t"); !errors.Is(err, ErrTransportNotWired) {
		t.Errorf("Unsubscribe before Wire = %v, want ErrTransportNotWired", err)
	}
	c := &capture{}
	tr.Wire(c)
	if err := tr.Publish(t.Context(), "t", []byte("x"), 1, true); err != nil {
		t.Errorf("Publish after Wire = %v", err)
	}
	if len(c.records()) != 1 {
		t.Error("the wired transport did not receive the publish")
	}
}

// TestWillAgreesWithTheAnnouncements: the broker writes the will when
// this daemon dies without a DISCONNECT and the daemon writes the
// announcements itself, so the two must name one topic, two payloads and
// one guarantee. A will nobody reads is indistinguishable from no will.
func TestWillAgreesWithTheAnnouncements(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1, true)
	will, err := p.Will()
	if err != nil {
		t.Fatalf("will: %v", err)
	}
	if err := p.AnnounceOnline(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.AnnounceOffline(t.Context()); err != nil {
		t.Fatal(err)
	}
	recs := c.records()
	if len(recs) != 2 {
		t.Fatalf("%d announcements, want 2", len(recs))
	}
	for _, r := range recs {
		if r.topic != will.Topic {
			t.Errorf("announced on %q, will writes %q", r.topic, will.Topic)
		}
		if r.qos != will.QoS || r.retain != will.Retain {
			t.Errorf("announcement qos=%d retain=%v, will qos=%d retain=%v",
				r.qos, r.retain, will.QoS, will.Retain)
		}
	}
	if string(recs[0].payload) != string(publisher.BirthPayload) {
		t.Errorf("birth payload = %q", recs[0].payload)
	}
	if !bytes.Equal(recs[1].payload, will.Payload) {
		t.Errorf("offline payload = %q, will payload = %q", recs[1].payload, will.Payload)
	}
}

// TestStatusTopicDisagreementIsRefused: publisher.New fills an empty
// StatusTopic from the layout and refuses one that disagrees with it, so
// stating both is the assertion that the daemon's Last Will and every
// entity's availability list name the same string. Under
// `availability_mode: all` a typo there greys out the whole fleet with
// nothing on the wire naming the cause.
func TestStatusTopicDisagreementIsRefused(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("a StatusTopic disagreeing with Layout.Bridge was accepted")
		}
	}()
	New(&capture{}, Config{
		Prefix:      "homeassistant",
		StatusTopic: "homeconnect/bridge/status", // the library default's shape, not this daemon's
		Layout:      testLayout{},
		QoS:         QoS(1),
		Logger:      slog.New(slog.DiscardHandler),
	})
}

// TestStateBytesReachTheWireUnwrapped is StateConfig.Encoding's only
// observable consequence here, and the test exists because the field is
// otherwise INERT — an inert field is a blind spot a pin can never see.
//
// This daemon renders its own state payload (bridge.payloadFor: a
// localized enum label, a bare number, a JSON object) and hands the bytes
// to Publish, so publisher's own renderer is never called and
// StateConfig.Encoding never selects anything. It is stated anyway because
// the zero value is EnvelopeEncoding and a later call to PublishValue —
// the obvious convenience — would then wrap every payload in JSON that
// the 667 configs already on the broker have no value_template to read.
// What IS observable is that nothing wraps the bytes today, and that is
// what this asserts.
func TestStateBytesReachTheWireUnwrapped(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1, true)
	for _, payload := range []string{"Run", "42", "3.5", `{"a":1}`, "Programm starten"} {
		if err := p.PublishState(t.Context(), "homeconnect/dev/"+payload+"/state", []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.records() {
		if want := r.topic[len("homeconnect/dev/") : len(r.topic)-len("/state")]; string(r.payload) != want {
			t.Errorf("wire carried %q, want %q — something rendered the payload", r.payload, want)
		}
	}
	if discovery.RawEncoding == discovery.EnvelopeEncoding {
		t.Fatal("the two encodings are the same value, so the Encoding field proves nothing")
	}
}
