// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
)

// hamqttPin renders one pinned configuration through go-hamqtt.
func hamqttPin(t *testing.T, tc goldenCase) []goldenRow {
	t.Helper()
	d := New(nil, goldenPrefix, goldenRoot, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
	if tc.enriched {
		d.SetEnricher(pinEnricher(t))
	}
	rows, err := d.hamqttComponents(tc.device, pincatalog.Info, pinEntities(t))
	if err != nil {
		t.Fatalf("hamqttComponents: %v", err)
	}
	out := make([]goldenRow, 0, len(rows))
	for i := range rows {
		var decoded map[string]any
		if err := json.Unmarshal(rows[i].Payload, &decoded); err != nil {
			t.Fatalf("%s: payload is not a JSON object: %v", rows[i].Topic, err)
		}
		out = append(out, goldenRow{Topic: rows[i].Topic, QoS: int(goldenQoS), Retain: true, Payload: decoded})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

// readGoldenRows reads a pin WITHOUT ever writing it. The update flag is not
// consulted here and must never be: this test's whole job is to compare the
// library's output against bytes produced before the library existed.
func readGoldenRows(t *testing.T, name string) []goldenRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var rows []goldenRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("golden %s is not valid json: %v", name, err)
	}
	return rows
}

// TestHamqttReproducesEveryPinnedPayloadByteForByte is ADR 0070 phase 7,
// step 4 — the decisive experiment, and the reason this PR exists.
//
// It renders all four pinned configurations through go-hamqtt v0.32.0 and
// compares the result against internal/hass/testdata/*.json: the bytes the
// hand-built path produced, pinned by #40 and moved deliberately by #41's
// eight defect fixes. The goldens are READ, never regenerated — the update
// flags are not reachable from this file — so a mismatch is a finding about
// the library or about this bridge, and never a file that quietly moved.
//
// If it passes, every later step of the phase is a switch-over with a test
// behind it. If it fails, the failure surfaces here, at zero risk, instead
// of at step 6 against a live Home Assistant where the only evidence is one
// WARNING line and no entities.
func TestHamqttReproducesEveryPinnedPayloadByteForByte(t *testing.T) {
	total := 0
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			want := readGoldenRows(t, tc.file)
			got := hamqttPin(t, tc)
			compareRows(t, want, got)
			if len(want) != len(got) {
				t.Errorf("%s: golden has %d rows, go-hamqtt rendered %d", tc.file, len(want), len(got))
			}
			t.Logf("%s: %d payloads reproduced byte for byte", tc.file, len(got))
		})
		total += len(readGoldenRows(t, tc.file))
	}
	t.Logf("byte-equality proved over %d pinned payloads across %d configurations", total, len(goldenCases))
}

// TestHamqttReproducesTheIdentityPlane is the same proof narrowed to the
// four strings Home Assistant keys its registries on and has no migration
// path for. It compares against identity_en.json rather than deriving the
// expectation from the payload file, so an identity regression cannot hide
// behind a payload that changed with it.
func TestHamqttReproducesTheIdentityPlane(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "identity_en.json")) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read identity_en.json: %v", err)
	}
	var want []goldenIdentity
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("identity_en.json: %v", err)
	}

	got := make([]goldenIdentity, 0, len(want))
	for _, r := range hamqttPin(t, goldenCase{lang: "en", device: goldenDeviceEN, enriched: true}) {
		uid, _ := r.Payload["unique_id"].(string)
		eid, _ := r.Payload["default_entity_id"].(string)
		dev, _ := r.Payload["device"].(map[string]any)
		ids, _ := dev["identifiers"].([]any)
		if len(ids) != 1 {
			t.Fatalf("%s: device.identifiers = %v, want exactly one", r.Topic, ids)
		}
		id, _ := ids[0].(string)
		got = append(got, goldenIdentity{ConfigTopic: r.Topic, UniqueID: uid, DefaultEntityID: eid, DeviceID: id})
	}
	if wb, gb := canonical(t, want), canonical(t, got); !bytes.Equal(wb, gb) {
		t.Error("go-hamqtt does not reproduce the identity plane — this would orphan " +
			"every entity in every installation; the diff is the finding, not the file")
	}
	t.Logf("identity reproduced for %d entities", len(got))
}

// TestHamqttLayoutAgreesWithTheDaemonsOwnBuilders compares the go-hamqtt
// Layout against internal/layout builder against builder, never through a
// file. It is the step-4 counterpart of TestStateTopicBuildersAgree (F3):
// a golden regeneration cannot make it pass, and it fails on a change to
// either side alone.
func TestHamqttLayoutAgreesWithTheDaemonsOwnBuilders(t *testing.T) {
	const device = goldenDeviceDE
	l := NewLayout(goldenRoot)
	dt := layout.NewDevice(goldenRoot, device)
	dev := hamqttDevice(device, pincatalog.Info)

	if got, want := l.Bridge(), layout.Bridge(goldenRoot); got != want {
		t.Errorf("Bridge() = %q, want %q", got, want)
	}
	n := 0
	for _, e := range pinEntities(t) {
		slot := hamqttSlot(dev, device, strings.Split(layout.FeaturePath(e.Name(), e.UID()), "/")...)
		if got, want := l.State(slot), dt.State(e.Name(), e.UID()); got != want {
			t.Errorf("State: %q, want %q", got, want)
		}
		if got, want := l.Command(slot), dt.Command(e.Name(), e.UID()); got != want {
			t.Errorf("Command: %q, want %q", got, want)
		}
		if got, want := l.Availability(slot), dt.Availability(); got != want {
			t.Errorf("Availability: %q, want %q", got, want)
		}
		n++
	}
	for _, key := range []string{layout.ControlStartProgram, layout.ControlStopProgram} {
		slot := hamqttSlot(dev, device, strings.Split(layout.ControlPath(key), "/")...)
		if got, want := l.Command(slot), dt.ControlCommand(key); got != want {
			t.Errorf("ControlCommand(%q): %q, want %q", key, got, want)
		}
	}
	if n == 0 {
		t.Fatal("no entities")
	}
	t.Logf("layout agrees with internal/layout over %d entities plus both synthetic controls", n)
}

// TestHamqttLayoutIgnoresAddressChannelAndBucket makes the layout's three
// inert Slot fields explicit.
//
// A field a Layout never reads is a blind spot no golden can see: mutate it
// and nothing fails, so nothing tells a later reader whether it was
// deliberately ignored or silently dropped. go-mtec2mqtt's equivalent step
// found three such fields and gave each an assertion rather than leaving the
// gap; this is the same. Address in particular is NOT the device's topic
// segment here — it is the slug-derived identity — and a layout that started
// reading it would move every state topic of a non-ASCII device name.
func TestHamqttLayoutIgnoresAddressChannelAndBucket(t *testing.T) {
	l := NewLayout(goldenRoot)
	base := model.Slot{
		Scope:   []string{goldenDeviceDE},
		Address: "homeconnect_geschirrspuler",
		Bucket:  model.BucketValues,
		Path:    []string{"BSH", "Common", "Status", "OperationState"},
	}
	perturbed := base
	perturbed.Address = "something_else_entirely"
	perturbed.Channel = "7"
	perturbed.Bucket = model.BucketMaster

	for _, tc := range []struct {
		name       string
		render     func(model.Slot) string
		wantSuffix string
	}{
		{"State", l.State, "/state"},
		{"Command", l.Command, "/set"},
		{"Availability", l.Availability, "/availability"},
	} {
		a, b := tc.render(base), tc.render(perturbed)
		if a != b {
			t.Errorf("%s reads Address, Channel or Bucket: %q vs %q", tc.name, a, b)
		}
		if !strings.HasSuffix(a, tc.wantSuffix) || !strings.HasPrefix(a, goldenRoot+"/"+goldenDeviceDE+"/") {
			t.Errorf("%s = %q, want <root>/<raw device>/…%s", tc.name, a, tc.wantSuffix)
		}
	}
	// The inverse: Scope and Path ARE read, so this test cannot pass by the
	// layout ignoring everything.
	moved := base
	moved.Scope = []string{"Waschmaschine"}
	if l.State(moved) == l.State(base) {
		t.Error("State ignores Scope — the layout reads nothing at all")
	}
	moved = base
	moved.Path = []string{"BSH", "Common", "Status", "DoorState"}
	if l.State(moved) == l.State(base) {
		t.Error("State ignores Path — the layout reads nothing at all")
	}
}

// TestHamqttTopicFormIsTheFiveSegmentNodeIDForm settles the fact step 6
// turns on, with evidence rather than by reading the source.
//
// This bridge publishes <prefix>/<platform>/<node_id>/<object_id>/config —
// five segments, node id present — which is go-hamqtt's DEFAULT legacy form,
// publisher.LegacyTopicWithNodeID. Both sibling bridges (go-zendure2mqtt,
// go-mtec2mqtt) needed publisher.LegacyTopicByUniqueID instead, so the
// precedent points the wrong way here and stating nothing is the correct
// statement.
//
// Getting it wrong is silent in exactly the way that costs a whole fleet:
// publisher.SupersededTopics would retract nothing, the device bundle would
// land while all 687 per-entity configs are still retained, and Home
// Assistant would answer with one
// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message`
// and no entities at all.
//
// The proof is against the PINNED topics, not against configTopic: every
// topic in identity_en.json is re-derived from LegacyTopicWithNodeID and
// must come out identical, and LegacyTopicByUniqueID must come out
// different for every one of them.
func TestHamqttTopicFormIsTheFiveSegmentNodeIDForm(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "identity_en.json")) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read identity_en.json: %v", err)
	}
	var pinned []goldenIdentity
	if err := json.Unmarshal(raw, &pinned); err != nil {
		t.Fatalf("identity_en.json: %v", err)
	}
	if len(pinned) == 0 {
		t.Fatal("identity_en.json is empty")
	}

	matched, wrongForm := 0, 0
	for _, row := range pinned {
		parsed, ok := publisher.ParseConfigTopic(goldenPrefix, row.ConfigTopic)
		if !ok {
			t.Fatalf("%s: the library cannot parse this bridge's own config topic", row.ConfigTopic)
		}
		if parsed.Bundle {
			t.Fatalf("%s parses as a device document", row.ConfigTopic)
		}
		if parsed.NodeID == "" {
			t.Fatalf("%s parses as the node-id-less four-segment form — "+
				"this bridge would need publisher.LegacyTopicByUniqueID after all", row.ConfigTopic)
		}
		e := publisher.LegacyEntity{
			Prefix:   goldenPrefix,
			Platform: parsed.Platform,
			NodeID:   parsed.NodeID,
			ObjectID: parsed.ObjectID,
			UniqueID: row.UniqueID,
		}
		if got := publisher.LegacyTopicWithNodeID(e); got != row.ConfigTopic {
			t.Errorf("LegacyTopicWithNodeID = %q, pinned %q", got, row.ConfigTopic)
			continue
		}
		matched++
		if publisher.LegacyTopicByUniqueID(e) != row.ConfigTopic {
			wrongForm++
		}
	}
	if matched != len(pinned) {
		t.Fatalf("LegacyTopicWithNodeID reproduced %d of %d pinned config topics", matched, len(pinned))
	}
	if wrongForm != len(pinned) {
		t.Errorf("LegacyTopicByUniqueID also reproduces %d pinned topics — the two forms are "+
			"not distinguishable here and the verdict is not safe", len(pinned)-wrongForm)
	}
	t.Logf("VERDICT: all %d pinned config topics are the five-segment node-id form. "+
		"Step 6 must use go-hamqtt's DEFAULT (publisher.LegacyTopicWithNodeID) and pass no "+
		"forms argument to SupersededTopics; LegacyTopicByUniqueID — what both sibling "+
		"bridges needed — reproduces none of the %d.", matched, len(pinned))
}

// f13RefusedOverrides is finding F13, discovered by step 4 and FIXED here:
// the shipped mapping.yaml assigns thirteen features a device_class drawn
// from Home Assistant's BINARY_SENSOR vocabulary, and eleven of them land on
// the `sensor` platform, where Home Assistant declares no such class and
// discarded the entity whole — silently, during schema validation, with
// nothing on the wire and nothing in a log.
//
// The map is now the set of overrides the enrichment step REFUSES, keyed by
// the entity key the refusal is observed on, with the class that is dropped.
// The eleven rows are the same eleven; what changed is which side of the
// assertion they sit on. Each one:
//
//   - keeps its platform, its config topic, its unique_id and its
//     default_entity_id — no identity string moves, so no entity is stranded
//     and no history is lost;
//   - loses the device_class the sensor platform refuses, EXCEPT
//     battery_charging, whose entity is an enum sensor: there the heuristic
//     class the refusal falls back to is `enum`, and the options list that
//     used to be deleted as collateral (the rider below) comes back with it.
//
// The rider. sanitizeForPlatform's enum branch is keyed on device_class ==
// "enum", so an enum sensor whose class the catalogue overrode fell through
// to `delete(p, "options")` and lost its options list too. Fixing the
// override at the point it is applied fixes that half by construction:
// bsh_common_status_batterychargingstate keeps `enum` and keeps its three
// options. The OTHER half is deliberately left, and is not a defect —
// BSH.Common.Status.BatteryLevel is an enum sensor whose catalogue class is
// `battery`, which the sensor platform DOES declare. Home Assistant's sensor
// schema permits `options` only alongside device_class `enum`, so keeping
// both would produce exactly the refused config this finding is about.
// Dropping `options` is the only legal resolution of an override the operator
// is entitled to make. TestValidDeviceClassOverrideStillDropsOptions pins it.
var f13RefusedOverrides = map[string]string{
	"bsh_common_status_batterychargingstate":           "battery_charging",
	"bsh_common_status_chargingconnection":             "plug",
	"refrigeration_common_status_door_bottlecooler":    "door",
	"refrigeration_common_status_door_chiller":         "door",
	"refrigeration_common_status_door_chillercommon":   "door",
	"refrigeration_common_status_door_chillerleft":     "door",
	"refrigeration_common_status_door_chillerright":    "door",
	"refrigeration_common_status_door_flexcompartment": "door",
	"refrigeration_common_status_door_freezer":         "door",
	"refrigeration_common_status_door_refrigerator":    "door",
	"refrigeration_common_status_door_winecompartment": "door",
}

// TestHamqttPayloadsPassDiscoveryValidate runs every rendered per-entity
// body through the library's schema validator — the question neither sibling
// bridge had ever asked of its own output, and the one that turned up F13.
//
// Step 4 recorded 11 of 687 refused in each of the three enriched
// configurations and 0 of 687 in the unenriched one. This step is the reason
// that number is now ZERO everywhere: 687 of 687 accepted, in all four
// configurations. Nothing may be refused, and the count is asserted rather
// than merely the absence of errors, so a configuration that silently stops
// rendering entities cannot pass this.
func TestHamqttPayloadsPassDiscoveryValidate(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			d := New(nil, goldenPrefix, goldenRoot, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
			if tc.enriched {
				d.SetEnricher(pinEnricher(t))
			}
			rows, err := d.hamqttComponents(tc.device, pincatalog.Info, pinEntities(t))
			if err != nil {
				t.Fatalf("hamqttComponents: %v", err)
			}
			if want := 687; !tc.curated && len(rows) != want {
				t.Fatalf("rendered %d components, want %d", len(rows), want)
			}

			refused, advisory := 0, 0
			for _, r := range rows {
				var body map[string]any
				if err := json.Unmarshal(r.Payload, &body); err != nil {
					t.Fatalf("%s: %v", r.Topic, err)
				}
				err := discovery.ValidateBody(hacatalog.Platform(r.Platform), body)
				if err == nil {
					continue
				}
				var ve *discovery.ValidationError
				if !asValidationError(err, &ve) {
					t.Errorf("%s: %v", r.Topic, err)
					continue
				}
				if !ve.Blocking() {
					advisory++
					t.Logf("%s: advisory: %v", r.Topic, err)
					continue
				}
				refused++
				t.Errorf("%s is refused by discovery.Validate: %v\n"+
					"F13 is fixed; a refusal here is a NEW defect, and at ADR 0070 step 6 it "+
					"costs the device every one of its %d entities, not this one.", r.Topic, err, len(rows))
			}
			if refused != 0 {
				t.Errorf("%d of %d payloads refused, want 0", refused, len(rows))
			}
			t.Logf("%d of %d payloads accepted by discovery.Validate (%d advisory)",
				len(rows)-refused, len(rows), advisory)
		})
	}
}

// TestEveryRefusedOverrideIsDroppedAndNothingElseMoves is the fix observed at
// the point it happens, on the finished payloads of the real builder rather
// than through the library.
//
// For each of the eleven it asserts the three things a reader of the PR body
// is entitled to check: the refused class is gone, the identity plane
// (config topic, unique_id, default_entity_id) is UNCHANGED from what the
// golden pinned, and the one enum row got its options back. It also asserts
// the converse — that exactly these eleven rows differ between an
// origin/main-shaped payload and this one — by counting the rows that carry a
// device_class the sensor platform refuses, which must be zero.
func TestEveryRefusedOverrideIsDroppedAndNothingElseMoves(t *testing.T) {
	rows := publishPin(t, "en", goldenDeviceEN, false, true)
	seen := map[string]bool{}
	for _, r := range rows {
		seg := strings.Split(r.Topic, "/")
		platform, key := seg[1], seg[len(seg)-2]
		dc, _ := r.Payload["device_class"].(string)
		if dc != "" && !deviceClassAllowed(platform, dc) {
			t.Errorf("%s still carries device_class %q, which %s does not declare", r.Topic, dc, platform)
		}
		refusedClass, isF13 := f13RefusedOverrides[key]
		if !isF13 {
			continue
		}
		seen[key] = true
		if dc == refusedClass {
			t.Errorf("%s still carries the refused device_class %q", r.Topic, dc)
		}
		if platform != platformSensor {
			t.Errorf("%s moved to platform %q — F13's fix must not move an entity between "+
				"platforms; unique_id and identifiers have no migration path", r.Topic, platform)
		}
		if r.Payload["unique_id"] != "homeconnect_dishwasher_"+key {
			t.Errorf("%s: unique_id %v moved", r.Topic, r.Payload["unique_id"])
		}
		if refusedClass == "battery_charging" {
			if dc != deviceClassEnum {
				t.Errorf("%s: device_class %q, want the heuristic's %q — the refusal must fall "+
					"back to the heuristic, not clear the key", r.Topic, dc, deviceClassEnum)
			}
			opts, ok := r.Payload["options"].([]any)
			if !ok || len(opts) != 3 {
				t.Errorf("%s: options %v, want the three enum values back (the F13 rider)", r.Topic, r.Payload["options"])
			}
		} else if dc != "" {
			t.Errorf("%s: device_class %q, want none — the heuristic derives none here", r.Topic, dc)
		}
	}
	if len(seen) != len(f13RefusedOverrides) {
		t.Errorf("observed %d of the %d refused overrides; the fixture no longer reaches them all",
			len(seen), len(f13RefusedOverrides))
	}
}

// TestValidDeviceClassOverrideStillDropsOptions pins the half of the rider
// that is NOT a defect, so a later reader does not "fix" it.
//
// BSH.Common.Status.BatteryLevel is an enum sensor whose catalogue class is
// `battery` — a class the sensor platform DOES declare, so the override
// applies. Home Assistant's sensor schema accepts `options` only alongside
// device_class `enum`; keeping both would be the very refused config F13 is
// about. So the options list is dropped and the operator's class wins, and
// that is the only legal resolution rather than an oversight.
func TestValidDeviceClassOverrideStillDropsOptions(t *testing.T) {
	const key = "bsh_common_status_batterylevel"
	found := false
	for _, r := range publishPin(t, "en", goldenDeviceEN, false, true) {
		if !strings.HasSuffix(r.Topic, "/"+key+"/config") {
			continue
		}
		found = true
		if got := r.Payload["device_class"]; got != "battery" {
			t.Errorf("%s: device_class %v, want \"battery\" — a VALID override must still apply", r.Topic, got)
		}
		if _, has := r.Payload["options"]; has {
			t.Errorf("%s: carries options alongside device_class \"battery\"; "+
				"Home Assistant's sensor schema permits options only with device_class \"enum\"", r.Topic)
		}
		var body map[string]any
		if err := json.Unmarshal(mustJSON(t, r.Payload), &body); err != nil {
			t.Fatal(err)
		}
		if err := discovery.ValidateBody(hacatalog.Platform(platformSensor), body); err != nil {
			var ve *discovery.ValidationError
			if asValidationError(err, &ve) && ve.Blocking() {
				t.Errorf("%s: %v", r.Topic, err)
			}
		}
	}
	if !found {
		t.Fatalf("%s is not in the pin; this test's premise has changed", key)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// catalogueClassesInvalidPerPlatform is F13's FULL extent, measured at the
// source rather than at the fixture: for every platform this bridge can
// classify a feature onto, the catalogued (feature, device_class) pairs that
// Home Assistant's schema for that platform refuses.
//
// The fixture is one appliance mix and the next one differs. Which of the
// thirteen sensor rows a given appliance reaches depends on how it models the
// element — a read-only boolean becomes a binary_sensor, where the class is
// legal, and an enumeration or a string becomes a sensor, where it is not —
// so the fixture's eleven is a sample and these numbers are the bound.
//
// Two of the six platforms were never the finding's subject and are listed
// because they are the reason the structural fix is safe: the old
// hand-maintained sets for binary_sensor, switch and button were EXACTLY Home
// Assistant's, so replacing them with go-ha-catalog's tables moves nothing
// there. The two that moved are sensor (`return true`, i.e. 13 refusals
// waiting) and number (`dc != "enum"`, i.e. 18).
var catalogueClassesInvalidPerPlatform = map[string]int{
	platformSensor:       13,
	platformNumber:       18,
	platformBinarySensor: 21,
	platformSelect:       35,
	platformSwitch:       35,
	platformButton:       35,
}

// f13CatalogueRows is the sensor row of the table above, by feature name.
// These thirteen are the whole of F13 over the shipped catalogue; the
// fixture reaches eleven of them.
var f13CatalogueRows = map[string]string{
	"BSH.Common.Appliance.Connected":                   "connectivity",
	"BSH.Common.Status.BatteryChargingState":           "battery_charging",
	"BSH.Common.Status.ChargingConnection":             "plug",
	"BSH.Common.Status.InteriorIlluminationActive":     "light",
	"Refrigeration.Common.Status.Door.BottleCooler":    "door",
	"Refrigeration.Common.Status.Door.Chiller":         "door",
	"Refrigeration.Common.Status.Door.ChillerCommon":   "door",
	"Refrigeration.Common.Status.Door.ChillerLeft":     "door",
	"Refrigeration.Common.Status.Door.ChillerRight":    "door",
	"Refrigeration.Common.Status.Door.FlexCompartment": "door",
	"Refrigeration.Common.Status.Door.Freezer":         "door",
	"Refrigeration.Common.Status.Door.Refrigerator":    "door",
	"Refrigeration.Common.Status.Door.WineCompartment": "door",
}

// TestCatalogueDeviceClassesAgainstEveryPlatform measures F13 at its source,
// independently of the pin fixture: the shipped mapping.yaml against
// go-ha-catalog's own per-platform device-class tables, for every platform
// classify can produce.
//
// It is the test that keeps the catalogue honest as it grows. A new
// mapping.yaml entry with a class the target platform refuses moves one of
// these counts, and the change has to be argued rather than discovered later
// against a live Home Assistant.
func TestCatalogueDeviceClassesAgainstEveryPlatform(t *testing.T) {
	cat := pinEnricher(t)
	entries, err := pinEntries()
	if err != nil {
		t.Fatalf("pincatalog: %v", err)
	}
	catalogued := map[string]string{}
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		if dc, ok := cat.DeviceClass(e.Name); ok {
			catalogued[e.Name] = dc
		}
	}
	if len(catalogued) != 35 {
		t.Errorf("mapping.yaml carries %d device_class entries reachable from the pin, want 35", len(catalogued))
	}

	// The two F13 literals must agree with the catalogue and with each other.
	// f13RefusedOverrides is keyed by entity key and f13CatalogueRows by
	// feature name; without this, a wrong class in either is invisible,
	// because the eleven rows are checked for the ABSENCE of a class and
	// absence looks the same whichever class was named.
	for name, dc := range f13CatalogueRows {
		if catalogued[name] != dc {
			t.Errorf("f13CatalogueRows says %s carries %q; mapping.yaml says %q", name, dc, catalogued[name])
		}
		if got, reached := f13RefusedOverrides[slugify(name)]; reached && got != dc {
			t.Errorf("f13RefusedOverrides says %s carries %q, f13CatalogueRows says %q",
				name, got, dc)
		}
	}
	for key, dc := range f13RefusedOverrides {
		found := false
		for name, want := range f13CatalogueRows {
			if slugify(name) == key {
				found, _ = true, want
				break
			}
		}
		if !found {
			t.Errorf("f13RefusedOverrides row %q (%q) has no feature in f13CatalogueRows", key, dc)
		}
	}
	for name, dc := range f13AlreadyStrippedDownstream {
		if catalogued[name] != dc {
			t.Errorf("f13AlreadyStrippedDownstream says %s carries %q; mapping.yaml says %q",
				name, dc, catalogued[name])
		}
	}

	for _, platform := range []string{
		platformSensor, platformNumber, platformBinarySensor,
		platformSelect, platformSwitch, platformButton,
	} {
		invalid := map[string]string{}
		for name, dc := range catalogued {
			if !deviceClassAllowed(platform, dc) {
				invalid[name] = dc
			}
		}
		if got, want := len(invalid), catalogueClassesInvalidPerPlatform[platform]; got != want {
			t.Errorf("%s: %d catalogued classes the platform refuses, want %d:\n%v", platform, got, want, invalid)
		}
		if platform != platformSensor {
			continue
		}
		for name, dc := range f13CatalogueRows {
			if invalid[name] != dc {
				t.Errorf("sensor: %s: refused class %q, want %q", name, invalid[name], dc)
			}
		}
		for name, dc := range invalid {
			if _, known := f13CatalogueRows[name]; !known {
				t.Errorf("sensor: %s carries the refused class %q and is not a known F13 row", name, dc)
			}
		}
	}
	t.Logf("F13's full extent over the shipped catalogue: %d of %d catalogued device classes "+
		"are refused on `sensor`, %d on `number`. The fixture reaches %d of the sensor rows; "+
		"which ones a real appliance reaches depends on how it models the element.",
		len(f13CatalogueRows), len(catalogued),
		catalogueClassesInvalidPerPlatform[platformNumber], len(f13RefusedOverrides))
}

// TestDeviceClassTableLoads asserts the premise deviceClassAllowed rests on:
// go-ha-catalog's embedded per-platform tables decode, and every platform
// this bridge classifies onto is present in them.
//
// deviceClassAllowed fails CLOSED when the table is missing — every entity
// loses its device_class rather than carrying one Home Assistant refuses —
// and that branch must be unreachable in a correct build rather than a
// behaviour anyone relies on.
// TestDeviceClassAllowanceFailsClosedWithoutTheTable reaches the branch a
// correct build cannot: go-ha-catalog's snapshot is embedded, so the decode
// cannot fail at runtime, and a mutation from `return false` to `return true`
// there changes no byte and fails no test unless the branch is exercised
// deliberately.
//
// It must fail CLOSED. Publishing no device_class costs every entity an icon
// and its class semantics and is recoverable by an operator; publishing one
// the platform refuses is not visible at all, and at ADR 0070 step 6 it costs
// the whole device.
func TestDeviceClassAllowanceFailsClosedWithoutTheTable(t *testing.T) {
	for _, platform := range []string{
		platformSensor, platformNumber, platformBinarySensor,
		platformSelect, platformSwitch, platformButton,
	} {
		for _, dc := range []string{"temperature", deviceClassEnum, "door", "outlet", "restart", ""} {
			if deviceClassAllowedIn(nil, platform, dc) {
				t.Errorf("deviceClassAllowedIn(nil, %q, %q) = true; it must fail closed", platform, dc)
			}
		}
	}
	// And with a table it is the table that decides, so the nil check is not
	// simply a constant false.
	tbl := map[string]map[string]bool{platformSensor: {"temperature": true}}
	if !deviceClassAllowedIn(tbl, platformSensor, "temperature") {
		t.Error("deviceClassAllowedIn ignores the table it is given")
	}
	if deviceClassAllowedIn(tbl, platformSensor, "door") {
		t.Error("deviceClassAllowedIn allows a class the table it is given does not list")
	}
}

func TestDeviceClassTableLoads(t *testing.T) {
	tbl := deviceClasses()
	if tbl == nil {
		t.Fatal("go-ha-catalog's device-class table failed to decode; deviceClassAllowed is failing closed")
	}
	want := map[string]int{
		platformSensor: 62, platformNumber: 58, platformBinarySensor: 28,
		platformSwitch: 2, platformButton: 3, platformSelect: 0,
	}
	for platform, n := range want {
		if got := len(tbl[platform]); got != n {
			t.Errorf("%s: %d device classes in the catalogue snapshot, want %d "+
				"(go-ha-catalog %s tracks Home Assistant %s)",
				platform, got, n, hacatalog.SnapshotRef, hacatalog.SnapshotVersion)
		}
	}
	if tbl[platformSensor][deviceClassEnum] != true {
		t.Error("`enum` is not a sensor class in the snapshot; the enum-sensor branch rests on it")
	}
	for _, dc := range []string{"door", "plug", "connectivity", "light", "battery_charging"} {
		if tbl[platformSensor][dc] {
			t.Errorf("`%s` is now a sensor class; F13's premise has changed", dc)
		}
		if !tbl[platformBinarySensor][dc] {
			t.Errorf("`%s` is no longer a binary_sensor class; F13's premise has changed", dc)
		}
	}
}

// TestTableDrivenAllowanceMatchesTheSetsItReplaced is the safety half of the
// structural fix. deviceClassAllowed used to hold three hand-maintained class
// sets plus two judgements about "open" vocabularies; it now reads Home
// Assistant's own tables. Three of the six platforms must be unaffected by
// that swap, and the literals below are the sets as origin/main held them,
// transcribed once so the claim is checked rather than asserted in prose.
func TestTableDrivenAllowanceMatchesTheSetsItReplaced(t *testing.T) {
	previous := map[string][]string{
		platformBinarySensor: {
			"battery", "battery_charging", "carbon_monoxide", "cold", "connectivity", "door",
			"garage_door", "gas", "heat", "light", "lock", "moisture", "motion", "moving",
			"occupancy", "opening", "plug", "power", "presence", "problem", "running", "safety",
			"smoke", "sound", "tamper", "update", "vibration", "window",
		},
		platformSwitch: {"outlet", "switch"},
		platformButton: {"identify", "restart", "update"},
	}
	tbl := deviceClasses()
	for platform, classes := range previous {
		if len(tbl[platform]) != len(classes) {
			t.Errorf("%s: table has %d classes, the set it replaced had %d",
				platform, len(tbl[platform]), len(classes))
		}
		for _, dc := range classes {
			if !deviceClassAllowed(platform, dc) {
				t.Errorf("%s: %q was allowed before the swap and is refused now", platform, dc)
			}
		}
	}
	// select took none before and takes none now.
	for _, dc := range []string{"door", "enum", "temperature", "power", "outlet"} {
		if deviceClassAllowed(platformSelect, dc) {
			t.Errorf("select: %q is allowed; select declares no device class at all", dc)
		}
	}
	// The two that DID move, named so the change is not silent.
	if deviceClassAllowed(platformSensor, "door") {
		t.Error("sensor still accepts `door` — F13 is not fixed")
	}
	if deviceClassAllowed(platformNumber, "timestamp") {
		t.Error("number accepts `timestamp`, which it does not declare")
	}
	if !deviceClassAllowed(platformSensor, "timestamp") {
		t.Error("sensor refuses `timestamp`, which it does declare — the swap over-tightened")
	}
}

// f13AlreadyStrippedDownstream is the part of the refusal that moves no byte.
//
// Both IDos base levels are writable enums, so they classify onto `select`,
// and `select` declares no device class at all — so sanitizeForPlatform
// already deleted the catalogue's "volume" at the end of the chain. Refusing
// the override earlier changes nothing about what is published; what it
// changes is that the daemon now SAYS so. They are listed because a reader
// counting log lines against the eleven moved rows would otherwise find two
// too many and have to work out why.
var f13AlreadyStrippedDownstream = map[string]string{
	"LaundryCare.Washer.Setting.IDos1BaseLevel": "volume",
	"LaundryCare.Washer.Setting.IDos2BaseLevel": "volume",
}

// TestRefusedOverrideIsLogged asserts the other half of what F13 was about:
// Home Assistant says nothing when it drops such an entity, so the daemon
// must. The refusal is the one place both halves of the pair are known.
//
// It also fixes the exact refusal set over the shipped catalogue and this
// fixture — thirteen, of which eleven move a published byte and two were
// already stripped downstream — so a catalogue edit that adds a fourteenth
// has to come here and say which.
func TestRefusedOverrideIsLogged(t *testing.T) {
	var buf bytes.Buffer
	d := New(nil, goldenPrefix, goldenRoot, "en", false,
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	d.SetEnricher(pinEnricher(t))
	dev := d.deviceBlockFor(goldenDeviceEN, pincatalog.Info)
	for _, e := range pinEntities(t) {
		platform, ok := classify(e)
		if !ok {
			continue
		}
		p := payloadFor(e, platform, goldenDeviceEN, d.topicsFor(goldenDeviceEN, e), dev)
		d.applyEnrichment(e, p, platform)
	}
	out := buf.String()

	// Parsed into (feature -> "platform/device_class") rather than matched as
	// substrings, so the assertion is over the refusal SET and not over the
	// handler's formatting.
	logged := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "hass.device_class_refused") {
			continue
		}
		var feature, platform, dc string
		for _, field := range strings.Fields(line) {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch k {
			case "feature":
				feature = v
			case "platform":
				platform = v
			case "device_class":
				dc = v
			}
		}
		if feature != "" {
			logged[feature] = platform + "/" + dc
		}
	}

	want := map[string]string{}
	for feature, dc := range f13CatalogueRows {
		if _, reached := f13RefusedOverrides[slugify(feature)]; reached {
			want[feature] = platformSensor + "/" + dc
		}
	}
	for feature, dc := range f13AlreadyStrippedDownstream {
		want[feature] = platformSelect + "/" + dc
	}
	if len(logged) != len(want) {
		t.Errorf("logged %d refusals, want %d (%d that move a published byte, %d already "+
			"stripped downstream)\n%s", len(logged), len(want), len(f13RefusedOverrides),
			len(f13AlreadyStrippedDownstream), out)
	}
	for feature, pair := range want {
		if logged[feature] != pair {
			t.Errorf("refusal for %s logged as %q, want %q", feature, logged[feature], pair)
		}
	}
	for feature, pair := range logged {
		if _, known := want[feature]; !known {
			t.Errorf("%s was refused (%s) and is not a known F13 row", feature, pair)
		}
	}
}

// TestHamqttBundleValidates is the schema question for the form step 6 will
// publish. Nothing publishes a bundle yet; this only asks whether the
// document this catalogue renders to is one Home Assistant would accept, so
// step 6 does not discover the answer against a live installation.
//
// Step 4 found it Blocking() in the three enriched configurations, carrying
// the eleven F13 rows — and a blocking bundle publishes NOTHING, so those
// eleven would have cost the device all 687 of its entities rather than
// eleven. That is the whole reason F13 had to be fixed before step 6, and
// this is the assertion that says it was: the bundle now validates
// NON-BLOCKING in every configuration.
func TestHamqttBundleValidates(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			d := New(nil, goldenPrefix, goldenRoot, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
			if tc.enriched {
				d.SetEnricher(pinEnricher(t))
			}
			b, err := d.hamqttBundle(tc.device, pincatalog.Info, pinEntities(t))
			if err != nil {
				t.Fatalf("hamqttBundle: %v", err)
			}
			if got, want := b.Topic(goldenPrefix), goldenPrefix+"/device/"+slugify(tc.device)+"/config"; got != want {
				t.Errorf("bundle topic = %q, want %q", got, want)
			}
			if b.NodeID != slugify(tc.device) {
				t.Errorf("bundle node id = %q, want %q", b.NodeID, slugify(tc.device))
			}

			err = discovery.Validate(b)
			if err == nil {
				t.Logf("bundle %q validates clean: %d components publishable as one document",
					b.NodeID, len(b.Components))
				return
			}
			var ve *discovery.ValidationError
			if !asValidationError(err, &ve) {
				t.Fatalf("bundle: %v", err)
			}
			if ve.Blocking() {
				t.Errorf("the bundle is BLOCKING, which publishes nothing at all — at step 6 "+
					"that costs the device all %d of its components:\n%v", len(b.Components), err)
				return
			}
			t.Logf("bundle advisory (non-blocking, %d components): %v", len(b.Components), err)
		})
	}
}

// asValidationError is errors.As without importing errors into every call.
func asValidationError(err error, target **discovery.ValidationError) bool {
	ve, ok := err.(*discovery.ValidationError) //nolint:errorlint // the validator returns this concretely
	if ok {
		*target = ve
	}
	return ok
}

// TestHamqttReproducesBothButtonPaths is the platform this rollout meets for
// the first time. Neither go-zendure2mqtt nor go-mtec2mqtt publishes a
// button, so nothing before this proved the library renders one faithfully —
// and this bridge publishes twenty per appliance from two different builders
// (#41 converged them onto basePayload, F6).
//
// The byte comparison above already covers them; this names them, so a
// regression that dropped every button would fail with the word "button" in
// it rather than as twenty missing rows among 687.
//
// A button is the platform where the render pipeline's defaults are most
// likely to be wrong, because it is write-only: Home Assistant declares no
// state_topic on it, so a library that projected one unconditionally would
// publish a key Home Assistant drops in silence, and a library that gated the
// command topic on a readable binding would publish no topic at all.
func TestHamqttReproducesBothButtonPaths(t *testing.T) {
	want := map[string]map[string]any{}
	for _, r := range readGoldenRows(t, "discovery_full_en.json") {
		if strings.HasPrefix(r.Topic, goldenPrefix+"/button/") {
			want[r.Topic] = r.Payload
		}
	}
	if len(want) != 20 {
		t.Fatalf("golden has %d buttons, want 20 (18 command-derived + 2 synthetic)", len(want))
	}

	var derived, synthetic int
	for _, r := range hamqttPin(t, goldenCase{lang: "en", device: goldenDeviceEN, enriched: true}) {
		if !strings.HasPrefix(r.Topic, goldenPrefix+"/button/") {
			continue
		}
		w, ok := want[r.Topic]
		if !ok {
			t.Errorf("go-hamqtt rendered a button the golden does not have: %s", r.Topic)
			continue
		}
		if wb, gb := canonical(t, w), canonical(t, r.Payload); !bytes.Equal(wb, gb) {
			t.Errorf("%s:\n golden: %s\n  built: %s", r.Topic, wb, gb)
			continue
		}
		delete(want, r.Topic)
		if r.Payload["payload_press"] == controlPressPayload {
			synthetic++
		} else {
			derived++
		}
		if _, has := r.Payload["state_topic"]; has {
			t.Errorf("%s carries a state_topic; a button is write-only", r.Topic)
		}
		if _, has := r.Payload["command_topic"]; !has {
			t.Errorf("%s carries no command_topic; the press would go nowhere", r.Topic)
		}
	}
	for topic := range want {
		t.Errorf("go-hamqtt did not render the button %s", topic)
	}
	if derived != 18 || synthetic != 2 {
		t.Errorf("reproduced %d derived and %d synthetic buttons, want 18 and 2", derived, synthetic)
	}
	t.Logf("both button paths reproduced: %d command-derived (payload_press %q) and "+
		"%d synthetic (payload_press %q)", derived, commandPressPayload, synthetic, controlPressPayload)
}

// TestButtonDeviceClassFilterIsInertOverThisCatalogue makes explicit a
// branch the goldens can never exercise.
//
// sanitizeForPlatform strips a device_class the button platform does not
// declare — Home Assistant accepts only identify, restart and update there.
// No command feature in the shipped mapping.yaml carries a device_class at
// all, and the heuristic derives none for a command (deviceClassAndUnit reads
// the content type, and a command has none), so across all 2 238 pinned
// payloads the branch is never taken. A mutation to it changes no byte and
// fails no test: it is a blind spot, and the honest answer is to say so and
// assert the behaviour directly rather than to claim golden coverage it does
// not have.
//
// Both paths are asserted, because both ran sanitizeForPlatform: the
// hand-built one on a map and the go-hamqtt one on a Description.
func TestButtonDeviceClassFilterIsInertOverThisCatalogue(t *testing.T) {
	// The blind spot itself: nothing in the pin reaches the branch.
	for _, r := range hamqttPin(t, goldenCase{lang: "en", device: goldenDeviceEN, enriched: true}) {
		if !strings.HasPrefix(r.Topic, goldenPrefix+"/button/") {
			continue
		}
		if dc, has := r.Payload["device_class"]; has {
			t.Errorf("%s carries device_class %v — the button filter is no longer inert; "+
				"this test's premise has changed", r.Topic, dc)
		}
	}

	// The behaviour, asserted directly on both renderers.
	for _, tc := range []struct {
		dc   string
		keep bool
	}{
		{"restart", true},
		{"identify", true},
		{"update", true},
		{"door", false},
		{deviceClassEnum, false},
		{"power", false},
	} {
		p := map[string]any{"device_class": tc.dc}
		sanitizeForPlatform(p, platformButton)
		_, kept := p["device_class"]
		if kept != tc.keep {
			t.Errorf("sanitizeForPlatform(button, %q): kept=%v, want %v", tc.dc, kept, tc.keep)
		}

		desc := &model.Description{DeviceClass: model.DeviceClass(tc.dc)}
		sanitizeDescriptionForPlatform(desc, platformButton)
		if got := string(desc.DeviceClass) != ""; got != tc.keep {
			t.Errorf("sanitizeDescriptionForPlatform(button, %q): kept=%v, want %v", tc.dc, got, tc.keep)
		}
	}
}

// refusingPublisher fails the test if anything reaches the broker.
type refusingPublisher struct{ t *testing.T }

func (p refusingPublisher) Publish(_ context.Context, topic string, _ []byte) (bool, error) {
	p.t.Errorf("the go-hamqtt rendering path published to %s — it publishes NOTHING", topic)
	return false, nil
}

// TestHamqttRenderPathPublishesNothing is the step-4 constraint as an
// assertion rather than as a promise in a doc comment.
//
// The whole experiment is arranged so that a failure costs nothing: no
// change to the publish path, to the coordinator or to the MQTT bootstrap,
// and no byte on a broker. This drives every rendering entry point with a
// Publisher that fails the test on contact.
func TestHamqttRenderPathPublishesNothing(t *testing.T) {
	for _, tc := range goldenCases {
		d := New(refusingPublisher{t}, goldenPrefix, goldenRoot, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
		if tc.enriched {
			d.SetEnricher(pinEnricher(t))
		}
		if _, err := d.hamqttComponents(tc.device, pincatalog.Info, pinEntities(t)); err != nil {
			t.Fatalf("hamqttComponents: %v", err)
		}
		if _, err := d.hamqttBundle(tc.device, pincatalog.Info, pinEntities(t)); err != nil {
			t.Fatalf("hamqttBundle: %v", err)
		}
	}
}

// TestObjectIDCompositionIsAnEquivalentMutant records a mutation that could
// NOT be made to fail, and why.
//
// ObjectID slugifies the joined string, slugify(device + "_" + key). The
// obvious alternative, slugify(device) + "_" + slugify(key), survives every
// assertion in this package — 2 238 payloads, both device probes, all 687
// keys — because the two are equal for every input the daemon can reach:
// slugify collapses a run of separators to one underscore and trims the
// ends, so the only way they can differ is for one half to slugify to the
// empty string, and profile.validateDeviceName refuses a device name with no
// ASCII letter or digit for exactly that reason.
//
// #41's standard is that an assertion which cannot be made to fail is named
// rather than left as coverage nobody checked. This is that naming: the
// mutation is EQUIVALENT, not missed, and the equivalence is asserted here
// over the whole catalogue plus the device-name probes from §3.2 of the
// measurement, together with the one input that breaks it.
func TestObjectIDCompositionIsAnEquivalentMutant(t *testing.T) {
	devices := []string{
		goldenDeviceEN, goldenDeviceDE, "Küche", "Café", "ÜÄÖ",
		"Waschmaschine / Trockner", "Dish_", "A-1", "x  y",
	}
	entities := pinEntities(t)
	n := 0
	for _, device := range devices {
		for _, e := range entities {
			key := featureKey(e)
			joined := slugify(device + "_" + key)
			separate := slugify(device) + "_" + slugify(key)
			if joined != separate {
				t.Errorf("device %q key %q: joined %q, separate %q", device, key, joined, separate)
			}
			n++
		}
	}
	// The one input that separates them — and the one profile.LoadDevices
	// refuses, because both halves of the identity would be empty.
	if slugify("()"+"_"+"x") == slugify("()")+"_"+slugify("x") {
		t.Error("the two compositions no longer differ for a device name that slugifies to " +
			"nothing; this test's premise has changed and ObjectID is now genuinely untested")
	}
	t.Logf("the two ObjectID compositions agree over %d device x key pairs", n)
}

// TestGoHamqttRefusesAStateTopicOnAWriteOnlyPlatform records the second
// mutation that could not be made to fail, and turns it into an assertion.
//
// Giving a button a readable state binding changes no byte: the library
// projects state_topic only onto the platforms whose schema declares it, and
// button is one of the ten that do not. That is a guarantee worth having —
// a key Home Assistant does not declare is dropped in silence, so publishing
// one is invisible in both directions — but it is the LIBRARY's guarantee,
// and a test that only ever renders correct input never exercises it.
//
// So this renders the wrong input deliberately: a button entity with a
// readable state binding, which the old hand-built path would have happily
// given a state_topic (payloadFor branches on the platform, not on a schema).
func TestGoHamqttRefusesAStateTopicOnAWriteOnlyPlatform(t *testing.T) {
	d := New(nil, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))
	dev := hamqttDevice(goldenDeviceEN, pincatalog.Info)
	slot := hamqttSlot(dev, goldenDeviceEN, "BSH", "Common", "Command", "AcknowledgeEvent")

	ent := &model.Basic{
		EntityKey:      "bsh_common_command_acknowledgeevent",
		EntityPlatform: hacatalog.Platform(platformButton),
		Description:    model.Description{Name: model.L("Acknowledge Event")},
		Binds: []model.Binding{
			{Role: model.RoleCommand, Slot: slot, Mode: model.Write},
			{Role: model.RoleState, Slot: slot, Mode: model.Read},
		},
	}
	comp, err := discovery.RenderComponent(d.hamqttContext(), dev, ent, discovery.Origin{})
	if err != nil {
		t.Fatalf("RenderComponent: %v", err)
	}
	if comp.StateTopic != "" {
		t.Errorf("the library projected state_topic %q onto a button; Home Assistant declares "+
			"no such key on that platform and would drop it in silence", comp.StateTopic)
	}
	if comp.CommandTopic == "" {
		t.Error("the library projected no command_topic onto a button with a writable binding")
	}
	body, err := comp.EntityJSON()
	if err != nil {
		t.Fatalf("EntityJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("EntityJSON: %v", err)
	}
	if _, has := decoded["state_topic"]; has {
		t.Error("the rendered button payload carries a state_topic")
	}
}
