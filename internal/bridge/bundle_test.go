// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
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
		haplanePacketSize(doc, len(payload)))
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

// TestTheSweepNeverOffersTheDocumentItJustPublished.
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
	if _, err := b.hass.PublishDeviceBundle(t.Context(), dev.name, dev.app.Info(), dev.app.Entities()); err != nil {
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

	// started is still open: Run has not reached the end of the refresh.
	b.PublishOnline(t.Context())
	time.Sleep(50 * time.Millisecond)
	if slices.Contains(rec.publishedTopics(), doc) {
		t.Fatalf("%s was published before HASS_DISCOVERY_REFRESH had run", doc)
	}

	b.startOne.Do(func() { close(b.started) })
	waitUntil(t, "the document once the refresh has finished", func() bool {
		return slices.Contains(rec.publishedTopics(), doc)
	})
}

// TestTheReconnectRepublishDoesNotOverlapItself. A flapping link fires
// OnConnect repeatedly, and every pass costs a snapshot window and a
// ~460 KB document per appliance. Two concurrent passes buy nothing: the
// second is deduplicated against the first anyway, having first paid for
// the window.
func TestTheReconnectRepublishDoesNotOverlapItself(t *testing.T) {
	shortWindow(t)
	b, _, _, rec := pinBridge(t)
	defer drainReconciles(t, b)
	b.startOne.Do(func() { close(b.started) })

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.republishDiscovery(t.Context())
		}()
	}
	wg.Wait()

	// At most two: whoever takes the pending flag runs the fleet, and a
	// request that arrives after it was taken is honoured by exactly one
	// further pass. Four passes would mean the coalescing does nothing.
	windows := rec.discoveryWindows(pinPrefix)
	if len(windows) > 2 {
		t.Errorf("four concurrent republishes opened %d snapshot windows: %v", len(windows), windows)
	}
}

// haplanePacketSize mirrors haplane.PacketSize for the log line above. It
// is a test-local copy on purpose: importing the package here only to
// format a number would put internal/bridge's pins in the position of
// depending on an arithmetic they do not assert. The arithmetic itself is
// pinned where it is used, in internal/haplane.
func haplanePacketSize(topic string, payloadLen int) int { return payloadLen + len(topic) + 64 }
