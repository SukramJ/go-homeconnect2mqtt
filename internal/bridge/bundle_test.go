// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// The device-document migration's pins at the bridge level.
//
// internal/haplane pins the ORDER — retract, then publish, on a runtime
// that never outlives its connection. This file pins what the daemon does
// around it: that the orphan sweep runs only when the document actually
// reached the broker, that a (re)connect re-drives the publish, and that
// the one retained topic this daemon now depends on is never offered to
// its own delete pass.

// bundleTopicFor is the topic under test, taken from production code
// rather than spelled here — see hass.Discovery.BundleTopic for why that
// matters.
func bundleTopicFor(b *Bridge, device string) string { return b.hass.BundleTopic(device) }

// waitUntil polls cond until it holds or the budget runs out. The republish
// runs on a goroutine by design (mqtt.Lifecycle runs OnConnect callbacks
// inline on its reconnect loop and a callback that blocks stalls every
// later reconnect), so a pin on it has to wait rather than assume.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// drainReconciles waits for every asynchronous orphan sweep this test
// started to finish.
//
// Draining is the only way to assert on what a sweep did: both outcomes of
// the race between the goroutine and the end of the test look like a pass,
// which is how a sweep pin becomes a flake.
func drainReconciles(t *testing.T, b *Bridge) {
	t.Helper()
	waitUntil(t, "the asynchronous orphan sweeps to finish", func() bool {
		b.reconcileMu.Lock()
		defer b.reconcileMu.Unlock()
		return len(b.reconciling) == 0
	})
}

// TestTheMigrationRetractsEveryPerEntityConfigBeforeTheDocument is the
// whole step, driven end to end over the real pin catalogue: a real
// Bridge, a real hass.Discovery, the real publish plane, and 687 entities.
//
// What it asserts is the operator-visible sequence. Every per-entity config
// topic the previous release published is cleared, all of them land before
// the document, and the document is the last thing written. Home Assistant
// refuses a device document while a per-entity config for the same
// `unique_id` is still retained — symmetrically, and with one
// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` as
// the entire signal — so an ordering regression here is invisible on the
// wire and costs the appliance every entity it has.
//
// It also MEASURES the document, because the preflight in internal/haplane
// is only worth what the number behind it is worth.
func TestTheMigrationRetractsEveryPerEntityConfigBeforeTheDocument(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)

	doc := bundleTopicFor(b, dev.name)
	bundle, err := b.hass.BundleFor(dev.name, dev.app.Info(), dev.app.Entities())
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	superseded := publisher.SupersededTopics(pinPrefix, bundle)

	b.publishDiscovery(t.Context(), dev)

	rec.mu.Lock()
	calls := slices.Clone(rec.pubs)
	rec.mu.Unlock()

	docAt := -1
	cleared := map[string]bool{}
	for i, c := range calls {
		switch {
		case c.topic == doc && !c.retraction:
			docAt = i
		case c.retraction:
			if docAt >= 0 {
				t.Errorf("%s was retracted AFTER the document, at index %d", c.topic, i)
			}
			cleared[c.topic] = true
		}
	}
	if docAt < 0 {
		t.Fatalf("the device document %s was never published", doc)
	}
	for _, topic := range superseded {
		if !cleared[topic] {
			t.Errorf("the per-entity config %s was not retracted before the document", topic)
		}
	}

	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("migration: %d components, %d per-entity configs retracted first, "+
		"document %s is %d bytes (%d on the wire)",
		len(bundle.Components), len(superseded), doc, len(payload),
		haplane.PacketSize(doc, len(payload)))
	if len(superseded) != len(bundle.Components) {
		t.Errorf("%d components supersede %d topics — every component's per-entity config "+
			"must be named, or the ones that are not left behind refuse their own entity",
			len(bundle.Components), len(superseded))
	}
}

// TestTheSweepIsSkippedWhenTheDocumentWasNotPublished is the guard that
// tests the right value.
//
// A build that succeeds and a publish that fails is ordinary — an open
// circuit breaker, a broker refusing the packet size, a context that
// expired part-way through 687 retractions. A sweep that ran there would
// find every per-entity config of the previous release owned and unclaimed
// and clear the lot, with nothing published in their place. go-mtec2mqtt
// shipped this guard asking whether the document had been BUILT while its
// log line said "published" (its PR #54, finding F3).
//
// The evidence read is the SUBSCRIBE list, not the live handler map: the
// snapshot window unsubscribes on the way out, so a pin on the map cannot
// tell a sweep that ran from one that never started.
func TestTheSweepIsSkippedWhenTheDocumentWasNotPublished(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	doc := bundleTopicFor(b, dev.name)
	rec.setFail(doc)

	// A retained per-entity config of ours that nothing will claim — the
	// exact thing an ungated sweep would delete.
	seedRetained(rec, map[string]string{
		"homeassistant/sensor/geschirrspuler/retired_feature/config": ourConfig("retired_feature"),
	})

	b.publishDiscovery(t.Context(), dev)
	// Drained BEFORE the assertion, not in a deferred cleanup after it.
	// reconcileOrphans registers its device synchronously and then does the
	// work on a goroutine, so a test that read the window list the instant
	// publishDiscovery returned was racing the subscribe — and losing that
	// race looks exactly like a sweep that correctly did not run. Removing
	// BOTH guards at once was not caught until this line moved.
	drainReconciles(t, b)

	if windows := rec.discoveryWindows(pinPrefix); len(windows) != 0 {
		t.Errorf("the sweep opened %v although the document was not published", windows)
	}
	if got := retractedTopics(rec); slices.Contains(got, "homeassistant/sensor/geschirrspuler/retired_feature/config") {
		t.Error("the sweep cleared a per-entity config although nothing replaced it")
	}
	if slices.Contains(rec.publishedTopics(), doc) {
		t.Errorf("%s was recorded as published although the broker refused it", doc)
	}
}

// TestTheSweepRunsWhenTheDocumentWasPublished is the other direction, so
// the guard above is not passing by refusing everything.
func TestTheSweepRunsWhenTheDocumentWasPublished(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	const orphan = "homeassistant/sensor/geschirrspuler/retired_feature/config"
	seedRetained(rec, map[string]string{orphan: ourConfig("retired_feature")})

	b.publishDiscovery(t.Context(), dev)

	waitUntil(t, "the sweep to clear the orphan", func() bool {
		return slices.Contains(retractedTopics(rec), orphan)
	})
	if windows := rec.discoveryWindows(pinPrefix); len(windows) == 0 {
		t.Fatal("the sweep opened no snapshot window although the document was published")
	}
}

// TestTheSweepNeverOffersTheDocumentItJustPublished keeps the one retained
// topic that now holds a whole appliance out of the daemon's own delete pass.
//
// hass.Discovery.OwnsConfigTopic declines the device-document form. That
// line was written when this daemon published no document at all and the
// step-5 notes flagged it as "not a guard to delete on sight"; this step is
// where it becomes load-bearing in the other direction, and the decision is
// to KEEP it.
//
// The document is now the single retained topic that holds every entity of
// an appliance. Widening the predicate to reach it would put it inside the
// judgement of a pass whose whole job is to delete what nothing claims —
// and hass.Discovery.IsOwnConfig, the payload check that keeps a SIBLING
// INSTANCE's configs safe (F8), reads a top-level `unique_id` and
// `state_topic` that a device document does not have, so a widened
// predicate would not be narrowed again by it. The document is claimed by
// Declared() as well, so this is the second of two locks; the test drives
// the sweep with an EMPTY claim set, which removes the first one.
func TestTheSweepNeverOffersTheDocumentItJustPublished(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	doc := bundleTopicFor(b, dev.name)

	// Seeded before anything publishes, and the publish is driven directly
	// rather than through publishDiscovery: the sweep the latter starts is
	// asynchronous, and two windows over one stub is a test racing itself
	// rather than a property.
	seedRetained(rec, map[string]string{doc: `{"device":{"identifiers":["homeconnect_geschirrspuler"]},"components":{}}`})
	if _, _, err := b.hass.PublishDeviceBundle(t.Context(), dev.name, dev.app.Info(), dev.app.Entities(), nil); err != nil {
		t.Fatalf("PublishDeviceBundle: %v", err)
	}

	owned, _, err := b.collectOwnConfigs(t.Context(), dev.name)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if slices.Contains(owned, doc) {
		t.Errorf("the sweep offered %s for deletion", doc)
	}
	// With no claim at all, which is the worst case the sweep can be in.
	if orphans := b.orphanTopics(owned, nil); slices.Contains(orphans, doc) {
		t.Errorf("with an empty claim set the sweep would retract %s — the one retained topic "+
			"holding every entity of this appliance", doc)
	}
}

// TestPublishOnlineRepublishesEveryAppliancesDocument is the dedup-gate
// answer, and it is the half that is easy to get wrong by doing nothing.
//
// haplane.Plane.Reconnect throws the discovery runtime away on every
// (re)connect, so the gate that suppresses a repeat publish is OPEN on the
// new connection. An open gate nothing walks through publishes exactly as
// little as a closed one, and nothing else re-drives discovery: onState
// fires when an APPLIANCE connects, the birth handler when HOME ASSISTANT
// restarts, and a broker reconnect is neither. A broker that came back
// without its retained store therefore kept the whole fleet missing until
// the daemon itself was restarted — go-mtec2mqtt's finding F2, and with one
// document per appliance it is the whole fleet rather than a few entities.
func TestPublishOnlineRepublishesEveryAppliancesDocument(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	doc := bundleTopicFor(b, dev.name)
	b.startOne.Do(func() { close(b.started) })

	b.PublishOnline(t.Context())
	waitUntil(t, "the device document on the first connection", func() bool {
		return slices.Contains(rec.publishedTopics(), doc)
	})

	// The broker came back without its retained store. The runtime is
	// rebuilt, so the document must go out again — a dedup gate that
	// survived the reconnect would answer "already published" for bytes no
	// broker holds.
	rec.mu.Lock()
	rec.pubs = nil
	rec.mu.Unlock()

	b.PublishOnline(t.Context())
	waitUntil(t, "the device document again after the reconnect", func() bool {
		return slices.Contains(rec.publishedTopics(), doc)
	})
}

// TestTheReconnectRepublishWaitsForTheOneShotRefresh pins the ordering
// between two passes that would otherwise race.
//
// HASS_DISCOVERY_REFRESH clears every retained config this daemon owns —
// the device documents included — and waits, so the workers re-create the
// entities from scratch. It runs at the head of Run. The (re)connect
// republish runs from the lifecycle's OnConnect hook, which fires BEFORE
// Run on the very first connect. Without a gate the republish can write the
// document the refresh is about to delete, and the flag silently stops
// doing the one thing it exists for.
func TestTheReconnectRepublishWaitsForTheOneShotRefresh(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	doc := bundleTopicFor(b, dev.name)

	// The fleet is emptied first, and that is what makes the observation
	// deterministic rather than a race against the work. With no
	// appliances the pass is microseconds long, so "it has not returned"
	// can only mean "it is still waiting on the gate". Two earlier versions
	// of this test failed to catch the gate's removal for the opposite
	// reason: the pass over a real appliance is a 687-message retraction
	// and a 460 KB document, so it had not returned — and had published
	// nothing yet — whether the gate held or not. A negative asserted
	// against slow work is a negative asserted by nothing.
	b.devices = nil
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		b.republishDiscovery(t.Context())
	}()

	select {
	case <-returned:
		t.Fatal("the republish ran to completion before HASS_DISCOVERY_REFRESH had finished — " +
			"it would write the device document the refresh is about to clear")
	case <-time.After(100 * time.Millisecond):
	}
	if slices.Contains(rec.publishedTopics(), doc) {
		t.Fatalf("%s was published before HASS_DISCOVERY_REFRESH had run", doc)
	}

	b.startOne.Do(func() { close(b.started) })
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the republish never ran after the refresh finished")
	}
	// What it publishes once released is TestPublishOnlineRepublishes-
	// EveryAppliancesDocument's assertion, over a fleet this one has
	// deliberately emptied.
	_ = doc
}

// TestBothSweepGuardsMustFailTogether names a masking pair rather than
// leaving it as two assertions that each pass because of the other.
//
// publishDiscovery refuses to sweep on two independent grounds: the publish
// returned an error, and publisher.Runtime does not claim the topic.
// Removing EITHER alone changes nothing, because a publish that failed is
// also a publish the runtime did not claim — so each mutation is caught
// only by the other guard, and a mutation report reads both as survivors.
//
// They are kept as two because they answer different questions and log
// different reasons, and because the claim check is the one that would
// still hold if PublishDeviceBundle ever grew a path that reported success
// without writing. What has to be pinned is that removing BOTH is caught,
// which is what this drives: the sweep must not run when the document was
// refused, for whatever combination of reasons the code gives.
func TestBothSweepGuardsMustFailTogether(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	doc := bundleTopicFor(b, dev.name)
	rec.setFail(doc)

	topic, _, err := b.hass.PublishDeviceBundle(t.Context(), dev.name, d0(dev), dev.app.Entities(), nil)
	if err == nil {
		t.Fatal("the stub accepted a document it was told to refuse")
	}
	if b.documentIsDeclared(topic) {
		t.Errorf("the runtime claims %s although the broker refused it — the claim check "+
			"would then let the sweep run and clear the previous release's whole fleet", topic)
	}
}

// d0 is dev.app.Info(), named so the call above reads as one line.
func d0(d *Device) profile.DeviceInfo { return d.app.Info() }

// overlapWatch counts how many publishes to one topic are inside the
// transport at the same time.
//
// It holds each of them for a beat, which is the only way to see the
// property: overlap is a fact about WHEN two passes ran, and a recorded
// call list cannot distinguish two passes that ran together from two that
// ran one after the other. Both leave two calls.
type overlapWatch struct {
	topic string
	hold  time.Duration

	mu       sync.Mutex
	inside   int
	deepest  int
	arrivals int
}

func (w *overlapWatch) hook(topic string) {
	if topic != w.topic {
		return
	}
	w.mu.Lock()
	w.inside++
	w.arrivals++
	if w.inside > w.deepest {
		w.deepest = w.inside
	}
	w.mu.Unlock()
	time.Sleep(w.hold)
	w.mu.Lock()
	w.inside--
	w.mu.Unlock()
}

func (w *overlapWatch) read() (deepest, arrivals int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deepest, w.arrivals
}

// TestTheReconnectRepublishDoesNotOverlapItself. A flapping link fires
// OnConnect repeatedly, and every pass costs a snapshot window and a
// ~460 KB document per appliance. Two concurrent passes buy nothing: the
// second is deduplicated against the first anyway, having first paid for
// the window.
//
// What is asserted is OVERLAP, and the previous version of this test did
// not assert it. It counted snapshot windows after four concurrent passes
// and allowed two — but two other mechanisms suppress a second window
// already (b.reconciling gates a re-entrant sweep per device, and
// publisher.Runtime deduplicates an identical document), so deleting the
// coalescing block from republishDiscovery survived 10 runs in 12. A pin
// that catches its own mutation two times in twelve is a pin that reports
// luck.
//
// The transport is the place where overlap is visible: each publish of the
// device document is held inside the stub, and two passes that run
// together are then two publishes inside it at once. The coalescing makes
// that impossible — republishMu serialises the at-most-two passes — so the
// number is exactly one, every time, and it is one for a reason no other
// guard supplies.
func TestTheReconnectRepublishDoesNotOverlapItself(t *testing.T) {
	shortWindow(t)
	// The two-entity appliance again, and for the same reason as the birth
	// pin: over the 687-entity catalogue each pass spends about a second
	// rendering its document before it writes anything, so four passes
	// started together arrive at the transport a second apart and the
	// first one's declaration deduplicates the rest. Un-coalesced overlap
	// was then invisible in four runs in twelve. What is under test is the
	// coalescing, not the render.
	b, dev, rec := birthRaceBridge(t)
	defer drainReconciles(t, b)
	b.startOne.Do(func() { close(b.started) })

	w := &overlapWatch{topic: bundleTopicFor(b, dev.name), hold: 100 * time.Millisecond}
	rec.setOnPublish(w.hook)
	t.Cleanup(func() { rec.setOnPublish(nil) })

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.republishDiscovery(t.Context())
		}()
	}
	wg.Wait()

	deepest, arrivals := w.read()
	if arrivals == 0 {
		t.Fatal("no pass published the device document — the measurement is of nothing")
	}
	if deepest > 1 {
		t.Errorf("%d of %d device-document publishes were inside the transport at once: "+
			"the republish passes overlapped, and each overlap is a second snapshot window "+
			"and a second ~460 KB document against the same connection", deepest, arrivals)
	}

	// And the coalescing's OTHER half: a request made while a pass is
	// running is honoured by exactly one further pass, never by four.
	if windows := rec.discoveryWindows(pinPrefix); len(windows) > 2 {
		t.Errorf("four concurrent republishes opened %d snapshot windows: %v", len(windows), windows)
	}
}

// TestTheDeviceAvailabilityPayloadsAreTheOnesEveryConfigDeclares closes a
// loop that nothing was closing.
//
// availOnline and availOffline are defined from internal/hass's constants,
// so they cannot drift from them by a typo — but they can be SWAPPED, and
// swapping them passed the whole suite: the three tests that touched them
// compared the published byte against the same two constants they were
// testing, and the goldens pin `payload_available` inside the discovery
// payload with nothing comparing that to the word the device worker
// actually writes.
//
// Inverted, every appliance reads `offline` while it is connected. Under
// `availability_mode: all` that is the whole fleet greyed out whenever the
// appliance is up, and nothing on the wire or in a log names the cause.
//
// So the assertion crosses the two planes: the payload the worker publishes
// on the device availability topic, against the payload the rendered
// discovery config tells Home Assistant to expect there.
func TestTheDeviceAvailabilityPayloadsAreTheOnesEveryConfigDeclares(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)

	bundle, err := b.hass.BundleFor(dev.name, dev.app.Info(), dev.app.Entities())
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	raw, err := json.Marshal(bundle.Components[bundle.Keys()[0]])
	if err != nil {
		t.Fatalf("marshal component: %v", err)
	}
	var comp struct {
		Availability []struct {
			Topic        string `json:"topic"`
			Available    string `json:"payload_available"`
			NotAvailable string `json:"payload_not_available"`
		} `json:"availability"`
	}
	if err := json.Unmarshal(raw, &comp); err != nil {
		t.Fatalf("component: %v", err)
	}
	avail := dev.topics.Availability()
	declared := -1
	for i, a := range comp.Availability {
		if a.Topic == avail {
			declared = i
		}
	}
	if declared < 0 {
		t.Fatalf("no component declares %s as an availability source: %+v", avail, comp.Availability)
	}

	for _, tc := range []struct {
		state homeconnect.ConnectionState
		want  string
	}{
		{homeconnect.StateConnected, comp.Availability[declared].Available},
		{homeconnect.StateOffline, comp.Availability[declared].NotAvailable},
	} {
		b.onState(dev, tc.state)
		got, ok := rec.lastPayload(avail)
		if !ok {
			t.Fatalf("%s: nothing was published on %s", tc.state, avail)
		}
		if string(got) != tc.want {
			t.Errorf("%s: the worker wrote %q on %s, every config declares %q there",
				tc.state, got, avail, tc.want)
		}
	}
}

// TestStopDiscoveryClosesTheShutdownWindow drives the gate that keeps a
// discovery publish from landing after the daemon has said it is gone.
//
// The daemon's last act is a retained "offline" on its own status topic,
// and it is the only one that goes out at all on a graceful stop — a clean
// DISCONNECT suppresses the Last Will. Two things in this daemon publish
// discovery asynchronously and outlive the call that started them: the Home
// Assistant birth handler, and the (re)connect republish this step added.
// Either landing after the offline marker writes "online"-era configs to a
// broker this daemon has already told Home Assistant it left.
//
// publisher.Runtime.Close does not cover it and never did: it drains the
// birth-replay worker, which exists only after Runtime.WatchBirth, and this
// daemon watches the birth topic itself. So the gate is this daemon's own.
func TestStopDiscoveryClosesTheShutdownWindow(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	b.startOne.Do(func() { close(b.started) })

	b.StopDiscovery()
	b.republishDiscovery(t.Context())
	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)

	if got := rec.publishedTopics(); slices.Contains(got, bundleTopicFor(b, dev.name)) {
		t.Errorf("a discovery publish reached the broker after StopDiscovery: %v", got)
	}
	if windows := rec.discoveryWindows(pinPrefix); len(windows) != 0 {
		t.Errorf("a sweep opened %v after StopDiscovery", windows)
	}
}

// TestTheMigrationBudgetIsNotOnePublishesBudget states an intent no stub
// can exercise, so that the constant is not quietly reduced to the one it
// sits next to.
//
// bundlePublishTimeout bounds a whole migration: 687 retained retractions,
// each waiting on its own acknowledgement, and then a ~460 KB document.
// publishTimeout bounds ONE publish. Cutting the first to the second is
// invisible against any in-process stub and is exactly the change that
// turns a slow broker into the half-migrated state — per-entity configs
// gone, document not written — that the ordering exists to avoid.
func TestTheMigrationBudgetIsNotOnePublishesBudget(t *testing.T) {
	t.Parallel()
	if bundlePublishTimeout < 10*publishTimeout {
		t.Errorf("bundlePublishTimeout = %v against publishTimeout = %v: the migration is "+
			"hundreds of round trips, not one, and cutting it off halfway leaves the "+
			"appliance with no discovery config at all", bundlePublishTimeout, publishTimeout)
	}
}

// setGate installs the gate the recorder asks at publish time. See
// subRecorder.gate.
func (s *subRecorder) setGate(f func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = f
}

// gateOf is the "has Run released the one-shot refresh gate?" question, as
// a closure the recorder can ask on any goroutine.
func gateOf(b *Bridge) func() bool {
	return func() bool {
		select {
		case <-b.started:
			return true
		default:
			return false
		}
	}
}

// preGateWrites is everything written before the one-shot
// HASS_DISCOVERY_REFRESH migration finished, except the migration's own
// work.
//
// The exception is exactly the refresh's retraction list and nothing else,
// which is what makes the rest attributable. refreshDiscoveryOnce clears
// what the snapshot window found plus the device documents it names
// explicitly; the test seeds a retained tree holding no discovery config
// at all, so the migration's entire output is a retraction of each
// appliance's document topic. Anything else written in that window came
// from the birth replay — its 687 per-entity retractions first, its
// ~473 KB document after them — and every one of those writes is into the
// window the refresh is about to clear.
func preGateWrites(rec *subRecorder, refreshOwns []string) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, p := range rec.pubs {
		if !p.preGate {
			continue
		}
		if p.retraction && slices.Contains(refreshOwns, p.topic) {
			continue // the migration clearing a document it named itself
		}
		out = append(out, p.topic)
	}
	return out
}

// birthRaceBridge is pinBridge's wiring over a two-entity appliance: the
// real Bridge, the real hass.Discovery and the real publish plane, with a
// description small enough that a discovery pass is instantaneous. See the
// note in TestTheBirthReplayCannotPublishIntoTheRefreshWindow.
func birthRaceBridge(t *testing.T) (*Bridge, *Device, *subRecorder) {
	t.Helper()
	return smallBridge(t, nil)
}

// smallBridge is birthRaceBridge with the broker's advertised Maximum
// Packet Size chosen by the caller, so the preflight can be driven against
// the document that is actually published rather than against the one the
// renderer produced before the removals were written into it.
//
// A nil hook is UNKNOWN, which publishes — see haplane.Config's
// BrokerMaxPacketSize for why unknown may never be read as small.
func smallBridge(t *testing.T, maxPacket func() (uint32, bool)) (*Bridge, *Device, *subRecorder) {
	t.Helper()
	b, rec := smallBridgeWith(t, maxPacket, pinDevice)
	return b, b.devices[0], rec
}

// smallBridgeWith is smallBridge over several appliances, for the pins
// whose property only exists with more than one — a fleet-wide snapshot
// window that stops at the first document it sees is indistinguishable
// from a correct one on a one-appliance fixture.
func smallBridgeWith(
	t *testing.T, maxPacket func() (uint32, bool), names ...string,
) (*Bridge, *subRecorder) {
	t.Helper()
	cfg := testCfg()
	cfg.MQTTTopic = pinRoot
	cfg.HASSEnable = true
	cfg.HASSBaseTopic = pinPrefix

	rec := &subRecorder{}
	logger := slog.New(slog.DiscardHandler)
	plane := planeWithLimit(t, rec, cfg.QoSLevel(), cfg.RetainEnabled(), maxPacket)
	desc := smallDescription(t)
	specs := make([]DeviceSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, DeviceSpec{
			Config: profile.DeviceConfig{
				// 127.0.0.1:80 refuses fast, so Run's workers cycle
				// through the offline path instead of hanging on a dial
				// to an address nothing answers.
				Name: name, Host: "127.0.0.1",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: desc,
		})
	}
	b, err := New(Deps{
		Config:  cfg,
		MQTT:    rec,
		Plane:   plane,
		Logger:  logger,
		HASS:    hass.New(plane, pinPrefix, pinRoot, cfg.Language, false, logger),
		Devices: specs,
	})
	if err != nil {
		t.Fatalf("bridge.New: %v", err)
	}
	return b, rec
}

// TestTheBirthReplayCannotPublishIntoTheRefreshWindow drives Run, in Run's
// own order, because that order is the defect.
//
// #45 gave this daemon one gate — `started`, closed once the one-shot
// HASS_DISCOVERY_REFRESH migration has finished — and stated the invariant
// it buys: the first connect's republish cannot publish a device document
// into the window the refresh is about to clear. The (re)connect republish
// waited on it. The OTHER asynchronous discovery publisher, the one
// StopDiscovery's own comment names as the second of two, did not.
//
// And it is not a race that needs bad luck, it is a race with a driver.
// Home Assistant publishes homeassistant/status RETAINED, this handler
// deliberately keeps retained deliveries, and Run subscribes it inside
// subscribeCommands — BEFORE refreshDiscoveryOnce. So the broker replays
// `online` the instant the subscription exists and the birth pass runs
// against a fleet the refresh is about to delete: 687 retractions and a
// ~473 KB document written, the refresh then retracting that document,
// Home Assistant removing the device and every entity on it, and a
// republish three seconds later putting them back. The end state is
// correct, which is why nothing failed; the cost is a whole extra
// migration per boot per appliance, a second concurrent fleet-wide
// snapshot window, and a stretch of time in which the appliance has no
// discovery config at all — made permanent by a shutdown or a link drop
// inside it.
//
// The assertion is read at PUBLISH time (subRecorder.gate), not off the
// call list afterwards: once the gate opens every publish is legitimate,
// so a test that snapshots the list when it opens is racing the passes it
// is trying to judge.
func TestTheBirthReplayCannotPublishIntoTheRefreshWindow(t *testing.T) {
	shortWindow(t)
	prevSettle := refreshSettleDelay.Get()
	refreshSettleDelay.Set(200 * time.Millisecond)
	t.Cleanup(func() { refreshSettleDelay.Set(prevSettle) })

	// A SMALL appliance on purpose, unlike every other pin in this file.
	// What is being timed is the gate, not the render: over the 687-entity
	// pin catalogue an un-gated birth pass spends longer building its
	// document than the whole one-shot migration takes, so its writes land
	// after the gate opened by accident and the defect hides behind its own
	// cost. With a two-entity appliance the un-gated pass is writing within
	// microseconds of the SUBSCRIBE that replayed the birth message, which
	// is where the defect actually lives — the gate, not the arithmetic.
	b, dev, rec := birthRaceBridge(t)
	b.cfg.HASSDiscoveryRefresh = true
	rec.setGate(gateOf(b))
	// Home Assistant announced itself before this daemon started, and the
	// announcement is retained: the broker replays it on subscribe.
	seedRetained(rec, map[string]string{b.hass.BirthTopic(): "online"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	waitUntil(t, "Run to finish the one-shot refresh", gateOf(b))
	doc := bundleTopicFor(b, dev.name)
	waitUntil(t, "the birth pass to publish the device document", func() bool {
		return slices.Contains(rec.publishedTopics(), doc)
	})

	if early := preGateWrites(rec, []string{doc}); len(early) != 0 {
		t.Errorf("%d discovery writes went out before HASS_DISCOVERY_REFRESH had finished, "+
			"the first of them %q — the birth replay walked past the gate, and every one of "+
			"those writes is into the window the refresh is about to clear",
			len(early), early[0])
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// The appliance workers dial an address nothing answers; a slow
		// unwind is not what this test is about.
	}
	drainReconciles(t, b)
}

// TestTheBirthHandlerAndTheReconnectHookShareOnePass. The two
// asynchronous discovery publishers are one function, and that is the
// property the gate in the test above depends on: an invariant stated on
// republishDiscovery is an invariant of the daemon only while nothing else
// publishes the fleet.
//
// A birth handler that looped over b.devices itself would satisfy the gate
// pin on every boot where HASS_DISCOVERY_REFRESH is off — which is every
// boot but one — so what is measured here is the other half: a birth
// delivery and a (re)connect that arrive together must not run two passes
// over the same appliance at the same time. Measured inside the transport,
// because that is the only place overlap exists; the per-device sweep gate
// and the runtime's dedup both hide it from a call list.
func TestTheBirthHandlerAndTheReconnectHookShareOnePass(t *testing.T) {
	shortWindow(t)
	b, dev, rec := birthRaceBridge(t)
	defer drainReconciles(t, b)
	b.startOne.Do(func() { close(b.started) })

	if err := b.subscribeBirth(t.Context()); err != nil {
		t.Fatalf("subscribeBirth: %v", err)
	}
	rec.mu.Lock()
	h := rec.handlers[b.hass.BirthTopic()]
	rec.mu.Unlock()
	if h == nil {
		t.Fatal("the birth topic was not subscribed")
	}

	w := &overlapWatch{topic: bundleTopicFor(b, dev.name), hold: 100 * time.Millisecond}
	rec.setOnPublish(w.hook)
	t.Cleanup(func() { rec.setOnPublish(nil) })

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h(&mqtt.Message{Topic: b.hass.BirthTopic(), Payload: []byte("online"), Retain: true})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.republishDiscovery(t.Context())
	}()
	wg.Wait()
	waitUntil(t, "the birth passes to publish the device document", func() bool {
		_, arrivals := w.read()
		return arrivals > 0
	})
	drainReconciles(t, b)

	if deepest, arrivals := w.read(); deepest > 1 {
		t.Errorf("%d of %d device-document publishes were inside the transport at once — "+
			"the birth handler is not going through the one coalesced pass", deepest, arrivals)
	}
	if sweeps := len(rec.discoveryWindows(pinPrefix)); sweeps > 2 {
		t.Errorf("three birth deliveries and a reconnect opened %d snapshot windows", sweeps)
	}
}

// TestAStoppedDiscoveryRepublishDoesNotParkOnTheStartGate names what the
// second discoveryStopped check is for, because "defence in depth" is not
// an assertion.
//
// publishDiscovery tests the same flag, so removing it from
// republishDiscovery alone changes no published byte and survived the
// suite — a third unnamed masking pair of the M6/M7 shape. The two are NOT
// equivalent, though, and the difference is ORDER: republishDiscovery
// reads the flag BEFORE it waits on `started`, and publishDiscovery can
// only read it after. A shutdown that arrives before Run has finished the
// one-shot refresh — a boot interrupted by a Ctrl-C, a supervisor stopping
// the add-on during its first migration — leaves the pass parked on a gate
// that will never open, holding the birth handler's goroutine and, behind
// republishMu, every later pass, for as long as the process lives.
//
// So the assertion is that the call RETURNS, which is the only thing the
// inner check cannot provide.
func TestAStoppedDiscoveryRepublishDoesNotParkOnTheStartGate(t *testing.T) {
	shortWindow(t)
	b, _, rec := birthRaceBridge(t)
	// `started` is deliberately NOT closed: this is a shutdown during the
	// one-shot migration, which is the window the flag has to be read in.
	b.StopDiscovery()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		b.republishDiscovery(context.Background())
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("republishDiscovery is still waiting for the one-shot refresh after StopDiscovery: " +
			"the shutdown window is closed after the gate instead of before it, so the pass parks " +
			"forever and takes the birth handler's goroutine and every later pass with it")
	}
	if got := rec.publishedTopics(); len(got) != 0 {
		t.Errorf("a stopped daemon published %v", got)
	}
}
