// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package haplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"
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

// testLayout is the convention's own layout: this package must not depend
// on internal/hass (which depends on nothing here), and the daemon layout's
// agreement with its builders is pinned in internal/hass. What this package
// needs from a layout is the topic.SmartHomeLayout capability, which is
// what switches the runtime's `connected` vocabulary.
func testLayout() hatopic.SmartHome {
	l, err := hatopic.NewSmartHome("homeconnect")
	if err != nil {
		panic(err)
	}
	return l
}

const (
	testConnected = "homeconnect/connected"
	testState     = "homeconnect/status/HAID/BSH/x"
)

func newPlane(t *testing.T, mqttQoS int) (*Plane, *capture) {
	t.Helper()
	c := &capture{}
	return New(c, Config{
		Prefix:      "homeassistant",
		StatusTopic: testConnected,
		Layout:      testLayout(),
		QoS:         QoS(mqttQoS),
		SetFilter:   "homeconnect/set/#",
		Logger:      slog.New(slog.DiscardHandler),
	}), c
}

// statusVal decodes the `val` of a status object off the wire.
func statusVal(t *testing.T, payload []byte) any {
	t.Helper()
	var obj struct {
		Val any   `json:"val"`
		TS  int64 `json:"ts"`
		LC  int64 `json:"lc"`
	}
	if err := json.Unmarshal(payload, &obj); err != nil {
		t.Fatalf("%q is not a status object: %v", payload, err)
	}
	if obj.TS == 0 || obj.LC == 0 || obj.LC > obj.TS {
		t.Errorf("%q: ts/lc missing or lc > ts", payload)
	}
	return obj.Val
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
// transport for each plane in turn — discovery, birth, retraction, will —
// which is the only place a silently-defaulted field becomes visible. The
// state plane is the exception, and it is asserted as one: status items are
// QoS 0 by the convention whatever MQTT_QOS says (openccu-loom ADR 0083).
func TestEveryPublishCarriesTheStatedQoS(t *testing.T) {
	t.Parallel()
	for _, mqttQoS := range []int{0, 1} {
		p, c := newPlane(t, mqttQoS)
		ctx := t.Context()
		if _, err := p.Publish(ctx, "homeassistant/sensor/dev/x/config", []byte(`{"a":1}`)); err != nil {
			t.Fatalf("discovery publish: %v", err)
		}
		if err := p.AnnounceOnline(ctx); err != nil {
			t.Fatalf("announce: %v", err)
		}
		if err := p.Retract(ctx, "homeassistant/sensor/dev/gone/config"); err != nil {
			t.Fatalf("retract: %v", err)
		}
		if err := p.PublishStatus(ctx, testState, 42); err != nil {
			t.Fatalf("state publish: %v", err)
		}
		recs := c.records()
		if len(recs) != 4 {
			t.Fatalf("MQTT_QOS %d: %d publishes, want 4", mqttQoS, len(recs))
		}
		for _, r := range recs {
			want := mqttQoS
			if r.topic == testState {
				want = 0
			}
			if int(r.qos) != want {
				t.Errorf("MQTT_QOS %d: %s reached the transport at QoS %d, want %d", mqttQoS, r.topic, r.qos, want)
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

// TestPublishStatusIsARetainedStatusObject pins the payload shape every
// discovery payload reads through `value_json.val`, and the one way to say
// "no value": an empty retained payload, never `{"val":null}`.
func TestPublishStatusIsARetainedStatusObject(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value any
		want  any // decoded val; nil means the empty retraction
	}{
		{"a number", 42, float64(42)},
		{"a float", 3.5, 3.5},
		{"a boolean", true, true},
		{"a token", "BSH.Common.EnumType.PowerState.On", "BSH.Common.EnumType.PowerState.On"},
		{"an object", map[string]any{"a": float64(1)}, map[string]any{"a": float64(1)}},
		{"no value", nil, nil},
	}
	for _, tc := range cases {
		p, c := newPlane(t, 1)
		if err := p.PublishStatus(t.Context(), testState, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		recs := c.records()
		if len(recs) != 1 || !recs[0].retain || recs[0].qos != 0 {
			t.Fatalf("%s: wire carried %+v, want one retained QoS 0 publish", tc.name, recs)
		}
		if tc.want == nil {
			if len(recs[0].payload) != 0 {
				t.Errorf("%s: payload %q, want the empty retraction", tc.name, recs[0].payload)
			}
			continue
		}
		got, _ := json.Marshal(statusVal(t, recs[0].payload))
		want, _ := json.Marshal(tc.want)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: val = %s, want %s", tc.name, got, want)
		}
	}
}

// TestPublishStatusDeduplicatesAnUnchangedVal is pinned in both directions.
//
// Every NOTIFY from an appliance reaches onUpdate whether or not anything
// moved, so an unchanged value would cost one retained broker write and
// one Home Assistant state evaluation each time — and with a status object
// whose `ts` moves on every observation, a byte comparison would never see
// a repeat at all. The gate compares `val`. A changed value must still go
// out, which is the half a too-eager gate would break.
func TestPublishStatusDeduplicatesAnUnchangedVal(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1)
	for _, v := range []string{"Run", "Run", "Run", "Inactive", "Run"} {
		if err := p.PublishStatus(t.Context(), testState, v); err != nil {
			t.Fatal(err)
		}
	}
	recs := c.records()
	got := make([]any, 0, len(recs))
	for _, r := range recs {
		got = append(got, statusVal(t, r.payload))
	}
	want := []any{"Run", "Inactive", "Run"}
	if len(got) != len(want) {
		t.Fatalf("wire carried %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wire carried %v, want %v", got, want)
		}
	}
}

// TestAStatusPublishIntoTheCommandTreeIsRefused: the command tree is
// `<name>/set/#`, stated to the state plane as its collision guard — which
// before 0.15.0 could not be stated at all, because the old command filter
// matched every state topic (F4).
func TestAStatusPublishIntoTheCommandTreeIsRefused(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1)
	if err := p.PublishStatus(t.Context(), "homeconnect/set/HAID/BSH/x", 1); err == nil {
		t.Error("a status publish into the command tree was accepted")
	}
	if n := len(c.records()); n != 0 {
		t.Errorf("%d publishes reached the wire", n)
	}
}

// TestRepublishStatusResendsTheCachedObjects is the "and on every
// reconnect" half of the publish rule: the replay sends the bytes the
// broker accepted, original `ts` included, without consulting the gate.
func TestRepublishStatusResendsTheCachedObjects(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1)
	ctx := t.Context()
	for _, topic := range []string{testState, testState + "2"} {
		if err := p.PublishStatus(ctx, topic, "Run"); err != nil {
			t.Fatal(err)
		}
	}
	first := c.records()
	n, err := p.RepublishStatus(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RepublishStatus = (%d, %v), want (2, nil)", n, err)
	}
	replay := c.records()[len(first):]
	for i, r := range replay {
		if !bytes.Equal(r.payload, first[i].payload) || !r.retain {
			t.Errorf("replay of %s = %q retained=%v, want the cached %q retained",
				r.topic, r.payload, r.retain, first[i].payload)
		}
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
	p, c := newPlane(t, 1)
	ctx := t.Context()
	const configTopic = "homeassistant/sensor/dev/x/config"
	if err := p.PublishStatus(ctx, testState, "Run"); err != nil {
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
	if err := p.PublishStatus(ctx, testState, "Run"); err != nil {
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

// TestConnectedLevelSurvivesAReconnect pins `<name>/connected` across the
// runtime swap. publisher.Runtime remembers the level, and Plane.Reconnect
// throws the runtime away — so without the plane's own memory a broker
// reconnect while every appliance is up would announce 1, and every entity
// would sit unavailable until an appliance happened to reconnect too.
func TestConnectedLevelSurvivesAReconnect(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1)
	ctx := t.Context()
	payloads := func() []string {
		var out []string
		for _, r := range c.records() {
			if r.topic == testConnected {
				out = append(out, string(r.payload))
			}
		}
		return out
	}
	steps := []struct {
		name string
		do   func() error
		want []string
	}{
		{"the first announce: no appliance yet", func() error { return p.AnnounceOnline(ctx) }, []string{"1"}},
		{"an appliance connects", func() error { return p.SetConnected(ctx, discovery.ConnectedOperational) }, []string{"1", "2"}},
		{"a repeat publishes nothing", func() error { return p.SetConnected(ctx, discovery.ConnectedOperational) }, []string{"1", "2"}},
		{"a broker reconnect", func() error { p.Reconnect(); return p.AnnounceOnline(ctx) }, []string{"1", "2", "2"}},
		{"the last appliance drops", func() error { return p.SetConnected(ctx, discovery.ConnectedBroker) }, []string{"1", "2", "2", "1"}},
		{"another reconnect", func() error { p.Reconnect(); return p.AnnounceOnline(ctx) }, []string{"1", "2", "2", "1", "1"}},
		{"a graceful stop", func() error { return p.AnnounceOffline(ctx) }, []string{"1", "2", "2", "1", "1", "0"}},
	}
	for _, st := range steps {
		if err := st.do(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if got := payloads(); strings.Join(got, ",") != strings.Join(st.want, ",") {
			t.Fatalf("%s: connected carried %v, want %v", st.name, got, st.want)
		}
	}
	will, err := p.Will()
	if err != nil || will.Topic != testConnected || string(will.Payload) != "0" {
		t.Errorf("will = %+v (%v), want 0 on %s", will, err, testConnected)
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
// announcements itself, so the two must name one topic and one guarantee,
// and the graceful stop must write what the will writes. A will nobody
// reads is indistinguishable from no will.
func TestWillAgreesWithTheAnnouncements(t *testing.T) {
	t.Parallel()
	p, c := newPlane(t, 1)
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
	if string(recs[0].payload) != "1" {
		t.Errorf("birth payload = %q, want 1 (the broker is up, no appliance yet)", recs[0].payload)
	}
	if !bytes.Equal(recs[1].payload, will.Payload) || string(will.Payload) != "0" {
		t.Errorf("offline payload = %q, will payload = %q, want both 0", recs[1].payload, will.Payload)
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
		StatusTopic: "homeconnect/status", // the pre-0.15.0 topic, not the layout's
		Layout:      testLayout(),
		QoS:         QoS(1),
		Logger:      slog.New(slog.DiscardHandler),
	})
}

// TestBypassForKeepsTheAvailabilityMarkersOffTheBreaker pins the
// asymmetry that is invisible in the composition root: the volume
// publishes go through the circuit breaker and the two availability
// markers do not.
//
// It matters most at the moment a breaker is open, which is exactly the
// moment a reconnect announces online: mqtt.Breaker counts
// ErrNotConnected and ErrConnectionLost as failures, so a connection drop
// is what opens it, and a birth marker refused there leaves every entity
// unavailable under `availability_mode: all` until a recovery probe
// happens to succeed.
func TestBypassForKeepsTheAvailabilityMarkersOffTheBreaker(t *testing.T) {
	t.Parallel()
	const status = testConnected
	gated, direct := &capture{}, &capture{}
	p := New(BypassFor(status, gated, direct), Config{
		Prefix:      "homeassistant",
		StatusTopic: status,
		Layout:      testLayout(),
		QoS:         QoS(1),
		Logger:      slog.New(slog.DiscardHandler),
	})
	ctx := t.Context()
	if err := p.AnnounceOnline(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.AnnounceOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Publish(ctx, "homeassistant/sensor/dev/x/config", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.PublishStatus(ctx, testState, 42); err != nil {
		t.Fatal(err)
	}
	if n := len(direct.records()); n != 2 {
		t.Errorf("%d publishes bypassed the breaker, want the 2 availability markers: %v",
			n, direct.records())
	}
	for _, r := range direct.records() {
		if r.topic != status {
			t.Errorf("%s bypassed the breaker; only the status topic may", r.topic)
		}
	}
	if n := len(gated.records()); n != 2 {
		t.Errorf("%d publishes went through the breaker, want the config and the state: %v",
			n, gated.records())
	}
	for _, r := range gated.records() {
		if r.topic == status {
			t.Errorf("an availability marker went through the breaker — a reconnect after a " +
				"drop would find it open and leave the whole fleet unavailable")
		}
	}
	// Subscriptions always take the gated half, which is the same client's
	// subscribe side either way.
	if err := BypassFor(status, gated, direct).Subscribe(ctx, "homeassistant/#", 1, nil); err != nil {
		t.Fatal(err)
	}
	if len(gated.subs) != 1 || len(direct.subs) != 0 {
		t.Errorf("subscriptions split %d/%d, want all on the gated half", len(gated.subs), len(direct.subs))
	}
}
