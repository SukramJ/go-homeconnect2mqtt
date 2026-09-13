// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package haplane

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
)

// The device-document pins.
//
// This is ADR 0070 phase 7, step 6: the one step of the phase that can
// destroy an installed Home Assistant setup, because the per-entity
// configs it retracts and the document it publishes cannot coexist and the
// window between them is a fleet with no discovery config at all. Every
// assertion in this file is about that window.

const (
	testPrefix = "homeassistant"
	testNode   = "geschirrspuler"
)

var errBrokerRefused = errors.New("broker refused the packet")

// broker is a publisher.Transport that records what crossed it and can be
// told to refuse one topic.
//
// It records the PAYLOAD length as well as the topic, because the two
// publishes this file distinguishes — a retraction and a document — differ
// in nothing else: both are retained, both go to the discovery prefix, and
// a recorder that kept only topics could not tell "retracted" from
// "written".
type broker struct {
	mu     sync.Mutex
	calls  []call
	refuse string
}

type call struct {
	topic   string
	empty   bool
	payload []byte
}

func (b *broker) Publish(_ context.Context, topic string, payload []byte, _ byte, _ bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuse != "" && topic == b.refuse {
		return errBrokerRefused
	}
	b.calls = append(b.calls, call{topic: topic, empty: len(payload) == 0, payload: append([]byte(nil), payload...)})
	return nil
}

func (b *broker) Subscribe(context.Context, string, byte, publisher.Handler) error { return nil }
func (b *broker) Unsubscribe(context.Context, string) error                        { return nil }

// take returns everything recorded since the last take and clears the log,
// which is how one connection's traffic is separated from the next one's.
func (b *broker) take() []call {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.calls
	b.calls = nil
	return out
}

func (b *broker) setRefuse(topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refuse = topic
}

// retractions is every empty retained publish in a connection's log, in
// order.
func retractions(calls []call) []string {
	var out []string
	for _, c := range calls {
		if c.empty {
			out = append(out, c.topic)
		}
	}
	return out
}

// documentAt is the index of the device document in a connection's log, or
// -1. The document is the one non-empty publish on the bundle topic.
func documentAt(calls []call, topic string) int {
	for i, c := range calls {
		if c.topic == topic && !c.empty {
			return i
		}
	}
	return -1
}

// testBundle is a three-component device document across three platforms,
// which is the smallest shape that makes the superseded set non-trivial:
// the per-entity topic of each carries its own platform segment, so a
// retraction derived from the wrong component would still look plausible.
func testBundle() *discovery.Bundle {
	comp := func(platform, uid string) discovery.Component {
		return discovery.Component{Platform: hacatalog.Platform(platform), UniqueID: uid}
	}
	return &discovery.Bundle{
		NodeID: testNode,
		Device: discovery.DeviceInfo{Identifiers: []string{"homeconnect_" + testNode}, Name: "Geschirrspüler"},
		Origin: discovery.Origin{Name: "go-homeconnect2mqtt"},
		Components: map[string]discovery.Component{
			"bsh_common_setting_powerstate":    comp("select", "homeconnect_geschirrspuler_bsh_common_setting_powerstate"),
			"bsh_common_status_doorstate":      comp("sensor", "homeconnect_geschirrspuler_bsh_common_status_doorstate"),
			"bsh_common_root_activeprogram_on": comp("switch", "homeconnect_geschirrspuler_bsh_common_root_activeprogram_on"),
		},
	}
}

func bundlePlane(t *testing.T, tr publisher.Transport, brokerMax func() (uint32, bool)) *Plane {
	t.Helper()
	p := New(tr, Config{
		Prefix:              testPrefix,
		StatusTopic:         "homeconnect/status",
		Layout:              testLayout{},
		QoS:                 QoS(1),
		Retain:              true,
		BrokerMaxPacketSize: brokerMax,
		Logger:              slog.New(slog.DiscardHandler),
	})
	t.Cleanup(p.Close)
	return p
}

func docTopic() string { return publisher.BundleConfigTopic(testPrefix, testNode) }

// TestTheRetractionsAreReSentAfterAReconnect is the pin this whole step
// rests on, and it is written as the defect and its fix side by side.
//
// A retraction that "succeeded" says the bytes reached a socket. At QoS 0
// that is all it says; the broker may have applied none of them if the
// socket then died. publisher.Runtime remembers which superseded topics it
// has retracted so a steady-state boot does not re-send them forever — a
// memo that is correct per CONNECTION and wrong per process. go-mtec2mqtt
// shipped a process-lifetime runtime, and its in-process retry after a
// reconnect sent ZERO retractions and published the document into a tree
// still holding every per-entity config: measured as `retractions re-sent =
// 0, document published = true, configs still retained = 1`.
//
// Both halves are driven here, against the same code, so the pin states
// what the fix is worth rather than only that it exists:
//
//   - Without Plane.Reconnect, the retry re-sends nothing and publishes the
//     document anyway. That subtest asserts the DEFECT, and it is what
//     turns red if the memo ever stops being per-connection.
//   - With Plane.Reconnect — which is what Bridge.PublishOnline calls at
//     the head of every (re)connect — every superseded topic is re-sent,
//     and every one of them lands before the document.
func TestTheRetractionsAreReSentAfterAReconnect(t *testing.T) {
	t.Parallel()
	b := testBundle()
	want := publisher.SupersededTopics(testPrefix, b)
	if len(want) != len(b.Components) {
		t.Fatalf("the fixture supersedes %d topics for %d components: %v", len(want), len(b.Components), want)
	}

	for _, tc := range []struct {
		name       string
		reconnect  bool
		wantReSent int
	}{
		{"a reconnect rebuilds the runtime", true, 3},
		{"the defect: the memo survives the connection it was written on", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := &broker{}
			p := bundlePlane(t, tr, nil)

			// Connection 1. The retractions go out and the document does
			// not — the socket died between the two, which is the whole
			// hazard. A real link reports that as a write error; here it is
			// injected on the one topic.
			tr.setRefuse(docTopic())
			if _, err := p.PublishBundle(t.Context(), b); err == nil {
				t.Fatal("PublishBundle reported success although the document was refused")
			}
			first := tr.take()
			if got := retractions(first); len(got) != len(want) {
				t.Fatalf("connection 1 retracted %d topics, want %d: %v", len(got), len(want), got)
			}
			if documentAt(first, docTopic()) >= 0 {
				t.Fatal("connection 1 published the document although the broker refused it")
			}

			// The link comes back.
			tr.setRefuse("")
			if tc.reconnect {
				p.Reconnect()
			}

			if _, err := p.PublishBundle(t.Context(), b); err != nil {
				t.Fatalf("PublishBundle on connection 2: %v", err)
			}
			second := tr.take()
			reSent := retractions(second)
			if len(reSent) != tc.wantReSent {
				t.Errorf("connection 2 re-sent %d retractions, want %d: %v", len(reSent), tc.wantReSent, reSent)
			}
			docAt := documentAt(second, docTopic())
			if docAt < 0 {
				t.Fatal("connection 2 did not publish the document")
			}
			if !tc.reconnect {
				// The defect, stated: the document went out into a tree
				// this process believes it cleared and the broker never did.
				return
			}
			slices.Sort(reSent)
			wantSorted := slices.Clone(want)
			slices.Sort(wantSorted)
			if !slices.Equal(reSent, wantSorted) {
				t.Errorf("connection 2 re-sent %v, want %v", reSent, wantSorted)
			}
			if docAt != len(reSent) {
				t.Errorf("the document is at index %d of %d publishes — every retraction must precede it: %v",
					docAt, len(second), second)
			}
		})
	}
}

// TestTheCrashWindowHealsOnTheNextBoot is the other half of the same
// hazard, one process boundary further out.
//
// Retractions on the wire, document not, and the daemon dies. The
// appliance now has NO discovery config: not an unavailable entity, an
// absent one. Nothing on the broker records that a migration was half
// done, so the next boot has to redo both halves — and it does, precisely
// because it remembers nothing: publisher.Runtime's superseded and declared
// maps are per-instance, and a fresh process builds a fresh one.
//
// The assertion is on BOTH halves. A boot that trusted a previous process's
// retraction would publish the document into a tree still holding the
// per-entity configs, which Home Assistant refuses with one WARNING line
// and no entities.
func TestTheCrashWindowHealsOnTheNextBoot(t *testing.T) {
	t.Parallel()
	b := testBundle()
	want := publisher.SupersededTopics(testPrefix, b)
	tr := &broker{}

	// The process that dies.
	crashed := bundlePlane(t, tr, nil)
	tr.setRefuse(docTopic())
	if _, err := crashed.PublishBundle(t.Context(), b); err == nil {
		t.Fatal("PublishBundle reported success although the document was refused")
	}
	if got := retractions(tr.take()); len(got) != len(want) {
		t.Fatalf("the crashing process retracted %d topics, want %d", len(got), len(want))
	}

	// The next boot: a new process over the same broker.
	tr.setRefuse("")
	fresh := bundlePlane(t, tr, nil)
	if _, err := fresh.PublishBundle(t.Context(), b); err != nil {
		t.Fatalf("the next boot: %v", err)
	}
	calls := tr.take()
	reSent := retractions(calls)
	slices.Sort(reSent)
	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)
	if !slices.Equal(reSent, wantSorted) {
		t.Errorf("the next boot re-sent %v, want %v — a boot that trusted the dead process's "+
			"retraction publishes the document into a tree still holding every per-entity config",
			reSent, wantSorted)
	}
	if at := documentAt(calls, docTopic()); at != len(reSent) {
		t.Errorf("the document is at index %d of %d — every retraction must precede it", at, len(calls))
	}
}

// TestADocumentTooLargeForTheBrokerRetractsNothing is the preflight, and
// the assertion is that NOTHING crossed the transport.
//
// go-mqtt reports mqtt.ErrPacketTooLarge from inside the PUBLISH — after
// publisher.Runtime.PublishBundle has already retracted every per-entity
// config the document supersedes. The refusal has to happen one layer up,
// before the retraction, or the failure mode is not "the migration did not
// run" but "the fleet was deleted and nothing replaced it".
func TestADocumentTooLargeForTheBrokerRetractsNothing(t *testing.T) {
	t.Parallel()
	tr := &broker{}
	p := bundlePlane(t, tr, func() (uint32, bool) { return 512, true })

	sent, err := p.PublishBundle(t.Context(), testBundle())
	if !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("err = %v, want ErrDocumentTooLarge", err)
	}
	if sent {
		t.Error("PublishBundle reported the document written")
	}
	if calls := tr.take(); len(calls) != 0 {
		t.Errorf("a refused document still wrote %d messages: %v — the retraction must not have "+
			"happened at all", len(calls), calls)
	}
	if got := p.Declared(); len(got) != 0 {
		t.Errorf("the runtime claims %v after a refusal", got)
	}
}

// TestAnUnknownBrokerMaximumIsNotASmallOne. Three spellings of "nobody
// knows" — no hook at all, a connection that has not answered yet, and a
// broker that deliberately named no limit — and all three publish.
//
// Reading unknown as small would withhold the migration on every MQTT
// 3.1.1 link, where there is no property block to carry a Maximum Packet
// Size at all, and on every connection before the CONNACK has landed.
func TestAnUnknownBrokerMaximumIsNotASmallOne(t *testing.T) {
	t.Parallel()
	for name, hook := range map[string]func() (uint32, bool){
		"no hook wired":             nil,
		"no connection result yet":  func() (uint32, bool) { return 0, false },
		"the broker named no limit": func() (uint32, bool) { return 0, true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := &broker{}
			p := bundlePlane(t, tr, hook)
			if _, err := p.PublishBundle(t.Context(), testBundle()); err != nil {
				t.Fatalf("an unknown limit refused the document: %v", err)
			}
			if documentAt(tr.take(), docTopic()) < 0 {
				t.Error("the document was not published")
			}
		})
	}
}

// TestTheRefusalBoundaryIsThePacketSizeItMeasures drives the preflight at
// the exact byte either side of the limit, against the same arithmetic the
// refusal performs.
//
// A boundary asserted against a re-derivation of the size is a boundary
// asserted against a second copy of the bug. PacketSize is exported so
// there is one copy.
func TestTheRefusalBoundaryIsThePacketSizeItMeasures(t *testing.T) {
	t.Parallel()
	b := testBundle()
	payload, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	size := PacketSize(docTopic(), len(payload))
	if size <= uint64(len(payload)) {
		t.Fatalf("PacketSize(%d payload bytes) = %d — the overhead is not counted", len(payload), size)
	}

	for _, tc := range []struct {
		name    string
		limit   uint32
		refused bool
	}{
		{"exactly the packet size fits", uint32(size), false},
		{"one byte short does not", uint32(size) - 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := &broker{}
			p := bundlePlane(t, tr, func() (uint32, bool) { return tc.limit, true })
			_, err := p.PublishBundle(t.Context(), b)
			if got := errors.Is(err, ErrDocumentTooLarge); got != tc.refused {
				t.Errorf("limit %d: refused = %v, want %v (err = %v)", tc.limit, got, tc.refused, err)
			}
		})
	}
}

// TestThePlaneStatesTheDefaultLegacyTopicForm reads back the one field of
// publisher.Config this bridge depends on by NOT stating it.
//
// LegacyEntityTopics is nil here, which means publisher.LegacyTopicWithNodeID
// alone — the five-segment form this bridge's whole installed fleet is on,
// measured 687 of 687 at step 4, with publisher.LegacyTopicByUniqueID
// reproducing 0. Naming any form REPLACES that default rather than
// extending it, so an addition made in good faith would silently stop
// retracting the shape that actually exists, and the failure looks clean:
// the document publishes, nothing logs, and Home Assistant refuses every
// entity with one WARNING line. That is go-zendure2mqtt's measured fleet
// and the reason the field exists.
func TestThePlaneStatesTheDefaultLegacyTopicForm(t *testing.T) {
	t.Parallel()
	p := bundlePlane(t, &broker{}, nil)
	got := p.LegacyForms()
	want := []string{"publisher.LegacyTopicWithNodeID (default)"}
	if !slices.Equal(got, want) {
		t.Errorf("the plane retracts under %v, want %v", got, want)
	}
	// And the form is the one the fleet is on, spelled as a topic rather
	// than as a function name: five segments, the node id third.
	for _, topic := range publisher.SupersededTopics(testPrefix, testBundle()) {
		parts := strings.Split(topic, "/")
		if len(parts) != 5 || parts[0] != testPrefix || parts[2] != testNode || parts[4] != "config" {
			t.Errorf("superseded topic %q is not the five-segment node-id form", topic)
		}
	}
}

// TestANilDocumentIsRefusedRatherThanPublished: a nil bundle reaching the
// publish path is a programming error, and the library would marshal it to
// the four bytes `null` — a retained document Home Assistant cannot read
// and nothing would ever clear.
func TestANilDocumentIsRefusedRatherThanPublished(t *testing.T) {
	t.Parallel()
	tr := &broker{}
	p := bundlePlane(t, tr, nil)
	_, err := p.PublishBundle(t.Context(), nil)
	if err == nil {
		t.Fatal("a nil document was accepted")
	}
	// THIS refusal, not any refusal. publisher.Runtime rejects a nil bundle
	// too, so a test content with a non-nil error passes with the guard
	// removed — and the guard is what keeps the refusal on this side of the
	// preflight, where it can never be reordered after a retraction.
	if !strings.Contains(err.Error(), "haplane: nil device document") {
		t.Errorf("err = %v, want haplane's own refusal — an error from the library means "+
			"this guard is being masked by it", err)
	}
	if calls := tr.take(); len(calls) != 0 {
		t.Errorf("a nil document wrote %v", calls)
	}
}
