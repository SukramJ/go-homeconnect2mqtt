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
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
)

// hamqttPin renders one pinned configuration through go-hamqtt.
func hamqttPin(t *testing.T, tc goldenCase) []goldenRow {
	t.Helper()
	d := New(nil, goldenPrefix, goldenRoot, goldenQoS, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
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
	l := hamqttLayout{root: goldenRoot}
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
	l := hamqttLayout{root: goldenRoot}
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

// f13BinarySensorClassesOnSensors is finding F13, discovered by this step
// and by nothing before it: the shipped mapping.yaml assigns thirteen
// features a device_class drawn from Home Assistant's BINARY_SENSOR
// vocabulary, and eleven of them land on the `sensor` platform, where Home
// Assistant declares no such class and discards the entity whole.
//
// It is not a library artefact and not a fixture artefact. The mechanism is
// in deviceClassAllowed (payload.go): `case platformSensor: return true`.
// sensor and number carry Home Assistant's open-ended value classes, so the
// filter trusts the operator catalogue absolutely there — and the catalogue
// is generated to mirror the official `home_connect` integration, where
// these thirteen features ARE binary sensors. Whenever an appliance models
// one of them as anything but a read-only boolean, this bridge classifies it
// as a sensor and publishes a class the sensor platform does not accept.
//
// A second, quieter consequence rides along: sanitizeForPlatform's enum
// branch is keyed on device_class == "enum", so an enum sensor whose class
// the catalogue has overridden falls through to `delete(p, "options")` and
// loses its options list as well.
//
// What Home Assistant does with it is the part that makes this worth a
// finding at all: nothing. The config is dropped during schema validation,
// before the entity is constructed — no error on the wire, no log line
// naming the cause, and an entity that is indistinguishable from one the
// bridge never published.
//
// NOT FIXED HERE, deliberately. A fix moves bytes in three of the four
// goldens, and step 4 must regenerate nothing (the sequencing table in
// notes/adr0070-phase7-measurement.md, "What I would not do"). It is pinned
// instead, exactly, so the step that fixes it produces a diff a reviewer can
// read — and so it cannot silently grow.
var f13BinarySensorClassesOnSensors = map[string]string{
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
// body through the library's schema validator — a question neither sibling
// bridge had ever asked of its own output, and the one that turned up F13.
//
// go-mtec2mqtt's equivalent step passed clean, and that it passed was
// previously unknown. This one does not: 11 of 687 payloads are refused in
// each of the three enriched configurations, 0 of 687 in the unenriched one,
// which is what localises the cause in mapping.yaml rather than in the
// heuristic. Those eleven are asserted by name and by class; anything else
// is a failure.
func TestHamqttPayloadsPassDiscoveryValidate(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			d := New(nil, goldenPrefix, goldenRoot, goldenQoS, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
			if tc.enriched {
				d.SetEnricher(pinEnricher(t))
			}
			rows, err := d.hamqttComponents(tc.device, pincatalog.Info, pinEntities(t))
			if err != nil {
				t.Fatalf("hamqttComponents: %v", err)
			}
			// The unenriched configuration is the control: the heuristic on
			// its own produces nothing the schemas refuse.
			want := map[string]string{}
			if tc.enriched {
				want = f13BinarySensorClassesOnSensors
			}

			got := map[string]string{}
			advisory := 0
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
				seg := strings.Split(r.Topic, "/")
				key := seg[len(seg)-2]
				dc, _ := body["device_class"].(string)
				got[key] = dc
				if _, known := want[key]; !known {
					t.Errorf("%s is refused by discovery.Validate and is NOT a known F13 row: %v", r.Topic, err)
				}
			}
			for key, dc := range want {
				if got[key] != dc {
					t.Errorf("F13 row %q: expected device_class %q to be refused, got %q — "+
						"if this was fixed, move the golden and this literal in the same commit", key, dc, got[key])
				}
			}
			t.Logf("%d payloads validated: %d refused (all F13), %d advisory", len(rows), len(got), advisory)
		})
	}
}

// TestCatalogueAssignsBinarySensorClassesToThirteenFeatures is F13 measured
// at its source, independently of the pin fixture: the shipped mapping.yaml,
// against go-ha-catalog's own per-platform device-class tables.
//
// The pin fixture decides how many of the thirteen land on `sensor` (eleven,
// for its synthesised wire descriptors); the catalogue decides how many
// could. A real appliance that models a door status as an enumeration rather
// than a boolean reaches the same place, so this is the number that bounds
// the finding rather than the eleven above.
func TestCatalogueAssignsBinarySensorClassesToThirteenFeatures(t *testing.T) {
	want := map[string]string{
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

	classes, err := hacatalog.LoadDeviceClasses()
	if err != nil {
		t.Fatalf("LoadDeviceClasses: %v", err)
	}
	sensorClasses := map[string]bool{}
	for _, c := range classes["sensor"] {
		sensorClasses[c] = true
	}
	binaryClasses := map[string]bool{}
	for _, c := range classes["binary_sensor"] {
		binaryClasses[c] = true
	}

	cat := pinEnricher(t)
	got := map[string]string{}
	entries, err := pinEntries()
	if err != nil {
		t.Fatalf("pincatalog: %v", err)
	}
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		dc, ok := cat.DeviceClass(e.Name)
		if !ok || sensorClasses[dc] {
			continue
		}
		got[e.Name] = dc
		if !binaryClasses[dc] {
			t.Errorf("%s: device_class %q is neither a sensor nor a binary_sensor class", e.Name, dc)
		}
	}
	if len(got) != len(want) {
		t.Errorf("catalogue features carrying a non-sensor device_class: %d, want %d", len(got), len(want))
	}
	for name, dc := range want {
		if got[name] != dc {
			t.Errorf("%s: device_class %q, want %q", name, got[name], dc)
		}
	}
	for name, dc := range got {
		if _, known := want[name]; !known {
			t.Errorf("%s carries the non-sensor device_class %q and is not a known F13 row", name, dc)
		}
	}
	t.Logf("F13: %d catalogued features carry a binary_sensor device_class; "+
		"each is discarded by Home Assistant whenever the appliance models it as anything "+
		"but a read-only boolean", len(got))
}

// TestHamqttBundleValidates is the schema question for the form step 6 will
// publish. Nothing publishes a bundle yet; this only asks whether the
// document this catalogue renders to is one Home Assistant would accept, so
// step 6 does not discover the answer against a live installation.
//
// The bundle carries the same F13 rows and is therefore Blocking() in the
// three enriched configurations — and a blocking bundle publishes NOTHING,
// so at step 6 those eleven rows would cost the device all 687 of its
// entities rather than eleven. That is the cost this experiment moves from
// step 6 to here. discovery.ValidateIgnoring is not the answer: the class is
// not a key Home Assistant is known to drop, it is a value it refuses.
func TestHamqttBundleValidates(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			d := New(nil, goldenPrefix, goldenRoot, goldenQoS, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
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
			var ve *discovery.ValidationError
			switch {
			case err == nil:
				if tc.enriched {
					t.Error("the bundle now validates clean — F13 is fixed; move the goldens, " +
						"the F13 literals and this branch in the same commit")
				}
			case !asValidationError(err, &ve):
				t.Errorf("bundle: %v", err)
			case !ve.Blocking():
				t.Logf("bundle advisory: %v", err)
			case !tc.enriched:
				t.Errorf("the unenriched bundle is refused, which F13 does not explain: %v", err)
			default:
				if len(ve.Issues) != len(f13BinarySensorClassesOnSensors) {
					t.Errorf("bundle has %d blocking issues, want the %d F13 rows:\n%v",
						len(ve.Issues), len(f13BinarySensorClassesOnSensors), err)
				}
				t.Logf("bundle blocked by the %d F13 rows alone — at step 6 that costs the "+
					"device all %d components, not %d entities",
					len(ve.Issues), len(b.Components), len(ve.Issues))
			}
			t.Logf("bundle %q: %d components", b.NodeID, len(b.Components))
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

// refusingPublisher fails the test if anything reaches the MQTT client.
type refusingPublisher struct{ t *testing.T }

func (p refusingPublisher) Publish(_ context.Context, topic string, _ []byte, _ mqtt.QoS, _ bool, _ ...mqtt.PublishOption) error {
	p.t.Errorf("the go-hamqtt rendering path published to %s — step 4 publishes NOTHING", topic)
	return nil
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
		d := New(refusingPublisher{t}, goldenPrefix, goldenRoot, goldenQoS, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
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
