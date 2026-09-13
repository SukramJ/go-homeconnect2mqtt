// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// The tombstone pins. What is asserted here is not "a tombstone is
// written" — that is one line — but the four properties that make writing
// one safe in front of the one publish of this release that cannot be
// undone:
//
//  1. a component the new document declares is NEVER tombstoned;
//  2. a component that is not provably this instance's never becomes prior
//     state, so a sibling instance's entities cannot be deleted;
//  3. every other failure direction produces FEWER tombstones, never
//     different ones;
//  4. the document that results still validates, still supersedes every
//     per-entity config it replaces, and still fits.

// bundlePair renders the same appliance twice: the full set and the
// curated one, from the SHIPPED catalogue on both sides.
//
// The enricher is not optional. `enabled_by_default` is an enrichment, so a
// Discovery without it curates against a different set than the daemon
// does, and every number below would be a number no operator ever pays.
func bundlePair(t *testing.T) (full, curated *discovery.Bundle) {
	t.Helper()
	f, c := pinBundles()
	return cloneBundle(f), cloneBundle(c)
}

// pinBundles renders the pair once per test binary. Rendering 687
// components is about a second under the race detector and six tests need
// the pair, which is enough to push this package past its own timeout when
// the whole suite runs in parallel.
var pinBundles = sync.OnceValues(func() (*discovery.Bundle, *discovery.Bundle) {
	build := func(cur bool) *discovery.Bundle {
		d := New(nil, goldenPrefix, goldenRoot, "en", cur, slog.New(slog.DiscardHandler))
		cat, err := pinCatalog()
		if err != nil {
			panic("mapping.Load: " + err.Error())
		}
		d.SetEnricher(cat)
		entries, err := pinEntries()
		if err != nil {
			panic("pincatalog: " + err.Error())
		}
		app := homeconnect.NewAppliance(nil, &profile.Description{Info: pincatalog.Info, Entries: entries}, nil)
		b, err := d.BundleFor(goldenDeviceEN, pincatalog.Info, app.Entities())
		if err != nil {
			panic("BundleFor: " + err.Error())
		}
		return b
	}
	return build(false), build(true)
})

// cloneBundle is a copy every caller may mutate: ApplyTombstones replaces
// entries in Components and writes Tombstones, and nothing anywhere mutates
// a Component in place, so replacing the two maps is the whole of it.
func cloneBundle(b *discovery.Bundle) *discovery.Bundle {
	out := *b
	out.Components = maps.Clone(b.Components)
	out.Tombstones = nil
	return &out
}

// TestTheCuratedFlipRemovesWhatItStopsPublishing is the finding, driven.
//
// Flipping HASS_DISCOVERY from `full` to `curated` used to leave every
// component the curated filter drops behind in Home Assistant — with its
// registry entry, with both availability sources still publishing `online`
// and still receiving live state, because `curated` is read only in this
// package and the state plane never sees it. The operator who set the
// option to reduce clutter saw no change at all.
//
// Now the previous document's component set is the prior state, and
// everything in it the curated render does not produce is written into the
// document as a removal. The count is DERIVED from the two documents rather
// than written down: on the pin catalogue it is 510 of 687.
func TestTheCuratedFlipRemovesWhatItStopsPublishing(t *testing.T) {
	full, curated := bundlePair(t)
	prior := LiveComponents(full)

	gone := ApplyTombstones(curated, prior)
	want := len(full.Components) - len(prior) + len(prior) - len(LiveComponents(curated))
	if len(gone) != want || len(gone) == 0 {
		t.Fatalf("tombstoned %d components, want %d — the fixture no longer exercises the option",
			len(gone), want)
	}
	t.Logf("the curated flip removes %d of %d components", len(gone), len(prior))

	for _, key := range gone {
		entry, present := curated.Components[key]
		if !present {
			t.Fatalf("%s was counted as removed and is not in the document; an OMITTED key "+
				"removes nothing at all", key)
		}
		body, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		// Home Assistant removes a component when its entry carries a
		// platform and NOTHING else. An empty object is ignored, and a
		// `unique_id` un-removes the entity the entry exists to remove.
		if want := `{"platform":"` + string(prior[key].Platform) + `"}`; string(body) != want {
			t.Errorf("tombstone %s = %s, want %s", key, body, want)
		}
		if entry.UniqueID != "" {
			t.Errorf("tombstone %s carries a unique_id, which un-removes the entity", key)
		}
		// And the identity, outside the payload, because the legacy
		// retraction still needs it.
		if got := curated.Tombstones[key].UniqueID; got != prior[key].UniqueID {
			t.Errorf("Tombstones[%q].UniqueID = %q, want %q", key, got, prior[key].UniqueID)
		}
	}

	// The retained per-entity config of a removed entity is retracted too,
	// which is the other half of the removal: left standing, it re-creates
	// the entity on every MQTT-integration restart.
	superseded := publisher.SupersededTopics(goldenPrefix, curated)
	if len(superseded) != len(curated.Components) {
		t.Errorf("%d entries supersede %d per-entity topics; every one of them has to be named",
			len(curated.Components), len(superseded))
	}
}

// TestATombstoneNeverOverwritesALiveComponent is the assertion the whole
// design has to earn, because getting it wrong deletes a working entity —
// strictly worse than the phantom this feature exists to remove.
//
// Two directions, and the second is the hostile one: a prior document that
// declares a key this render DOES produce must leave that component exactly
// as rendered, whatever the prior entry said about it.
func TestATombstoneNeverOverwritesALiveComponent(t *testing.T) {
	full, _ := bundlePair(t)
	before := map[string][]byte{}
	for _, key := range full.Keys() {
		body, err := json.Marshal(full.Components[key])
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		before[key] = body
	}

	// Direction one: the previous document is this one. Nothing is gone,
	// so nothing may be marked.
	if gone := ApplyTombstones(full, LiveComponents(full)); len(gone) != 0 {
		t.Errorf("a document identical to its predecessor tombstoned %d components: %v",
			len(gone), gone[:min(5, len(gone))])
	}

	// Direction two: a prior entry for a key that is still live, carrying a
	// different platform and a different identity. It is still live, so it
	// is still rendered.
	hostile := map[string]discovery.Component{}
	for key, comp := range LiveComponents(full) {
		comp.Platform = "binary_sensor"
		comp.UniqueID = "homeconnect_somebody_else_" + key
		hostile[key] = comp
	}
	if gone := ApplyTombstones(full, hostile); len(gone) != 0 {
		t.Errorf("a prior document that disagrees about a LIVE component tombstoned %d of them",
			len(gone))
	}
	for _, key := range full.Keys() {
		body, err := json.Marshal(full.Components[key])
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if !bytes.Equal(body, before[key]) {
			t.Fatalf("%s was rewritten by the tombstone pass:\n got %s\nwant %s",
				key, body, before[key])
		}
	}
}

// TestEveryFailureDirectionOfTheReadBackProducesFewerTombstones is the
// safety argument as a table.
//
// The claim that makes a broker read-back acceptable in front of the one
// irreversible publish is that its failure mode is the behaviour of NOT
// having it. Each row is a way the read can go wrong, and every one of them
// has to produce no prior state at all — which is nil, not an empty map,
// because a caller must not be able to tell "read nothing" from "read
// something that proved nothing".
func TestEveryFailureDirectionOfTheReadBackProducesFewerTombstones(t *testing.T) {
	d := New(nil, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))
	// Both carry the two-source availability list every rendered payload
	// has carried since F1, because that list is where attribution now
	// lives: `<root>/status` is an exact string, and a fixture without it
	// is not a smaller real payload but one this daemon never publishes.
	ours := `{"platform":"sensor","unique_id":"homeconnect_geschirrspuler_a",` +
		`"availability":[{"topic":"` + goldenRoot + `/status"},{"topic":"` + goldenRoot +
		`/Geschirrspüler/availability"}],` +
		`"state_topic":"` + goldenRoot + `/Geschirrspüler/A/state"}`
	sibling := `{"platform":"sensor","unique_id":"homeconnect_geschirrspuler_a",` +
		`"availability":[{"topic":"other_root/status"},{"topic":"other_root/Geschirrspüler/availability"}],` +
		`"state_topic":"other_root/Geschirrspüler/A/state"}`
	// A NESTED sibling: rooted one topic level under us, which is the
	// configuration the prefix rule could not tell from our own. Every
	// topic it names begins with `homeconnect/`.
	nested := `{"platform":"sensor","unique_id":"homeconnect_geschirrspuler_a",` +
		`"availability":[{"topic":"` + goldenRoot + `/kitchen/status"},{"topic":"` + goldenRoot +
		`/kitchen/Geschirrspüler/availability"}],` +
		`"state_topic":"` + goldenRoot + `/kitchen/Geschirrspüler/A/state"}`

	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"a window that delivered nothing", ``},
		{"a document that is not JSON at all", `not a document`},
		{"a truncated document", `{"components":{"a":{"platform":`},
		{"a document with no components key", `{"device":{"identifiers":["homeconnect_x"]}}`},
		{"a document with an empty component map", `{"components":{}}`},
		{"a component that is not an object", `{"components":{"a":7}}`},
		// Ours by identity AND by every topic it names, so the ONLY thing
		// that can refuse it is the missing platform. Written that way on
		// purpose: the first version of this row named no topic either,
		// which meant the ownership rule refused it and the platform check
		// could be deleted with the suite green.
		{"a component of ours with no platform", `{"components":{"a":{"unique_id":"homeconnect_x_a","availability":[{"topic":"` + goldenRoot + `/status"}],"state_topic":"` + goldenRoot + `/x/state"}}}`},
		{"a tombstone this daemon wrote itself", `{"components":{"a":{"platform":"sensor"}}}`},
		{"a component with a foreign unique_id", `{"components":{"a":{"platform":"sensor","unique_id":"zigbee_a","availability":[{"topic":"` + goldenRoot + `/status"}],"state_topic":"` + goldenRoot + `/x/state"}}}`},
		{"a component naming no topic at all", `{"components":{"a":{"platform":"sensor","unique_id":"homeconnect_x_a"}}}`},
		{"a SIBLING INSTANCE's document", `{"components":{"a":` + sibling + `}}`},
		// The one the topic-prefix rule accepted: every topic is under
		// `homeconnect/`, the unique_id is one we would produce, and the
		// components are somebody else's LIVE entities.
		{"a NESTED sibling instance's document", `{"components":{"a":` + nested + `}}`},
		// Our anchor, somebody else's state topic: the component is
		// refused by the "every topic under our root" half of the rule
		// alone, which nothing else here exercises.
		{"a component carrying our anchor and a foreign state topic", `{"components":{"a":{"platform":"sensor","unique_id":"homeconnect_geschirrspuler_a","availability":[{"topic":"` + goldenRoot + `/status"}],"state_topic":"other_root/Geschirrspüler/A/state"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.BundleComponents([]byte(tc.payload)); got != nil {
				t.Errorf("prior state = %v, want none — this read cannot be trusted, so it "+
					"must produce fewer tombstones, never different ones", got)
			}
		})
	}

	// The converse, so the rule above is not passing by refusing
	// everything: a document of ours is read, and a MIXED one keeps only
	// the components that are provably ours.
	if got := d.BundleComponents([]byte(`{"components":{"a":` + ours + `}}`)); len(got) != 1 {
		t.Fatalf("a document of ours read as %v — the rule refuses everything", got)
	}
	mixed := d.BundleComponents([]byte(`{"components":{"a":` + ours + `,"b":` + sibling + `}}`))
	if len(mixed) != 1 {
		t.Fatalf("a mixed document read %d components, want 1", len(mixed))
	}
	if _, taken := mixed["b"]; taken {
		t.Error("a sibling instance's component became prior state, so this instance would " +
			"tombstone an entity it never published")
	}
}

// TestATombstoneIsNotCarriedForwardAsPriorState closes the loop that would
// otherwise make a removal permanent noise.
//
// The document that carries tombstones is itself the next connection's
// previous document. If its platform-only entries came back as prior state
// they would be re-marked in every document this daemon ever publishes
// again, and the curated document would carry 510 dead keys forever.
func TestATombstoneIsNotCarriedForwardAsPriorState(t *testing.T) {
	full, curated := bundlePair(t)
	gone := ApplyTombstones(curated, LiveComponents(full))
	if len(gone) == 0 {
		t.Fatal("nothing was removed, so this test proves nothing")
	}
	payload, err := json.Marshal(curated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	d := New(nil, goldenPrefix, goldenRoot, "en", true, slog.New(slog.DiscardHandler))
	readBack := d.BundleComponents(payload)
	live := LiveComponents(curated)
	if len(readBack) != len(live) {
		t.Errorf("reading the tombstoned document back yields %d components, want the %d LIVE ones",
			len(readBack), len(live))
	}
	for _, key := range gone {
		if _, present := readBack[key]; present {
			t.Fatalf("the tombstone %s came back as prior state; it would be re-marked forever", key)
		}
	}

	// And the second pass over the same connection, driven: prior is now
	// what went out, so nothing is marked again.
	_, curatedAgain := bundlePair(t)
	if again := ApplyTombstones(curatedAgain, readBack); len(again) != 0 {
		t.Errorf("a second pass re-marked %d components", len(again))
	}
}

// TestTheTombstonedDocumentStillValidatesAndIsMeasured is the artefact
// check, because tombstones change the bytes Home Assistant reads and the
// packet the preflight measures.
//
// discovery.Validate must be CLEAN rather than merely non-blocking: Home
// Assistant drops a document it cannot validate in its entirety, so a
// finding here costs the appliance every entity it has.
func TestTheTombstonedDocumentStillValidatesAndIsMeasured(t *testing.T) {
	full, curated := bundlePair(t)
	plain, err := json.Marshal(curated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gone := ApplyTombstones(curated, LiveComponents(full))
	marked, err := json.Marshal(curated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := discovery.Validate(curated); err != nil {
		t.Fatalf("a document carrying %d tombstones does not validate: %v", len(gone), err)
	}
	fullBody, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("documents: full %d bytes (%d components); curated %d bytes (%d); "+
		"curated + %d tombstones %d bytes",
		len(fullBody), len(full.Components), len(plain), len(LiveComponents(curated)),
		len(gone), len(marked))
	if len(marked) >= len(fullBody) {
		t.Errorf("the tombstoned curated document (%d bytes) is not smaller than the full one "+
			"(%d) — a removal entry must cost less than the component it removes",
			len(marked), len(fullBody))
	}
}

// TestADocumentOfNothingButTombstonesIsWithheld is the gate tombstones
// moved.
//
// The empty-document refusal used to read `len(b.Components) == 0`, and a
// tombstone IS an entry. An appliance that classified to zero entities
// against a prior document of 687 therefore renders a document of 687
// entries that declares nothing at all — a fleet-wide deletion that walks
// straight through a gate whose whole purpose is to refuse it.
func TestADocumentOfNothingButTombstonesIsWithheld(t *testing.T) {
	full, _ := bundlePair(t)
	rec := &bundleRecorder{}
	d := New(rec, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))

	empty := &discovery.Bundle{
		NodeID: full.NodeID, Device: full.Device, Origin: full.Origin,
		Components: map[string]discovery.Component{},
	}
	gone := ApplyTombstones(empty, LiveComponents(full))
	if len(gone) != len(full.Components) {
		t.Fatalf("the fixture marked %d of %d components", len(gone), len(full.Components))
	}
	topic, err := d.publishBundle(t.Context(), goldenDeviceEN, empty)
	if err == nil {
		t.Fatal("a document that declares nothing and deletes everything was published")
	}
	if !strings.Contains(err.Error(), "no components") {
		t.Errorf("err = %v, want the empty-document refusal", err)
	}
	if want := d.BundleTopic(goldenDeviceEN); topic != want {
		t.Errorf("topic = %q, want %q", topic, want)
	}
	if rec.written() != 0 {
		t.Errorf("a withheld document still wrote %d messages", rec.written())
	}
}

// TestThePublishedDocumentReportsWhatItRemoved drives PublishDeviceBundle
// itself, so the wiring between the render, the tombstones and the two
// returns is asserted rather than assumed — including the second return,
// which is what a caller remembers as the next pass's prior state.
func TestThePublishedDocumentReportsWhatItRemoved(t *testing.T) {
	full, _ := bundlePair(t)
	rec := &bundleRecorder{}
	d := New(rec, goldenPrefix, goldenRoot, "en", true, slog.New(slog.DiscardHandler))
	d.SetEnricher(pinEnricher(t))

	topic, live, err := d.PublishDeviceBundle(t.Context(), goldenDeviceEN, pincatalog.Info,
		pinEntities(t), LiveComponents(full))
	if err != nil {
		t.Fatalf("PublishDeviceBundle: %v", err)
	}
	if want := d.BundleTopic(goldenDeviceEN); topic != want {
		t.Errorf("topic = %q, want %q", topic, want)
	}
	rec.mu.Lock()
	published := slices.Clone(rec.bundles)
	rec.mu.Unlock()
	if len(published) != 1 {
		t.Fatalf("published %d documents, want 1", len(published))
	}
	got := published[0]
	if len(live) != len(LiveComponents(got)) || len(live) == 0 {
		t.Fatalf("the returned live set is %d components, the document declares %d",
			len(live), len(LiveComponents(got)))
	}
	if len(got.Components) <= len(live) {
		t.Fatalf("the document carries %d entries for %d live components — nothing was removed",
			len(got.Components), len(live))
	}
	for key := range live {
		if got.Components[key].UniqueID == "" {
			t.Fatalf("the returned live set names %s, which the document tombstoned", key)
		}
	}

	// A refusal returns no live set at all: a document that was not
	// written did not become anybody's previous document.
	rec.err = errors.New("the broker refused the packet")
	_, live, err = d.PublishDeviceBundle(t.Context(), goldenDeviceEN, pincatalog.Info,
		pinEntities(t), LiveComponents(full))
	if err == nil {
		t.Fatal("the stub accepted a document it was told to refuse")
	}
	if live != nil {
		t.Errorf("a refused document returned %d components as prior state", len(live))
	}
}

// TestTheBundleNodeIDIsTheOneTheTopicCarries keeps the three spellings of
// the document's identity from drifting: the topic the daemon publishes,
// the node id the read-back looks itself up by, and the filter the window
// subscribes.
//
// "Geschirrspüler" is addressed as "geschirrspuler", which is neither the
// raw name nor merely its lower case — go-mtec2mqtt documented that topic
// in five places and got it wrong in three.
func TestTheBundleNodeIDIsTheOneTheTopicCarries(t *testing.T) {
	d := New(nil, goldenPrefix, goldenRoot, "de", false, slog.New(slog.DiscardHandler))
	const device = "Geschirrspüler"
	topic := d.BundleTopic(device)
	node, ok := d.BundleNodeIDOf(topic)
	if !ok {
		t.Fatalf("%s does not parse as a device document", topic)
	}
	if node != d.BundleNodeID(device) {
		t.Errorf("the topic carries node id %q, BundleNodeID says %q", node, d.BundleNodeID(device))
	}
	if strings.EqualFold(node, device) {
		t.Errorf("node id %q is the raw appliance name or its lower case; it is the SLUG", node)
	}
	if !publisher.MatchFilter(d.BundleFilter(), topic) {
		t.Errorf("the read-back filter %q does not match %q, so the window would see nothing",
			d.BundleFilter(), topic)
	}
	for _, other := range []string{
		goldenPrefix + "/sensor/geschirrspuler/x/config",
		goldenPrefix + "/device/geschirrspuler/config/x",
		"other_prefix/device/geschirrspuler/config",
	} {
		if _, ok := d.BundleNodeIDOf(other); ok {
			t.Errorf("%s was read as a device document of ours", other)
		}
	}
}

// TestTheCuratedWarningDescribesTheBehaviourThisReleaseHas: #48 shipped this warning against a daemon that could not remove what it
// omitted, so its text told the operator to delete the entities by hand
// after restarting Home Assistant. That remedy is now wrong, and a warning
// about a hazard that no longer exists is worse than none: it sends an
// operator to do something that will not help and implies the deletion did
// not happen.
//
// The line is kept — the number is more consequential than it was, not
// less, because flipping the option now DELETES that many entities — and
// what is pinned here is that the text describes the behaviour the release
// actually has.
func TestTheCuratedWarningDescribesTheBehaviourThisReleaseHas(t *testing.T) {
	var buf bytes.Buffer
	d := New(nil, goldenPrefix, goldenRoot, "en", true,
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	d.SetEnricher(pinEnricher(t))
	if _, err := d.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t)); err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, "hass.curated_components_omitted") {
		t.Fatalf("the curated render said nothing: %q", line)
	}
	for _, stale := range []string{
		"Remove them by hand",
		"keeps receiving state",
		"restart Home Assistant",
		"delete them on the device page",
	} {
		if strings.Contains(line, stale) {
			t.Errorf("the warning still says %q — that was the remedy for a hazard this "+
				"release removed, and following it now does nothing", stale)
		}
	}
	for _, want := range []string{"DELETES", "history"} {
		if !strings.Contains(line, want) {
			t.Errorf("the warning does not say %q; flipping this option now deletes entities "+
				"and their history, which is what an operator has to be told", want)
		}
	}
}

// TestTheValidatorIsShownTheDocumentTheRemovalsProduced is the other half
// of "both gates run on the published shape".
//
// The size preflight's half is pinned by
// TestThePreflightMeasuresTheDocumentTheRemovalsProduced. The validator's
// was not: TestTheTombstonedDocumentStillValidatesAndIsMeasured asserts
// that OUR tombstoned document validates, which stays true if
// discovery.Validate is handed a tombstone-free copy — the mutation that
// matters survived the whole suite. What it costs is the thing the gate
// exists for: a document whose REMOVAL entries make it blocking is
// published anyway, and Home Assistant drops the document entire, so the
// appliance loses all 687 entities rather than the one bad removal.
//
// The removal is where a bad platform can come from, and it is not
// hypothetical: the tombstone's platform is the PRIOR document's, read
// back from the broker, so it was written by some other version of this
// daemon (or by nothing at all) and no render of this one vouches for it.
//
// The control comes first. The identical publish with no prior state must
// succeed, so the refusal below can only be the tombstone — a test whose
// failure could come from the rendered components would prove nothing
// about which document the validator saw.
func TestTheValidatorIsShownTheDocumentTheRemovalsProduced(t *testing.T) {
	entities := pinEntities(t)
	control := &bundleRecorder{}
	d := New(control, goldenPrefix, goldenRoot, "en", true, slog.New(slog.DiscardHandler))
	d.SetEnricher(pinEnricher(t))
	if _, _, err := d.PublishDeviceBundle(t.Context(), goldenDeviceEN, pincatalog.Info, entities, nil); err != nil {
		t.Fatalf("the control publish was refused, so nothing below is attributable: %v", err)
	}
	if control.written() == 0 {
		t.Fatal("the control publish wrote nothing")
	}

	// A key no render of this daemon produces, so it becomes a tombstone,
	// carrying a platform Home Assistant has no MQTT support for.
	prior := map[string]discovery.Component{
		"a_component_no_render_of_ours_declares": {
			Platform: hacatalog.Platform("toaster"),
			UniqueID: "homeconnect_dishwasher_gone",
		},
	}
	rec := &bundleRecorder{}
	d2 := New(rec, goldenPrefix, goldenRoot, "en", true, slog.New(slog.DiscardHandler))
	d2.SetEnricher(pinEnricher(t))
	_, live, err := d2.PublishDeviceBundle(t.Context(), goldenDeviceEN, pincatalog.Info, entities, prior)
	if err == nil {
		t.Fatal("a document whose REMOVAL entry is blocking was published: Home Assistant drops " +
			"the whole document, so the appliance loses every entity it has")
	}
	var ve *discovery.ValidationError
	if !errors.As(err, &ve) || !ve.Blocking() {
		t.Fatalf("err = %v, want a blocking discovery.ValidationError", err)
	}
	if live != nil {
		t.Errorf("a refused document reported %d live components as the next prior state", len(live))
	}
	if rec.written() != 0 {
		t.Errorf("a refused document still wrote %d messages", rec.written())
	}
}
