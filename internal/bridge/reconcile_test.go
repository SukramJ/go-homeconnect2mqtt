// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// The orphan sweep's pins.
//
// The sweep is the one thing in this daemon that DELETES retained state
// on a broker it shares with every other Home Assistant integration and
// possibly with a second instance of itself, so its predicates are pinned
// by DRIVING the sweep rather than by asking them. That distinction is
// not pedantry: go-mtec2mqtt's step-5 PR asserted its ownership
// predicates directly, recorded a two-instance overlap as harmless, and
// one PR later the same overlap deleted a sibling instance's entire fleet
// (its PR #54, finding F4). A test that only questions a predicate cannot
// see what the pass built out of it.

// seedRetained installs a retained discovery tree on the stub broker.
func seedRetained(rec *subRecorder, tree map[string]string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.retained == nil {
		rec.retained = map[string][]byte{}
	}
	for topic, payload := range tree {
		rec.retained[topic] = []byte(payload)
	}
}

// retractedTopics is every topic the recorder saw cleared: an empty
// retained payload, which is MQTT's deletion.
func retractedTopics(rec *subRecorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, p := range rec.pubs {
		if p.retraction {
			out = append(out, p.topic)
		}
	}
	sort.Strings(out)
	return out
}

// ourConfig is a retained config payload of THIS instance: a unique_id in
// the homeconnect_ namespace and a state topic under this daemon's root.
func ourConfig(key string) string {
	return `{"unique_id":"homeconnect_geschirrspuler_` + key +
		`","state_topic":"` + pinRoot + `/` + pinDevice + `/X/state"}`
}

// siblingConfig is the same entity published by a SECOND instance of this
// daemon: a different MQTT_TOPIC root, the same HASS_BASE_TOPIC, and an
// appliance the operator happened to give the same name. Every identity
// string is byte-identical to ours — node id, unique_id, config topic —
// because MQTT_TOPIC appears in none of them. That is finding F8, and the
// state topic is the only place the two differ.
func siblingConfig(key string) string {
	return `{"unique_id":"homeconnect_geschirrspuler_` + key +
		`","state_topic":"other_root/` + pinDevice + `/X/state"}`
}

// shortWindow shrinks the snapshot window for the duration of a test. The
// stub broker flushes its retained tree inline on subscribe, so the
// window only has to be long enough to be entered.
func shortWindow(t *testing.T) {
	t.Helper()
	prev := reconcileCollectWindow
	reconcileCollectWindow = 20 * time.Millisecond
	t.Cleanup(func() { reconcileCollectWindow = prev })
}

// TestReportOnlySweepOverTheRealFleet is the measurement this migration
// step owed: what a retracting pass WOULD have cleared, run against a
// deliberately hostile retained tree, with a reason recorded for every
// survivor.
//
// The fan-out of reasons is what makes the predicate trustworthy. A pass
// that spares everything for one reason has only been shown to work in
// one direction.
func TestReportOnlySweepOverTheRealFleet(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	published := b.hass.PublishDevice(t.Context(), dev.name, dev.app.Info(), dev.app.Entities())
	if len(published) == 0 {
		t.Fatal("the discovery builder published nothing")
	}
	// One live config, taken from the set just published, so the claim
	// check is exercised against a real topic rather than a made-up one.
	live := sortedKeys(published)[0]

	tree := map[string]string{
		// Ours, no longer published: the one thing a sweep exists for.
		"homeassistant/sensor/geschirrspuler/retired_feature/config": ourConfig("retired_feature"),
		// Ours and live.
		live: ourConfig("live"),
		// A SIBLING INSTANCE of this daemon, same device name, different
		// MQTT_TOPIC. Identical topic, identical unique_id (F8).
		"homeassistant/sensor/geschirrspuler/sibling_only/config": siblingConfig("sibling_only"),
		// Other integrations sharing the discovery prefix.
		"homeassistant/sensor/zigbee2mqtt_bridge/state/config":  `{"unique_id":"zigbee2mqtt_x","state_topic":"zigbee2mqtt/x"}`,
		"homeassistant/binary_sensor/tasmota_ABC/status/config": `{"unique_id":"tasmota_ABC"}`,
		// A device document: not a form this daemon publishes (yet).
		"homeassistant/device/geschirrspuler/config": `{"dev":{"ids":["homeconnect_geschirrspuler"]}}`,
		// A four-segment per-entity config: the node-id-less form both
		// sibling bridges are on, and the one Tasmota publishes.
		"homeassistant/sensor/homeconnect_geschirrspuler_x/config": ourConfig("x"),
		// A platform this daemon never emits.
		"homeassistant/climate/geschirrspuler/thermostat/config": ourConfig("thermostat"),
		// Another appliance of ours, which this per-device pass is not
		// asked about. It is claimed by nobody in this test, so only the
		// device scope keeps it alive.
		"homeassistant/sensor/backofen/other_device/config": `{"unique_id":"homeconnect_backofen_x","state_topic":"` + pinRoot + `/Backofen/X/state"}`,
		// Already cleared by somebody.
		"homeassistant/sensor/geschirrspuler/emptied/config": "",
	}
	seedRetained(rec, tree)

	owned, res, err := b.collectOwnConfigs(t.Context(), dev.name)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	orphans := b.orphanTopics(owned, published)

	if len(retractedTopics(rec)) != 0 {
		t.Errorf("a ReportOnly pass retracted %v — it must not touch the broker", retractedTopics(rec))
	}
	want := []string{"homeassistant/sensor/geschirrspuler/retired_feature/config"}
	if len(orphans) != 1 || orphans[0] != want[0] {
		t.Errorf("would retract %v, want %v", orphans, want)
	}
	t.Logf("ReportOnly over the real fleet: %d retained configs offered, %d owned by topic, "+
		"%d claimed, %d would be retracted: %v",
		len(tree), res.Inspected, len(published), len(orphans), orphans)
	for _, survivor := range sortedKeys(tree) {
		if tree[survivor] == "" || survivor == want[0] {
			continue
		}
		t.Logf("  spared: %s", survivor)
	}
}

// TestSweepSparesASiblingInstancesConfigs is F8's protection, driven
// rather than asked.
//
// Two instances of this daemon on one broker with different MQTT_TOPIC
// roots, the same HASS_BASE_TOPIC and an appliance name in common publish
// the SAME 687 config topics with the SAME 687 unique_ids: MQTT_TOPIC
// appears in neither, nor in the node id, nor in default_entity_id. Today
// that is 687 independent last-writer-wins topics with IsOwnConfig
// preventing mutual retraction, and this is the test that says the sweep
// did not lose that property when the plane moved.
//
// It matters more than it looks. The sibling has no reason to republish:
// its own discovery pass runs when its appliance reconnects, which may be
// days away, and in the meantime Home Assistant has forgotten every
// entity it owns.
func TestSweepSparesASiblingInstancesConfigs(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	sibling := "homeassistant/sensor/geschirrspuler/sibling_only/config"
	seedRetained(rec, map[string]string{sibling: siblingConfig("sibling_only")})

	// Nothing published: the claim set is empty, which is the WORST case
	// — every owned topic is unclaimed, so only the payload check stands
	// between the sibling's fleet and deletion.
	cleared := b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{})
	if got := retractedTopics(rec); len(got) != 0 || cleared != 0 {
		t.Fatalf("retracted %v (%d) — that is a SIBLING instance's fleet, and it will not "+
			"republish (F8). The topic, the node id and the unique_id are identical to ours; "+
			"only the state topic in the payload is not", got, cleared)
	}
}

// TestAStaggeredUpgradeDoesNotDeleteTheSiblingsFleet is the shape that
// falsified go-mtec2mqtt's "harmless" verdict one PR after it was
// written: instance A migrates to a device bundle and stops publishing
// per-entity configs, so its published set no longer names the 687 topics
// the not-yet-upgraded instance B still owns.
//
// Here the upgrade is simulated the only way it can be before step 6
// exists — by publishing nothing at all, which is exactly what an
// upgraded instance's per-entity set looks like.
func TestAStaggeredUpgradeDoesNotDeleteTheSiblingsFleet(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	tree := map[string]string{}
	for _, key := range []string{"mode", "operationstate", "doorstate"} {
		tree["homeassistant/sensor/geschirrspuler/"+key+"/config"] = siblingConfig(key)
	}
	seedRetained(rec, tree)

	if cleared := b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{}); cleared != 0 {
		t.Fatalf("a staggered upgrade retracted %v — that is the not-yet-upgraded sibling "+
			"instance's entire fleet", retractedTopics(rec))
	}
}

// TestSweepDoesNotRetractASecondAppliancesConfigs is the device scope.
//
// This daemon mirrors several appliances that connect independently, so
// the window in which only the first has published is the normal case
// rather than a race. A fleet-wide predicate would call the second
// appliance's configs orphans there and clear them.
func TestSweepDoesNotRetractASecondAppliancesConfigs(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)
	// The second appliance is CONFIGURED — it has simply not connected
	// yet. That is what makes the scope measurable: a pass that judged the
	// whole fleet would own its configs and, with nothing claimed, clear
	// them. A fixture that left it out of b.devices would pass either way.
	b.devices = append(b.devices, &Device{name: "Backofen", topics: newDeviceTopics(pinRoot, "Backofen")})

	other := "homeassistant/sensor/backofen/bsh_common_status_operationstate/config"
	seedRetained(rec, map[string]string{
		other: `{"unique_id":"homeconnect_backofen_x","state_topic":"` + pinRoot + `/Backofen/X/state"}`,
	})

	if cleared := b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{}); cleared != 0 {
		t.Fatalf("the %s pass retracted %v — a second appliance of this same daemon, "+
			"which has simply not connected yet", dev.name, retractedTopics(rec))
	}
}

// TestSweepRetractsOurOwnOrphan is the positive control. Without it every
// test above passes on a sweep that retracts nothing at all, which is a
// sweep that has stopped working.
func TestSweepRetractsOurOwnOrphan(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	orphan := "homeassistant/sensor/geschirrspuler/retired_feature/config"
	seedRetained(rec, map[string]string{orphan: ourConfig("retired_feature")})

	if cleared := b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{}); cleared != 1 {
		t.Fatalf("cleared %d, want 1 — the sweep retracted nothing, which is how every "+
			"other test in this file passes for the wrong reason", cleared)
	}
	if got := retractedTopics(rec); len(got) != 1 || got[0] != orphan {
		t.Errorf("retracted %v, want [%s]", got, orphan)
	}
}

// TestSweepSparesAConfigWhoseOwnPublishFailed is the first of the two
// claim sets.
//
// A config whose publish failed — an open circuit breaker, a broker that
// refused it — is in the batch's published set and NOT in the runtime's
// declared set, because the runtime records only what the broker
// accepted. Subtracting declared alone would retract an entity this
// daemon is actively trying to create.
func TestSweepSparesAConfigWhoseOwnPublishFailed(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	topic := "homeassistant/sensor/geschirrspuler/never_landed/config"
	seedRetained(rec, map[string]string{topic: ourConfig("never_landed")})

	// Minted by this batch, never declared by the runtime.
	if cleared := b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{topic: true}); cleared != 0 {
		t.Fatalf("retracted %v — a config this batch minted but could not publish", retractedTopics(rec))
	}
}

// TestSweepSparesADeclaredConfigOutsideThisBatch is the second claim set.
//
// A config published on an earlier, larger batch that a transient
// classification shrank is in the runtime's declared set and not in this
// batch's published map. Subtracting the batch alone would clear it.
func TestSweepSparesADeclaredConfigOutsideThisBatch(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	topic := "homeassistant/sensor/geschirrspuler/earlier_batch/config"
	payload := ourConfig("earlier_batch")
	if _, err := b.plane.Publish(t.Context(), topic, []byte(payload)); err != nil {
		t.Fatalf("declare: %v", err)
	}
	seedRetained(rec, map[string]string{topic: payload})

	if cleared := b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{}); cleared != 0 {
		t.Fatalf("retracted %v — a config this PROCESS declared, outside this batch", retractedTopics(rec))
	}
}

// TestSweepWindowIsTheOnlyDiscoverySubscription reads the snapshot
// window off the transport, which is the one place it is observable: the
// library composes it internally and takes it down again.
//
// It replaced two narrower filters the hand-rolled reconcile installed
// (homeassistant/+/+/+/config for the refresh flag and
// homeassistant/+/<slug>/+/config per device, both hard-wired to QoS 0)
// with one window at MQTT_QOS. That is the single pinned value this step
// moves, and this is the assertion that says so out loud.
func TestSweepWindowIsTheOnlyDiscoverySubscription(t *testing.T) {
	shortWindow(t)
	b, dev, _, rec := pinBridge(t)

	rec.mu.Lock()
	rec.filters = nil
	rec.mu.Unlock()

	b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{})

	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	rec.mu.Unlock()

	var windows []filterQoS
	for _, f := range filters {
		if strings.HasPrefix(f.Filter, pinPrefix) {
			windows = append(windows, f)
		}
	}
	if len(windows) != 1 {
		t.Fatalf("the sweep installed %d subscriptions under %s, want exactly 1: %v",
			len(windows), pinPrefix, windows)
	}
	if want := pinPrefix + "/#"; windows[0].Filter != want {
		t.Errorf("snapshot filter = %q, want %q", windows[0].Filter, want)
	}
	if windows[0].QoS != int(pinQoS) {
		t.Errorf("snapshot qos = %d, want %d (MQTT_QOS, publisher.Config's)", windows[0].QoS, int(pinQoS))
	}
}

// TestRefreshDiscoveryOnceIsFleetWideAndOnlyBeforeAnythingIsPublished
// pins HASS_DISCOVERY_REFRESH's scope. It is the one pass whose scope IS
// the fleet, and it is safe precisely because it runs before any worker
// has published: nothing is claimed, so "owned and unclaimed" is "owned",
// which is what the flag means to clear.
func TestRefreshDiscoveryOnceIsFleetWideAndOnlyBeforeAnythingIsPublished(t *testing.T) {
	shortWindow(t)
	prevSettle := refreshSettleDelay
	refreshSettleDelay = time.Millisecond
	t.Cleanup(func() { refreshSettleDelay = prevSettle })

	b, _, _, rec := pinBridge(t)
	b.cfg.HASSDiscoveryRefresh = true

	// Two appliances of ours, so the fleet scope is measurable: a refresh
	// that only cleared the first device would pass a single-device
	// fixture and leave half an installed base standing.
	b.devices = append(b.devices, &Device{name: "Backofen", topics: newDeviceTopics(pinRoot, "Backofen")})

	// The two device documents are in the wanted set and the sweep cannot
	// find them: OwnsConfigTopic declines the device-document form, so they
	// are named explicitly from the configured device list. A refresh that
	// cleared only the per-entity leftovers would leave the one retained
	// topic that actually holds this fleet's entities — which is the config
	// the flag exists to make Home Assistant re-read.
	ours := []string{
		"homeassistant/sensor/geschirrspuler/anything/config",
		"homeassistant/sensor/backofen/anything/config",
		"homeassistant/device/geschirrspuler/config",
		"homeassistant/device/backofen/config",
	}
	seedRetained(rec, map[string]string{
		ours[0]: ourConfig("anything"),
		ours[1]: `{"unique_id":"homeconnect_backofen_anything","state_topic":"` + pinRoot + `/Backofen/X/state"}`,
		"homeassistant/sensor/geschirrspuler/sibling/config": siblingConfig("sibling"),
		"homeassistant/sensor/zigbee2mqtt_bridge/x/config":   `{"unique_id":"zigbee2mqtt_x"}`,
	})

	b.refreshDiscoveryOnce(t.Context())

	got := retractedTopics(rec)
	sort.Strings(ours)
	if !slices.Equal(got, ours) {
		t.Errorf("refresh cleared %v, want %v — every appliance of ours, its device document "+
			"included, and neither the sibling instance's nor the foreign integration's configs",
			got, ours)
	}
}

// TestRefreshDiscoveryOnceIsOffByDefault: the flag is a one-shot operator
// migration, and a sweep that ran unasked at every boot would be a
// fleet-wide delete nobody requested.
func TestRefreshDiscoveryOnceIsOffByDefault(t *testing.T) {
	shortWindow(t)
	b, _, _, rec := pinBridge(t)
	seedRetained(rec, map[string]string{
		"homeassistant/sensor/geschirrspuler/anything/config": ourConfig("anything"),
	})
	b.refreshDiscoveryOnce(context.Background())
	if got := retractedTopics(rec); len(got) != 0 {
		t.Errorf("HASS_DISCOVERY_REFRESH is unset and the refresh cleared %v", got)
	}
}
