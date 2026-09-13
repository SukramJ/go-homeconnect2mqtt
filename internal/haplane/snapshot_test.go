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

// TestSnapshotSubscribesAtTheConfiguredQoS is the pin the read-back window
// did not have, and it is driven at QoS 0 for the reason that matters:
// MQTT_QOS's shipped default is 1, which is also publisher.Config's own
// default, so a window that hard-coded publisher.QoSAtLeastOnce would agree
// with a correct one in every fixture the bridge tests use and be caught by
// none of them.
//
// This repository has already paid for a second spelling of a QoS default
// once — F9, a struct literal that omitted the field and upgraded the whole
// installed base's delivery guarantee, with a broker capture as the only
// evidence. internal/haplane/qos.go exists because of it, and this is the
// one subscription that was still resolving its level on its own.
func TestSnapshotSubscribesAtTheConfiguredQoS(t *testing.T) {
	t.Parallel()
	for _, want := range []byte{0, 1, 2} {
		tr := &replayTransport{}
		p := New(tr, Config{
			Prefix:      "homeassistant",
			StatusTopic: "homeconnect/status",
			Layout:      testLayout{},
			QoS:         QoS(int(want)),
			Logger:      slog.New(slog.DiscardHandler),
		})
		if err := p.Snapshot(t.Context(), "homeassistant/device/+/config", time.Millisecond,
			func(string, []byte) bool { return false }); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		tr.mu.Lock()
		subs := append([]record(nil), tr.subs...)
		tr.mu.Unlock()
		if len(subs) != 1 {
			t.Fatalf("MQTT_QOS %d: %d subscriptions, want 1", want, len(subs))
		}
		if subs[0].qos != want {
			t.Errorf("MQTT_QOS %d: the window subscribed at QoS %d", want, subs[0].qos)
		}
	}
}

// handlerTransport hands the test the subscription's handler instead of
// replaying anything itself, so a delivery can be made from ANOTHER
// goroutine after Subscribe has returned.
//
// replayTransport cannot express the case below. It replays inline inside
// Subscribe, which every other pin here needs, and which also means the
// visit has finished before Snapshot reaches its own select — so "a visit
// still running when the window closes" is unreachable through it and the
// mutation that removes the guard survives.
type handlerTransport struct {
	capture
	mu sync.Mutex
	h  publisher.Handler
}

func (x *handlerTransport) Subscribe(ctx context.Context, filter string, qos byte, h publisher.Handler) error {
	if err := x.capture.Subscribe(ctx, filter, qos, h); err != nil {
		return err
	}
	x.mu.Lock()
	x.h = h
	x.mu.Unlock()
	return nil
}

func (x *handlerTransport) Unsubscribe(context.Context, string) error { return nil }

func (x *handlerTransport) handler() publisher.Handler {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.h
}

// TestNoVisitRunsAfterSnapshotReturns closes the window on the window.
//
// `gated` used to read an atomic flag and then call visit, which is
// publisher.Runtime.snapshot's idiom: a delivery that passes the read can be
// descheduled and run its callback after Snapshot has already returned. That
// is harmless while the callback only touches state the caller has finished
// with, and it is not harmless here — the tombstone read-back's map escapes
// into bridge.priorDocuments, where it is stored and read under a DIFFERENT
// mutex. One live map under two mutexes is a concurrent map write, i.e. a
// process-killing panic rather than a wrong answer.
//
// It is driven rather than left to the race detector, which found nothing
// over -count=8. Two things have to be true for it to fail reliably, and
// the first version of this test had neither: the delivery must come from a
// goroutine that is NOT the one inside Subscribe, and the visit must still
// be inside the callback when the window's timer fires. The assertion is
// then made from inside the visit, after Snapshot has had every chance to
// return.
func TestNoVisitRunsAfterSnapshotReturns(t *testing.T) {
	t.Parallel()
	tr := &handlerTransport{}
	p := snapshotPlane(t, tr)

	var (
		mu        sync.Mutex
		returned  bool
		late      bool
		visited   bool
		delivered = make(chan struct{})
	)
	go func() {
		defer close(delivered)
		for {
			if h := tr.handler(); h != nil {
				h("homeassistant/device/a/config", []byte(`{"components":{}}`), true)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	err := p.Snapshot(t.Context(), "homeassistant/device/+/config", 30*time.Millisecond,
		func(string, []byte) bool {
			mu.Lock()
			visited = true
			mu.Unlock()
			// Longer than the window, so the timer fires while this is
			// still running.
			time.Sleep(250 * time.Millisecond)
			mu.Lock()
			if returned {
				late = true
			}
			mu.Unlock()
			return false
		})
	mu.Lock()
	returned = true
	mu.Unlock()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery never happened")
	}
	mu.Lock()
	gotVisit, gotLate := visited, late
	mu.Unlock()
	if !gotVisit {
		t.Fatal("nothing was delivered, so this test proves nothing")
	}
	if gotLate {
		t.Error("a visit was still running after Snapshot returned: the caller's map is live " +
			"in two goroutines under two different mutexes")
	}
}
