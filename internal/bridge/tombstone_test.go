// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
)

// The tombstone read-back at the bridge level: the part internal/hass
// cannot see, which is WHEN the previous document is read, on which
// connection the answer is remembered, and what happens to it when the
// connection goes away.
//
// Every test here runs over the TWO-ENTITY appliance rather than the 687
// one, and that is the review lesson of #48 rather than a convenience: a
// pass over the pin catalogue spends about a second rendering before it
// writes anything, so a publish that should have happened before the
// read-back lands after it by accident and the defect hides behind its own
// cost. One mutation survived 8 of 8 runs that way.

// priorFixture is birthRaceBridge with a retained device document already
// on the broker: the two components this appliance still has, plus one it
// no longer publishes.
//
// The two live entries are taken from the REAL render rather than written
// here, so "the previous document declared what we still declare" is true
// by construction and the only difference between the documents is the one
// this test is about.
func priorFixture(t *testing.T, extra map[string]string) (*Bridge, *Device, *subRecorder, *discovery.Bundle) {
	t.Helper()
	shortWindow(t)
	b, dev, rec := birthRaceBridge(t)
	return priorFixtureOn(t, b, dev, rec, extra)
}

// siblingComponent is a component of a SECOND instance of this daemon, as
// it appears inside a device document: every identity string is one this
// instance would produce — MQTT_TOPIC appears in none of them (F8) — and a
// PLATFORM, which is what makes it a candidate for removal at all.
//
// The platform is the part this fixture had to gain. Without it the
// components were declined for carrying no platform rather than for being
// somebody else's, and both sibling tests passed with the ownership rule
// deleted: M2 was caught only by the predicate table, which is the shape of
// masking this programme keeps finding.
func siblingComponent(key string) string {
	return `{"platform":"sensor","unique_id":"homeconnect_geschirrspuler_` + key +
		`","state_topic":"other_root/` + pinDevice + `/X/state",` +
		`"availability":[{"topic":"other_root/status"},{"topic":"other_root/` + pinDevice +
		`/availability"}]}`
}

// goneComponent is a component of OURS that the appliance no longer has:
// a unique_id in this daemon's namespace and topics under this instance's
// root, which is the whole of the ownership rule.
func goneComponent(key string) string {
	return `{"platform":"sensor","unique_id":"homeconnect_geschirrspuler_` + key +
		`","state_topic":"` + pinRoot + `/` + pinDevice + `/X/state",` +
		`"availability":[{"topic":"` + pinRoot + `/status"},{"topic":"` + pinRoot + `/` +
		pinDevice + `/availability"}]}`
}

// publishedDocument is the last device document the transport was handed.
func publishedDocument(t *testing.T, b *Bridge, dev *Device, rec *subRecorder) map[string]json.RawMessage {
	t.Helper()
	payload, ok := rec.lastPayload(bundleTopicFor(b, dev.name))
	if !ok {
		t.Fatal("no device document was published")
	}
	var doc struct {
		Components map[string]json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("the published document does not parse: %v", err)
	}
	return doc.Components
}

// TestTheReadBackRunsBeforeTheMigrationAndRemovesWhatIsGone is the feature,
// driven end to end through the production path.
//
// Two things are asserted and neither implies the other. The document
// carries a removal for the component the appliance no longer has — which
// is what makes Home Assistant delete the entity — and the read that
// produced it happened BEFORE the first byte of the migration, which is the
// only moment it could have: the publish overwrites the very document the
// read asks about, and the retraction that precedes it is already
// destructive.
//
// The ordering verdict is read AT PUBLISH TIME rather than off the call
// list afterwards. A list read once the window has opened cannot say
// whether the window opened first, and both outcomes of that race look like
// a pass — #48's F2, the same shape.
func TestTheReadBackRunsBeforeTheMigrationAndRemovesWhatIsGone(t *testing.T) {
	const retired = "retired_feature"
	b, dev, rec, live := priorFixture(t, map[string]string{retired: goneComponent(retired)})
	defer drainReconciles(t, b)

	var windowFirst, decided bool
	rec.onPublish = func(string) {
		if decided {
			return
		}
		decided = true
		windowFirst = len(rec.bundleWindows(pinPrefix)) > 0
	}

	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)

	if !decided {
		t.Fatal("the migration wrote nothing at all")
	}
	if !windowFirst {
		t.Error("the migration's first write happened before the read-back window opened — " +
			"the previous document it asks about had already been overwritten")
	}

	comps := publishedDocument(t, b, dev, rec)
	entry, present := comps[retired]
	if !present {
		t.Fatalf("the published document has no entry for %s; an OMITTED key removes nothing "+
			"at all, which is the finding this closes", retired)
	}
	if got := string(entry); got != `{"platform":"sensor"}` {
		t.Errorf("the removal entry is %s, want {\"platform\":\"sensor\"} — Home Assistant "+
			"deletes a component whose entry carries a platform and nothing else", got)
	}
	for key := range live.Components {
		if string(comps[key]) == `{"platform":"sensor"}` {
			t.Errorf("%s is still published and was removed anyway", key)
		}
	}

	// And the other half of a removal: the stale per-entity config is
	// retracted, or it re-creates the entity on the next MQTT-integration
	// restart.
	legacy := publisher.EntityConfigTopic(pinPrefix, "sensor", b.hass.BundleNodeID(dev.name), retired)
	if !slices.Contains(retractedTopics(rec), legacy) {
		t.Errorf("%s was not retracted", legacy)
	}
}

// TestTheReadBackIgnoresASiblingsDocument is the one failure direction that
// would be destructive rather than merely absent.
//
// Two instances of this daemon with different MQTT_TOPIC roots, the same
// HASS_BASE_TOPIC and an appliance name in common address the SAME document
// topic: MQTT_TOPIC appears in neither the discovery prefix nor the node id
// (F8). So the retained document this read finds may not be ours at all,
// and tombstoning from it would delete entities of a live instance that has
// no reason to republish them.
//
// Nothing in the topic can tell the two apart. The component payloads can,
// and that is the rule that is driven here.
func TestTheReadBackIgnoresASiblingsDocument(t *testing.T) {
	const theirs = "a_sibling_feature"
	b, dev, rec, _ := priorFixture(t, map[string]string{theirs: siblingComponent(theirs)})
	defer drainReconciles(t, b)

	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)

	comps := publishedDocument(t, b, dev, rec)
	if entry, present := comps[theirs]; present {
		t.Errorf("a sibling instance's component was removed by this one: %s", entry)
	}
	legacy := publisher.EntityConfigTopic(pinPrefix, "sensor", b.hass.BundleNodeID(dev.name), theirs)
	if slices.Contains(retractedTopics(rec), legacy) {
		t.Errorf("%s — a sibling's retained config — was retracted", legacy)
	}
}

// TestTheReadBackIsRepeatedOnEveryConnectionAndNotWithinOne pins where the
// memo lives, which is the question a read-back in front of an irreversible
// publish has to answer.
//
// Within one connection it must not repeat: the document that went out is
// the previous document now, so a second pass re-reading the broker would
// find its own tombstones and a second pass trusting a stale memo would
// re-mark what the first one removed. Across a reconnect it MUST repeat:
// everything a runtime remembers is a statement about a broker, and a
// broker that came back without its retained store holds none of it. That
// is the same property haplane.Plane.Reconnect exists to give
// publisher.Runtime, and a read-back cached across connections would undo
// it on the artefact beside it.
func TestTheReadBackIsRepeatedOnEveryConnectionAndNotWithinOne(t *testing.T) {
	const retired = "retired_feature"
	b, dev, rec, _ := priorFixture(t, map[string]string{retired: goneComponent(retired)})
	defer drainReconciles(t, b)

	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)
	if got := len(rec.bundleWindows(pinPrefix)); got != 1 {
		t.Fatalf("the first pass opened %d read-back windows, want 1", got)
	}
	if _, present := publishedDocument(t, b, dev, rec)[retired]; !present {
		t.Fatal("the first pass removed nothing, so this test proves nothing")
	}

	// Second pass, same connection: one window, and the removal is not
	// repeated.
	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)
	if got := len(rec.bundleWindows(pinPrefix)); got != 1 {
		t.Errorf("a second pass on the same connection opened %d read-back windows, want 1", got)
	}
	if entry, present := publishedDocument(t, b, dev, rec)[retired]; present {
		t.Errorf("the removal was repeated on the same connection: %s", entry)
	}

	// A reconnect, and the question is asked again.
	b.plane.Reconnect()
	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)
	if got := len(rec.bundleWindows(pinPrefix)); got != 2 {
		t.Errorf("after a reconnect the read-back ran %d times, want 2 — a statement about "+
			"what the BROKER holds may not outlive the connection it was made on", got)
	}
}

// TestAFailedDocumentDoesNotBecomeThePreviousDocument: the memo is what the next pass subtracts against, so writing it for a
// document the broker refused would make that pass believe components are
// still declared which are not on the broker at all — and it would skip the
// removal the retry exists to perform.
func TestAFailedDocumentDoesNotBecomeThePreviousDocument(t *testing.T) {
	const retired = "retired_feature"
	b, dev, rec, _ := priorFixture(t, map[string]string{retired: goneComponent(retired)})
	defer drainReconciles(t, b)
	doc := bundleTopicFor(b, dev.name)
	rec.setFail(doc)

	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)
	if _, ok := rec.lastPayload(doc); ok {
		t.Fatal("the stub accepted a document it was told to refuse")
	}

	rec.setFail("")
	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)
	if _, present := publishedDocument(t, b, dev, rec)[retired]; !present {
		t.Errorf("the retry published a document with no removal for %s — the refused document "+
			"was remembered as though it had been written", retired)
	}
}

// TestAPassThatStraddlesAReconnectDoesNotWriteTheNewConnectionsMemo — a
// discovery pass can outlive the connection it started on: it is a
// retraction of every superseded config and then a large document, and a
// link can drop anywhere inside that. Its result describes a broker state
// the NEW connection never had, so recording it would suppress the removal
// the new connection's own pass has to perform.
func TestAPassThatStraddlesAReconnectDoesNotWriteTheNewConnectionsMemo(t *testing.T) {
	b, dev, _, _ := priorFixture(t, nil)
	defer drainReconciles(t, b)
	node := b.hass.BundleNodeID(dev.name)

	stale := b.plane.Runtime()
	b.plane.Reconnect()
	fresh := b.plane.Runtime()

	// The new connection asks first and gets its answer. The ORDER is the
	// whole test: a straddling pass that records before this point is
	// harmlessly overwritten by the read, and a test written that way
	// passes with the guard deleted — it did, 0 of 5 runs caught.
	current := map[string]map[string]discovery.Component{
		node: {"still_here": {Platform: "sensor", UniqueID: "homeconnect_still_here"}},
	}
	got := b.prior.forConnection(t.Context(), fresh, node,
		func(_ context.Context) map[string]map[string]discovery.Component { return current })
	if len(got) != 1 {
		t.Fatalf("the new connection read %d components, want 1", len(got))
	}

	// Now the pass that started on the connection that died comes back.
	// Its result describes a broker state this connection never had.
	b.prior.record(stale, node, map[string]discovery.Component{
		"from_the_dead_connection": {Platform: "sensor", UniqueID: "homeconnect_dead"},
	})

	got = b.prior.forConnection(t.Context(), fresh, node,
		func(_ context.Context) map[string]map[string]discovery.Component {
			t.Error("the memo was re-read; the dead connection's write invalidated it")
			return nil
		})
	if _, present := got["from_the_dead_connection"]; present {
		t.Error("a pass from the dead connection wrote into the new connection's memo — " +
			"the removals this connection has to perform would be suppressed by a broker " +
			"state that no longer exists")
	}
	if _, present := got["still_here"]; !present {
		t.Error("the new connection's own answer was lost")
	}
}

// TestASiblingsWholeDocumentIsDeclined is the failure direction that a
// review of go-daikin2mqtt's read-back proved is NOT merely absent.
//
// There, two instances sharing a device node id read each other's retained
// documents, did not recognise them as foreign, and tombstoned each other's
// LIVE components — removed from Home Assistant's registry, taking the
// dashboards, automations, areas and renames with them, and then restored
// and removed again in a permanent ping-pong. That is strictly worse than
// the phantom this feature exists to delete, and it is the bar this
// bridge's read-back has to clear before it may ship.
//
// This bridge meets the same precondition. Two daemons with different
// MQTT_TOPIC roots, the same HASS_BASE_TOPIC and an appliance name in
// common publish byte-identical node ids, `unique_id`s, `identifiers` and
// `default_entity_id`s — MQTT_TOPIC appears in none of them (F8) — so they
// address the SAME document topic and nothing about the topic can tell them
// apart.
//
// What it has that daikin did not is MQTT_TOPIC inside every component:
// `state_topic` and `command_topic` are `<root>/…`, and since F1 every
// component also carries the two-source availability list, both under
// `<root>`. So the document CAN be attributed, component by component, and
// the rule that does it is the same one the orphan sweep uses.
//
// Driven, not asserted against the predicate: both of this programme's
// worst defects were found by driving the pass and missed by asking the
// predicate.
func TestASiblingsWholeDocumentIsDeclined(t *testing.T) {
	shortWindow(t)
	b, dev, rec := birthRaceBridge(t)
	defer drainReconciles(t, b)

	// Instance A's document, at the topic instance B is about to publish
	// to: its own appliance's components, none of which B has. Every
	// identity string is one B would produce; only the topics differ.
	theirs := map[string]string{}
	entries := make([]string, 0, 3)
	for _, key := range []string{"nacht_leise", "werktag", "a_third_one"} {
		theirs[key] = siblingComponent(key)
		entries = append(entries, `"`+key+`":`+theirs[key])
	}
	raw := `{"device":{"identifiers":["homeconnect_geschirrspuler"]},"components":{` +
		strings.Join(entries, ",") + `}}`
	seedRetained(rec, map[string]string{bundleTopicFor(b, dev.name): raw})

	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)

	published := publishedDocument(t, b, dev, rec)
	for key := range theirs {
		if entry, present := published[key]; present {
			t.Errorf("this instance removed %s, which is a LIVE component of another "+
				"instance: %s", key, entry)
		}
		legacy := publisher.EntityConfigTopic(pinPrefix, "sensor", b.hass.BundleNodeID(dev.name), key)
		if slices.Contains(retractedTopics(rec), legacy) {
			t.Errorf("%s — another instance's retained config — was retracted", legacy)
		}
	}
	// And the converse, so the decline is not passing by publishing
	// nothing: this instance's own components are still there.
	if len(published) == 0 {
		t.Fatal("nothing was published at all, so the decline above proves nothing")
	}
}

// TestThePreflightMeasuresTheDocumentTheRemovalsProduced — the size
// preflight exists because go-mqtt raises ErrPacketTooLarge from
// inside the PUBLISH, with every superseded per-entity config already
// retracted — so a document measured before it was finished is a gate on
// the wrong artefact, and the miss is exactly the fleet-wide deletion the
// gate was put there to prevent. Removals ADD entries, so the finished
// document is the larger one.
//
// The limit is chosen between the two sizes, which is the only interval in
// which the two orderings give different answers.
func TestThePreflightMeasuresTheDocumentTheRemovalsProduced(t *testing.T) {
	shortWindow(t)
	var limit uint32
	b, dev, rec := smallBridge(t, func() (uint32, bool) { return limit, true })
	defer drainReconciles(t, b)

	live, err := b.hass.BundleFor(dev.name, dev.app.Info(), dev.app.Entities())
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	body, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc := bundleTopicFor(b, dev.name)
	bare := haplane.PacketSize(doc, len(body))

	// A prior document holding what this appliance still has, plus enough
	// components it no longer has to push the finished document past a
	// limit the bare one clears.
	extra := map[string]string{}
	for i := range 40 {
		extra["gone_feature_"+strconv.Itoa(i)] = goneComponent("gone_feature_" + strconv.Itoa(i))
	}
	priorFixtureOn(t, b, dev, rec, extra)

	marked, err := b.hass.BundleFor(dev.name, dev.app.Info(), dev.app.Entities())
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	prior := map[string]discovery.Component{}
	for key, payload := range extra {
		var c discovery.Component
		if err := json.Unmarshal([]byte(payload), &c); err != nil {
			t.Fatalf("unmarshal %s: %v", key, err)
		}
		prior[key] = c
	}
	if gone := hass.ApplyTombstones(marked, prior); len(gone) != len(extra) {
		t.Fatalf("the fixture marked %d of %d components", len(gone), len(extra))
	}
	markedBody, err := json.Marshal(marked)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	full := haplane.PacketSize(doc, len(markedBody))
	if full <= bare {
		t.Fatalf("the finished document (%d) is not larger than the bare one (%d) — "+
			"this test cannot distinguish the two orderings", full, bare)
	}
	t.Logf("%d removals add %d bytes (%.1f per removal): %d -> %d on the wire",
		len(extra), full-bare, float64(full-bare)/float64(len(extra)), bare, full)

	// Between the two: the bare document fits and the finished one does not.
	limit = uint32(bare + (full-bare)/2) //nolint:gosec // both are small test sizes

	b.publishDiscovery(t.Context(), dev)
	drainReconciles(t, b)

	if _, ok := rec.lastPayload(doc); ok {
		t.Error("a document larger than the broker's limit was published — the preflight " +
			"measured the render, not the document")
	}
	if got := retractedTopics(rec); len(got) != 0 {
		t.Errorf("a refused migration retracted %d topics; the refusal exists to happen "+
			"BEFORE the retraction: %v", len(got), got)
	}
}

// priorFixtureOn seeds a retained device document on an already-built
// bridge: the components it still renders, plus the ones it does not.
func priorFixtureOn(
	t *testing.T, b *Bridge, dev *Device, rec *subRecorder, extra map[string]string,
) (*Bridge, *Device, *subRecorder, *discovery.Bundle) {
	t.Helper()
	live, err := b.hass.BundleFor(dev.name, dev.app.Info(), dev.app.Entities())
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	body, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	var comps map[string]json.RawMessage
	if err := json.Unmarshal(doc["components"], &comps); err != nil {
		t.Fatalf("components: %v", err)
	}
	for key, payload := range extra {
		comps[key] = json.RawMessage(payload)
	}
	if doc["components"], err = json.Marshal(comps); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if body, err = json.Marshal(doc); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	seedRetained(rec, map[string]string{bundleTopicFor(b, dev.name): string(body)})
	return b, dev, rec, live
}

// TestTheReadBackReadsEveryAppliancesDocument — the window is ONE
// fleet-wide subscription, so "I have what I came for" has to mean every
// configured appliance and not merely the first document the broker sends.
//
// The property does not exist on a one-appliance fixture: a read-back that
// stops at the first delivery is indistinguishable from a correct one
// there, and it survived every other pin in this file. The cost of getting
// it wrong is in the safe direction — the second appliance keeps its
// phantoms — which is exactly why nothing else notices.
func TestTheReadBackReadsEveryAppliancesDocument(t *testing.T) {
	prev := reconcileCollectWindow.Get()
	reconcileCollectWindow.Set(300 * time.Millisecond)
	t.Cleanup(func() { reconcileCollectWindow.Set(prev) })
	const second = "Waschmaschine"
	b, rec := smallBridgeWith(t, nil, pinDevice, second)
	defer drainReconciles(t, b)
	if len(b.devices) != 2 {
		t.Fatalf("the fixture has %d appliances", len(b.devices))
	}

	const retired = "retired_feature"
	for _, dev := range b.devices {
		priorFixtureOn(t, b, dev, rec, map[string]string{retired: goneComponent(retired)})
	}
	rec.mu.Lock()
	rec.staggerReplay = true
	rec.mu.Unlock()
	for _, dev := range b.devices {
		b.publishDiscovery(t.Context(), dev)
	}
	drainReconciles(t, b)

	if got := len(rec.bundleWindows(pinPrefix)); got != 1 {
		t.Errorf("two appliances opened %d read-back windows on one connection, want 1", got)
	}
	for _, dev := range b.devices {
		if _, present := publishedDocument(t, b, dev, rec)[retired]; !present {
			t.Errorf("%s's document carries no removal for %s — the window stopped before "+
				"its document arrived", dev.name, retired)
		}
	}
}
