// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package haplane

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// replayTransport is a capture that also does what a broker does: it hands
// a fresh subscriber the retained messages matching its filter, inline on
// the Subscribe call.
//
// A window that opens before its subscription exists sees none of the
// messages it was opened for, so a stub that does not replay cannot tell a
// working snapshot from a broken one.
type replayTransport struct {
	capture
	mu          sync.Mutex
	retained    map[string][]byte
	unsubscribe []string
	subErr      error
	unsubErr    error
}

func (r *replayTransport) Subscribe(
	ctx context.Context, filter string, qos byte, h publisher.Handler,
) error {
	if r.subErr != nil {
		return r.subErr
	}
	if err := r.capture.Subscribe(ctx, filter, qos, h); err != nil {
		return err
	}
	r.mu.Lock()
	pairs := make([][2]any, 0, len(r.retained))
	for topic, payload := range r.retained {
		if publisher.MatchFilter(filter, topic) {
			pairs = append(pairs, [2]any{topic, payload})
		}
	}
	r.mu.Unlock()
	for _, p := range pairs {
		h(p[0].(string), p[1].([]byte), true) //nolint:forcetypeassert // written by this stub
	}
	return nil
}

func (r *replayTransport) Unsubscribe(_ context.Context, filter string) error {
	r.mu.Lock()
	r.unsubscribe = append(r.unsubscribe, filter)
	r.mu.Unlock()
	return r.unsubErr
}

func (r *replayTransport) tornDown() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.unsubscribe...)
}

func snapshotPlane(t *testing.T, tr publisher.Transport) *Plane {
	t.Helper()
	return New(tr, Config{
		Prefix:      "homeassistant",
		StatusTopic: "homeconnect/status",
		Layout:      testLayout{},
		QoS:         QoS(1),
		Logger:      slog.New(slog.DiscardHandler),
	})
}

// TestSnapshotCollectsTheRetainedTreeAndTakesItsSubscriptionDown is the
// happy path and the teardown in one, because leaving the subscription
// installed is the failure that does not look like one: the handler stays
// registered, keeps accumulating into a worklist nobody reads, and a client
// that replays its subscriptions carries it across the very reconnect that
// stranded it.
func TestSnapshotCollectsTheRetainedTreeAndTakesItsSubscriptionDown(t *testing.T) {
	t.Parallel()
	tr := &replayTransport{retained: map[string][]byte{
		"homeassistant/device/a/config": []byte(`{"components":{}}`),
		"homeassistant/device/b/config": []byte(`{"components":{}}`),
		// Matches nothing this filter asks for.
		"homeassistant/sensor/a/x/config": []byte(`{}`),
		// An empty retained payload is a topic the broker is already
		// clearing: nothing to read, and reading it as a document would
		// make it prior state that declares nothing.
		"homeassistant/device/c/config": nil,
	}}
	p := snapshotPlane(t, tr)

	var seen []string
	err := p.Snapshot(t.Context(), "homeassistant/device/+/config", 50*time.Millisecond,
		func(topic string, _ []byte) bool {
			seen = append(seen, topic)
			return false
		})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(seen) != 2 {
		t.Errorf("the window collected %v, want the two device documents", seen)
	}
	if got := tr.tornDown(); len(got) != 1 || got[0] != "homeassistant/device/+/config" {
		t.Errorf("the window tore down %v, want exactly its own filter", got)
	}
}

// TestSnapshotStopsAsSoonAsTheCallerHasWhatItCameFor — the read-back sits in front of the migration, so every millisecond the
// window holds is a millisecond the appliance's discovery config is older
// than it needs to be. A caller that knows how many retained messages it
// expects must not pay the whole window for the ones it already has.
//
// The budget here is a second, and the assertion is that the call returns
// in a small fraction of it — a window that waited would take the lot.
func TestSnapshotStopsAsSoonAsTheCallerHasWhatItCameFor(t *testing.T) {
	t.Parallel()
	tr := &replayTransport{retained: map[string][]byte{
		"homeassistant/device/a/config": []byte(`{"components":{}}`),
	}}
	p := snapshotPlane(t, tr)

	start := time.Now()
	if err := p.Snapshot(t.Context(), "homeassistant/device/+/config", time.Second,
		func(string, []byte) bool { return true }); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("a satisfied window took %v of its 1s budget", elapsed)
	}
	if got := tr.tornDown(); len(got) != 1 {
		t.Errorf("an early-closed window tore down %v", got)
	}
}

// TestSnapshotRunsItsWindowOutWhenNothingCompletesIt states the other half
// deliberately: MQTT has no end-of-retained signal, so a broker that holds
// no document at all can only be distinguished from a slow one by waiting.
func TestSnapshotRunsItsWindowOutWhenNothingCompletesIt(t *testing.T) {
	t.Parallel()
	tr := &replayTransport{}
	p := snapshotPlane(t, tr)
	start := time.Now()
	if err := p.Snapshot(t.Context(), "homeassistant/device/+/config", 80*time.Millisecond,
		func(string, []byte) bool { return true }); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("an unsatisfied window returned after %v, so an installation whose broker "+
			"is merely slow would be read as one that holds nothing", elapsed)
	}
}

// TestSnapshotTearsDownOnACancelledContext is the path where leaving the
// subscription installed does the most damage, which is why the teardown
// runs on a context of its own.
func TestSnapshotTearsDownOnACancelledContext(t *testing.T) {
	t.Parallel()
	tr := &replayTransport{}
	p := snapshotPlane(t, tr)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Snapshot(ctx, "homeassistant/device/+/config", time.Minute,
		func(string, []byte) bool { return false }); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the caller's context; a partial read is the caller's to "+
			"judge, with the error in hand to judge it by", err)
	}
	if got := tr.tornDown(); len(got) != 1 {
		t.Errorf("a cancelled window tore down %v", got)
	}
}

// TestSnapshotReportsASubscribeItCouldNotMake, so a caller does not read a
// window that never opened as a broker that holds nothing.
func TestSnapshotReportsASubscribeItCouldNotMake(t *testing.T) {
	t.Parallel()
	want := errors.New("no")
	p := snapshotPlane(t, &replayTransport{subErr: want})
	err := p.Snapshot(t.Context(), "homeassistant/device/+/config", time.Millisecond,
		func(string, []byte) bool { return false })
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want the subscribe failure", err)
	}
}

// TestSnapshotWithoutATransportIsReportedRatherThanAPanic. Every caller is
// on a publish path, and a daemon that loses one read is a better outcome
// than a daemon that dies.
func TestSnapshotWithoutATransportIsReportedRatherThanAPanic(t *testing.T) {
	t.Parallel()
	p := &Plane{logger: slog.New(slog.DiscardHandler)}
	if err := p.Snapshot(t.Context(), "x", time.Millisecond,
		func(string, []byte) bool { return false }); err == nil {
		t.Error("a snapshot without a transport reported success")
	}
}
