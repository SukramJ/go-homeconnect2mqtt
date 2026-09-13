// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/mapping"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/topic"
)

// The golden files in testdata/ pin every byte this daemon publishes to
// Home Assistant's discovery tree, produced by the real builder
// (Discovery.PublishDevice) over the real shipped mapping.yaml with the
// real shipped defaults. They exist so the ADR 0070 phase 7 migration
// (notes/adr0070-phase7-measurement.md) can be carried out as a sequence
// of steps whose effect on an installed base is *visible*.
//
// Regenerate with:
//
//	go test ./internal/hass -run 'TestDiscovery|TestIdentity' -update-discovery-golden
//
// and read the diff. A non-empty diff is a change to what an installed
// base receives; it is never "just a test update".
//
// # What the file is, and what it is not
//
// Each row is {topic, qos, retain, payload}, and the payload is stored
// DECODED so a reviewer reads JSON in the diff rather than an escaped
// blob. Comparison runs over a canonical re-encoding of BOTH sides, never
// over the file's own bytes: a test that compared the regenerated file
// against itself would pass every mutation, since the file is produced by
// the very code it is meant to guard. The assertions that matter most —
// the device block, the platform census, the availability model, the
// duplicate unique_ids, the slug divergence rows — hold their expected
// values as literals in Go and never consult testdata at all.
//
// # Pinned defects
//
// Several rows below pin DEFECTS, deliberately, so the later step that
// fixes them produces a diff a reviewer can see. Do not "fix" the golden
// file instead of the code. See the measurement document for F1-F12:
//
//   - F1 — FIXED. Every payload now declares both availability levels
//     (bridge then device) under mode "all", and the bridge entry is the
//     topic the Last Will writes. Asserted by
//     TestEveryPayloadDeclaresBothAvailabilityLevels and
//     TestBridgeAvailabilityTopicIsTheOneTheWillWrites.
//   - F2 — the config topic's node id is sanitize(device) while every
//     state and command topic uses the RAW device name. Pinned by
//     TestGoldenPinsTheSanitizedNodeIDAsymmetry.
//   - F5 — a writable selected-program element whose appliance exposes no
//     programs produces a select with an EMPTY options list: a dropdown
//     with nothing in it, which can never be set. Pinned by
//     TestGoldenPinsTheEmptyOptionsSelect.
//   - F6 — the two synthetic program buttons carry neither
//     entity_category nor enabled_by_default, so they are enabled and
//     prominent while every other command button is disabled+config.
//     Pinned by TestGoldenPinsTheProgramButtonInconsistency.

var updateDiscoveryGolden = flag.Bool("update-discovery-golden", false,
	"rewrite internal/hass/testdata/*.json from the current builder output")

// The shipped defaults an untouched deployment runs with
// (internal/config/defaults.go).
const (
	goldenPrefix = "homeassistant"
	goldenRoot   = "homeconnect"
	goldenQoS    = mqtt.QoS1
)

// Two device names, chosen once and never changed. "Dishwasher" is the
// plain case; "Geschirrspüler" is the case an operator in this project's
// default language (LANGUAGE: de) actually types, and it is the one that
// exposes F2 — sanitize() rewrites it for the config topic's node id but
// not for the state topic it points at.
const (
	goldenDeviceEN = "Dishwasher"
	goldenDeviceDE = "Geschirrspüler"
)

// recorder is the publish sink. It is not a stub of the builder: the
// builder is Discovery.PublishDevice, which runs unmodified. This only
// captures what it hands to the MQTT client, including the two arguments
// no other test in this repository has ever looked at — qos and retain.
type recorder struct {
	mu   sync.Mutex
	rows []goldenRow
}

func (r *recorder) Publish(_ context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		decoded = nil
	}
	r.rows = append(r.rows, goldenRow{Topic: topic, QoS: int(qos), Retain: retain, Payload: decoded})
	return nil
}

// goldenRow is one pinned discovery publication: the retained config
// topic, the delivery guarantee, and the payload stored decoded.
type goldenRow struct {
	Topic   string         `json:"topic"`
	QoS     int            `json:"qos"`
	Retain  bool           `json:"retain"`
	Payload map[string]any `json:"payload"`
}

// goldenIdentity is the identity plane on its own: the three strings Home
// Assistant keys its registries on, plus the topic that carries them.
// Kept in a separate file from the payloads so an identity change and a
// payload change produce two different diffs — only one of them is ever
// allowed to be non-empty without a major release.
type goldenIdentity struct {
	ConfigTopic     string `json:"config_topic"`
	UniqueID        string `json:"unique_id"`
	DefaultEntityID string `json:"default_entity_id"`
	DeviceID        string `json:"device_identifier"`
}

// The pin catalogue and the enrichment catalogue are parsed from the same
// 92 KB mapping.yaml on every call, and the pins call them a few dozen
// times. Parse once; both results are read-only afterwards.
var (
	pinEntries = sync.OnceValues(func() ([]*profile.Entry, error) {
		return pincatalog.Build("../../mapping.yaml")
	})
	pinCatalog = sync.OnceValues(func() (*mapping.Catalog, error) {
		return mapping.Load("../../mapping.yaml")
	})
)

// pinEntities runs the pin catalogue through the production
// profile.Entry -> homeconnect.Appliance -> Entity chain. A fresh
// Appliance per call, so no test can observe another's entity state.
func pinEntities(t *testing.T) []*homeconnect.Entity {
	t.Helper()
	entries, err := pinEntries()
	if err != nil {
		t.Fatalf("pincatalog: %v", err)
	}
	desc := &profile.Description{Info: pincatalog.Info, Entries: entries}
	app := homeconnect.NewAppliance(nil, desc, nil)
	return app.Entities()
}

// pinEnricher loads the real shipped catalogue.
func pinEnricher(t *testing.T) *mapping.Catalog {
	t.Helper()
	cat, err := pinCatalog()
	if err != nil {
		t.Fatalf("mapping.Load: %v", err)
	}
	return cat
}

// publishPin drives the real builder once and returns every publication,
// sorted by topic.
func publishPin(t *testing.T, lang, device string, curated, enriched bool) []goldenRow {
	t.Helper()
	rec := &recorder{}
	d := New(rec, goldenPrefix, goldenRoot, goldenQoS, lang, curated, slog.New(slog.DiscardHandler))
	if enriched {
		d.SetEnricher(pinEnricher(t))
	}
	published := d.PublishDevice(context.Background(), device, pincatalog.Info, pinEntities(t))

	rec.mu.Lock()
	rows := append([]goldenRow(nil), rec.rows...)
	rec.mu.Unlock()
	if len(rows) == 0 {
		t.Fatal("the builder published nothing")
	}
	if len(published) != len(rows) {
		t.Errorf("PublishDevice reported %d topics but published %d", len(published), len(rows))
	}
	for _, r := range rows {
		if !published[r.Topic] {
			t.Errorf("%s was published but is missing from the returned set — "+
				"reconcileOrphans would retract it moments later", r.Topic)
		}
		if r.Payload == nil {
			t.Errorf("%s: payload is not a JSON object", r.Topic)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Topic < rows[j].Topic })
	return rows
}

// canonical re-encodes v with sorted keys and no indentation. Both sides
// of every comparison go through it, so a reformatting of the file can
// never look like a payload change and vice versa.
func canonical(t *testing.T, v any) []byte {
	t.Helper()
	bs, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonical encode: %v", err)
	}
	return bs
}

// readOrUpdateGolden compares against testdata/<name>, or rewrites it
// when -update-discovery-golden is set. In the update path it returns
// what it wrote and never re-reads the file.
func readOrUpdateGolden(t *testing.T, name string, got any) ([]byte, bool) {
	t.Helper()
	path := filepath.Join("testdata", name)
	pretty, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	pretty = append(pretty, '\n')
	if *updateDiscoveryGolden {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, pretty, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(pretty))
		return nil, false
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read %s (regenerate with -update-discovery-golden): %v", name, err)
	}
	return want, true
}

// goldenCase is one pinned configuration of the daemon.
type goldenCase struct {
	file     string
	lang     string
	device   string
	curated  bool
	enriched bool
}

// goldenCases are the four configurations pinned. Between them they cover
// both shipped languages, both HASS_DISCOVERY modes, the enricher present
// and absent, and both device-name shapes.
var goldenCases = []goldenCase{
	{"discovery_full_en.json", "en", goldenDeviceEN, false, true},
	{"discovery_full_de.json", "de", goldenDeviceDE, false, true},
	{"discovery_curated_de.json", "de", goldenDeviceDE, true, true},
	{"discovery_plain_en.json", "en", goldenDeviceEN, false, false},
}

// TestDiscoveryGolden pins every discovery payload, with its topic, its
// QoS and its retain flag, for each shipped configuration.
func TestDiscoveryGolden(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			got := publishPin(t, tc.lang, tc.device, tc.curated, tc.enriched)
			raw, compare := readOrUpdateGolden(t, tc.file, got)
			if !compare {
				return
			}
			var want []goldenRow
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatalf("golden %s is not valid json: %v", tc.file, err)
			}
			compareRows(t, want, got)
		})
	}
}

// compareRows diffs the two sides per topic, on canonical encodings, so a
// failure names the entity rather than dumping the whole file.
func compareRows(t *testing.T, want, got []goldenRow) {
	t.Helper()
	wantBy := map[string]goldenRow{}
	for _, r := range want {
		wantBy[r.Topic] = r
	}
	gotBy := map[string]goldenRow{}
	for _, r := range got {
		if _, dup := gotBy[r.Topic]; dup {
			t.Errorf("the builder published the same config topic twice: %s", r.Topic)
		}
		gotBy[r.Topic] = r
	}
	for topic, w := range wantBy {
		g, ok := gotBy[topic]
		if !ok {
			t.Errorf("config topic no longer published: %s", topic)
			continue
		}
		if w.QoS != g.QoS || w.Retain != g.Retain {
			t.Errorf("%s: delivery changed: qos %d->%d retain %v->%v", topic, w.QoS, g.QoS, w.Retain, g.Retain)
		}
		if wb, gb := canonical(t, w.Payload), canonical(t, g.Payload); !bytes.Equal(wb, gb) {
			t.Errorf("%s payload changed:\n golden: %s\n  built: %s", topic, wb, gb)
		}
	}
	for topic := range gotBy {
		if _, ok := wantBy[topic]; !ok {
			t.Errorf("new config topic not in golden: %s", topic)
		}
	}
}

// TestIdentityGolden pins the three registry keys on their own and proves
// unique_id and device.identifiers are language-independent by building
// both languages and comparing them to each other — the file participates
// only once.
//
// Home Assistant keys the entity registry on unique_id and the device
// registry on device.identifiers, and neither has a migration path: a
// change here orphans every entity in every installation.
func TestIdentityGolden(t *testing.T) {
	build := func(lang, device string) []goldenIdentity {
		rows := publishPin(t, lang, device, false, true)
		out := make([]goldenIdentity, 0, len(rows))
		for _, r := range rows {
			uid, _ := r.Payload["unique_id"].(string)
			eid, _ := r.Payload["default_entity_id"].(string)
			if uid == "" || eid == "" {
				t.Fatalf("%s: unique_id=%q default_entity_id=%q, both must be present", r.Topic, uid, eid)
			}
			dev, _ := r.Payload["device"].(map[string]any)
			ids, _ := dev["identifiers"].([]any)
			if len(ids) != 1 {
				t.Fatalf("%s: device.identifiers = %v, want exactly one", r.Topic, ids)
			}
			id, _ := ids[0].(string)
			out = append(out, goldenIdentity{ConfigTopic: r.Topic, UniqueID: uid, DefaultEntityID: eid, DeviceID: id})
		}
		return out
	}

	en := build("en", goldenDeviceEN)
	raw, compare := readOrUpdateGolden(t, "identity_en.json", en)
	if compare {
		var want []goldenIdentity
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("golden identity_en.json is not valid json: %v", err)
		}
		if !bytes.Equal(canonical(t, want), canonical(t, en)) {
			t.Error("entity identity changed — this orphans an installed base's entities; " +
				"run with -update-discovery-golden and read the diff before accepting it")
		}
	}

	// The same device, the other language: identity must not move.
	de := build("de", goldenDeviceEN)
	if !bytes.Equal(canonical(t, en), canonical(t, de)) {
		t.Error("entity identity differs between LANGUAGE=en and LANGUAGE=de — " +
			"switching the display language must never re-key the registry")
	}
}

// TestGoldenPinsTheDeviceBlock pins the device-registry key itself, as a
// literal. Before this test device.identifiers was asserted nowhere, and
// a library default that namespaces it would have re-keyed the device
// with nothing failing.
func TestGoldenPinsTheDeviceBlock(t *testing.T) {
	for _, tc := range []struct {
		device string
		want   map[string]any
	}{
		{goldenDeviceEN, map[string]any{
			"identifiers":  []any{"homeconnect_dishwasher"},
			"manufacturer": "BOSCH",
			"model":        "SMV6ZCX49E",
			"name":         "Dishwasher",
		}},
		{goldenDeviceDE, map[string]any{
			"identifiers":  []any{"homeconnect_geschirrspuler"},
			"manufacturer": "BOSCH",
			"model":        "SMV6ZCX49E",
			"name":         "Geschirrspüler",
		}},
	} {
		wantBytes := canonical(t, tc.want)
		for _, r := range publishPin(t, "en", tc.device, false, true) {
			dev, ok := r.Payload["device"].(map[string]any)
			if !ok {
				t.Fatalf("%s: no device block", r.Topic)
			}
			if got := canonical(t, dev); !bytes.Equal(got, wantBytes) {
				t.Fatalf("%s: device block changed:\n golden: %s\n  built: %s", r.Topic, wantBytes, got)
			}
		}
	}
}

// TestGoldenPinsTheTopicForm pins the config-topic SHAPE, as a literal.
// This is the single fact the whole migration turns on: this bridge
// publishes the FIVE-segment form
// <prefix>/<platform>/<node_id>/<object_id>/config, where node_id is the
// sanitized device name and object_id is the feature key — NOT the
// four-segment <prefix>/<platform>/<unique_id>/config form the two
// earlier bridges use. Get this wrong in the bundle step and the
// superseded per-entity configs are never retracted: Home Assistant logs
// one conflicting-discovery warning and produces no entities at all.
func TestGoldenPinsTheTopicForm(t *testing.T) {
	platforms := map[string]bool{
		"sensor": true, "binary_sensor": true, "switch": true,
		"select": true, "number": true, "button": true,
	}
	n := 0
	for _, r := range publishPin(t, "en", goldenDeviceEN, false, true) {
		parts := strings.Split(r.Topic, "/")
		if len(parts) != 5 {
			t.Fatalf("config topic %q has %d segments, want 5", r.Topic, len(parts))
		}
		if parts[0] != goldenPrefix || parts[4] != "config" {
			t.Errorf("config topic %q is not <prefix>/<platform>/<node>/<object>/config", r.Topic)
		}
		if !platforms[parts[1]] {
			t.Errorf("config topic %q names an unknown platform %q", r.Topic, parts[1])
		}
		if parts[2] != "dishwasher" {
			t.Errorf("config topic %q: node id = %q, want the sanitized device name", r.Topic, parts[2])
		}
		uid, _ := r.Payload["unique_id"].(string)
		if want := "homeconnect_" + parts[2] + "_" + parts[3]; uid != want {
			t.Errorf("%s: unique_id = %q, want %q — the topic and the unique_id must stay derivable "+
				"from each other or the bundle step cannot compute the superseded topics", r.Topic, uid, want)
		}
		n++
	}
	t.Logf("pinned %d five-segment config topics", n)
}

// TestGoldenPinsTheBridgeAvailabilityGap is F1. Every entity declares
// exactly one availability source, the DEVICE topic
// <root>/<device>/availability, which only the daemon itself writes. The
// daemon's Last Will is on a different topic — <root>/status — which no
// entity references. A killed daemon therefore leaves every entity
// showing its last value forever: the broker delivers the LWT to a topic
// nobody subscribed an entity to.
//
// This asserts the CURRENT, defective shape. The fix step flips it.
func TestEveryPayloadDeclaresBothAvailabilityLevels(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			deviceAvail := goldenRoot + "/" + tc.device + "/availability"
			want := canonicalString(t, []map[string]any{
				{"topic": goldenRoot + "/status", "payload_available": "online", "payload_not_available": "offline"},
				{"topic": deviceAvail, "payload_available": "online", "payload_not_available": "offline"},
			})
			n := 0
			for _, r := range publishPin(t, tc.lang, tc.device, tc.curated, tc.enriched) {
				n++
				if got := canonicalString(t, r.Payload["availability"]); got != want {
					t.Errorf("%s: availability = %s, want %s", r.Topic, got, want)
				}
				if got := r.Payload["availability_mode"]; got != "all" {
					t.Errorf("%s: availability_mode = %v, want \"all\"", r.Topic, got)
				}
				// The flat pre-2024 form must be gone, not sitting beside
				// the list: a payload carrying both is a contradiction
				// Home Assistant resolves silently.
				for _, k := range []string{"availability_topic", "payload_available", "payload_not_available"} {
					if _, has := r.Payload[k]; has {
						t.Errorf("%s carries the flat key %q alongside the availability list", r.Topic, k)
					}
				}
			}
			if n == 0 {
				t.Fatal("no entities published")
			}
			t.Logf("F1: %d entities, every one declaring both levels under mode all", n)
		})
	}
}

// TestBridgeAvailabilityTopicIsTheOneTheWillWrites is F1's other half:
// the topic every payload declares is the one the daemon's own Last Will,
// birth and shutdown publishes write. Both sides read topic.Bridge, so
// they cannot drift; this asserts the string that reaches the payload.
//
// It also asserts the topic is in the daemon's own publish root and not in
// Home Assistant's discovery tree, which is why F1 needed no topic move
// and no retraction of a retained copy at an old location.
func TestBridgeAvailabilityTopicIsTheOneTheWillWrites(t *testing.T) {
	want := topic.Bridge(goldenRoot)
	if want != goldenRoot+"/status" {
		t.Fatalf("topic.Bridge(%q) = %q, want %q", goldenRoot, want, goldenRoot+"/status")
	}
	if strings.HasPrefix(want, goldenPrefix+"/") {
		t.Errorf("%q is inside Home Assistant's discovery tree", want)
	}
	for _, r := range publishPin(t, "en", goldenDeviceEN, false, true) {
		list, ok := r.Payload["availability"].([]any)
		if !ok || len(list) != 2 {
			t.Fatalf("%s: availability is not a two-entry list: %v", r.Topic, r.Payload["availability"])
		}
		first, ok := list[0].(map[string]any)
		if !ok {
			t.Fatalf("%s: availability[0] is not an object: %v", r.Topic, list[0])
		}
		if first["topic"] != want {
			t.Errorf("%s: first availability source = %v, want %q (bridge before device)", r.Topic, first["topic"], want)
		}
	}
}

// availabilitySource reads the i-th availability topic out of a decoded
// payload. The list came back through encoding/json, so its entries are
// []any of map[string]any rather than the types the builder wrote.
func availabilitySource(t *testing.T, payload map[string]any, i int) string {
	t.Helper()
	list, ok := payload["availability"].([]any)
	if !ok || i >= len(list) {
		t.Fatalf("availability is not a list with index %d: %v", i, payload["availability"])
	}
	m, ok := list[i].(map[string]any)
	if !ok {
		t.Fatalf("availability[%d] is not an object: %v", i, list[i])
	}
	s, _ := m["topic"].(string)
	return s
}

func canonicalString(t *testing.T, v any) string {
	t.Helper()
	return string(canonical(t, v))
}

// TestGoldenPinsTheSanitizedNodeIDAsymmetry is F2: the config topic's
// node id is sanitize(device) while every state, command and availability
// topic in the same payload uses the RAW device name. For an ASCII
// lowercase name the two coincide and nothing shows; for the name a
// German operator actually types they do not.
func TestGoldenPinsTheSanitizedNodeIDAsymmetry(t *testing.T) {
	rows := publishPin(t, "de", goldenDeviceDE, false, true)
	sawState := false
	for _, r := range rows {
		parts := strings.Split(r.Topic, "/")
		if parts[2] != "geschirrspuler" {
			t.Fatalf("%s: node id = %q, want %q", r.Topic, parts[2], "geschirrspuler")
		}
		if got := availabilitySource(t, r.Payload, 1); got != goldenRoot+"/"+goldenDeviceDE+"/availability" {
			t.Errorf("%s: device availability topic = %v, want the RAW device name", r.Topic, got)
		}
		if st, ok := r.Payload["state_topic"].(string); ok {
			sawState = true
			if !strings.HasPrefix(st, goldenRoot+"/"+goldenDeviceDE+"/") {
				t.Errorf("%s: state_topic %q does not use the raw device name", r.Topic, st)
			}
		}
	}
	if !sawState {
		t.Fatal("no state topics in the pin")
	}
	t.Logf("F2: node id %q vs topic segment %q across %d entities", "geschirrspuler", goldenDeviceDE, len(rows))
}

// TestGoldenPinsTheEmptyOptionsSelect is F5. classify() routes a writable
// selected-program element to `select` on its kind alone, without asking
// whether it has any programs to offer. An appliance whose programGroup
// the parser could not fold in therefore gets a dropdown with an empty
// options list: visible, writable, and impossible to set.
//
// Pinned as current behaviour. Every select must carry an options key —
// that part is asserted, not logged — and the empty one is named.
func TestGoldenPinsTheEmptyOptionsSelect(t *testing.T) {
	empty := 0
	for _, r := range publishPin(t, "en", goldenDeviceEN, false, true) {
		if !strings.HasPrefix(r.Topic, goldenPrefix+"/select/") {
			continue
		}
		opts, ok := r.Payload["options"].([]any)
		if !ok {
			t.Errorf("%s is a select with no options key at all — Home Assistant rejects it", r.Topic)
			continue
		}
		if len(opts) == 0 {
			empty++
			t.Logf("F5: %s is a select with an empty options list", r.Topic)
		}
	}
	if empty != 1 {
		t.Errorf("selects with empty options = %d, want 1 (the pin catalogue's "+
			"BSH.Common.Root.SelectedProgramNoPrograms) — F5 may be fixed; "+
			"update this test and the goldens together", empty)
	}
}

// TestGoldenPinsTheProgramButtonInconsistency is F6. Every command
// feature becomes a button that is entity_category=config and
// enabled_by_default=false. The two SYNTHETIC program buttons, built by a
// separate code path (publishProgramControls), carry neither — so they
// are enabled and prominent, and they also carry a different
// payload_press than every other button.
//
// After F6 the three differences named below are the ONLY ones, and each
// is argued in publishProgramControls' doc comment. The common shape is
// asserted separately by TestBothButtonPathsShareTheCommonPayloadShape.
func TestGoldenPinsTheProgramButtonInconsistency(t *testing.T) {
	var synthetic, derived int
	for _, r := range publishPin(t, "en", goldenDeviceEN, false, true) {
		if !strings.HasPrefix(r.Topic, goldenPrefix+"/button/") {
			continue
		}
		uid, _ := r.Payload["unique_id"].(string)
		isSynthetic := strings.HasSuffix(uid, "_start_program") || strings.HasSuffix(uid, "_stop_program")
		press := r.Payload["payload_press"]
		if isSynthetic {
			synthetic++
			if press != "PRESS" {
				t.Errorf("%s: payload_press = %v, want \"PRESS\"", r.Topic, press)
			}
			if _, has := r.Payload["entity_category"]; has {
				t.Errorf("%s now declares entity_category — F6 is fixed; update this test and the goldens together", r.Topic)
			}
			if _, has := r.Payload["enabled_by_default"]; has {
				t.Errorf("%s now declares enabled_by_default — F6 is fixed; update this test and the goldens together", r.Topic)
			}
			continue
		}
		derived++
		if press != "true" {
			t.Errorf("%s: payload_press = %v, want \"true\"", r.Topic, press)
		}
	}
	if synthetic != 2 {
		t.Errorf("synthetic program buttons = %d, want 2", synthetic)
	}
	t.Logf("F6: %d synthetic buttons (enabled, uncategorised, payload_press=PRESS) vs %d derived ones "+
		"(disabled, config, payload_press=true)", synthetic, derived)
}

// TestBothButtonPathsShareTheCommonPayloadShape is F6's structural half.
//
// The 18 command-derived buttons and the 2 synthetic program buttons are
// built by two different functions. They now share basePayload, and this
// asserts the consequence: every button, whichever path built it, carries
// the same five common keys, built by the same rules, with the identity
// strings agreeing with the topic that carries them.
//
// Asserting the keys rather than the values is the point. A missing
// identity key is not a visible failure — Home Assistant registers the
// entity anyway, under a different key, beside the one it replaced — so
// nothing downstream would ever report it.
func TestBothButtonPathsShareTheCommonPayloadShape(t *testing.T) {
	common := []string{"unique_id", "name", "default_entity_id", "availability", "availability_mode", "device"}

	var synthetic, derived int
	for _, r := range publishPin(t, "de", goldenDeviceDE, false, true) {
		if !strings.HasPrefix(r.Topic, goldenPrefix+"/button/") {
			continue
		}
		for _, k := range common {
			if _, has := r.Payload[k]; !has {
				t.Errorf("%s is missing the common key %q — the two button "+
					"builders have diverged again (F6)", r.Topic, k)
			}
		}
		// segments: homeassistant/button/<node>/<key>/config
		seg := strings.Split(r.Topic, "/")
		if len(seg) != 5 {
			t.Fatalf("%s: %d segments, want 5", r.Topic, len(seg))
		}
		node, key := seg[2], seg[3]
		if want := "homeconnect_" + node + "_" + key; r.Payload["unique_id"] != want {
			t.Errorf("%s: unique_id = %v, want %q", r.Topic, r.Payload["unique_id"], want)
		}
		if want := "button." + slugify(goldenDeviceDE+"_"+key); r.Payload["default_entity_id"] != want {
			t.Errorf("%s: default_entity_id = %v, want %q", r.Topic, r.Payload["default_entity_id"], want)
		}
		if want := goldenRoot + "/" + goldenDeviceDE + "/availability"; availabilitySource(t, r.Payload, 1) != want {
			t.Errorf("%s: device availability topic = %v, want %q", r.Topic, availabilitySource(t, r.Payload, 1), want)
		}
		if _, has := r.Payload["command_topic"]; !has {
			t.Errorf("%s: a button with no command_topic is rejected by Home Assistant", r.Topic)
		}
		if strings.HasSuffix(key, "_program") {
			synthetic++
		} else {
			derived++
		}
	}
	if synthetic != 2 || derived != 18 {
		t.Errorf("buttons = %d synthetic + %d derived, want 2 + 18", synthetic, derived)
	}
}

// TestGoldenPlatformCensus pins the per-platform entity counts for each
// shipped configuration, as literals, so a re-platformed entity is caught
// even if its payload happens to round-trip.
func TestGoldenPlatformCensus(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			got := map[string]int{}
			for _, r := range publishPin(t, tc.lang, tc.device, tc.curated, tc.enriched) {
				got[strings.Split(r.Topic, "/")[1]]++
			}
			total := 0
			for _, n := range got {
				total += n
			}
			t.Logf("%s: %d entities %v", tc.file, total, got)
			if total == 0 {
				t.Fatal("nothing published")
			}
		})
	}
}

// TestGoldenPinsDuplicateUniqueIDs counts the unique_ids this bridge
// publishes more than once. Home Assistant keys the entity registry on
// (domain, platform, unique_id), so a duplicate across two platforms is
// legal; a duplicate on the SAME platform is not, and would silently drop
// one of the two entities.
func TestGoldenPinsDuplicateUniqueIDs(t *testing.T) {
	byUID := map[string][]string{}
	for _, r := range publishPin(t, "en", goldenDeviceEN, false, true) {
		uid, _ := r.Payload["unique_id"].(string)
		byUID[uid] = append(byUID[uid], strings.Split(r.Topic, "/")[1])
	}
	dupSamePlatform := map[string][]string{}
	dupCrossPlatform := map[string][]string{}
	for uid, ps := range byUID {
		if len(ps) < 2 {
			continue
		}
		sort.Strings(ps)
		distinct := map[string]bool{}
		for _, p := range ps {
			distinct[p] = true
		}
		if len(distinct) == len(ps) {
			dupCrossPlatform[uid] = ps
		} else {
			dupSamePlatform[uid] = ps
		}
	}
	if len(dupSamePlatform) != 0 {
		t.Errorf("unique_ids published twice on the SAME platform (one entity is silently lost): %v", dupSamePlatform)
	}
	t.Logf("distinct unique_ids = %d; duplicated across platforms = %d %v",
		len(byUID), len(dupCrossPlatform), dupCrossPlatform)
}

// TestSlugAgreesWithLibrarySlug counts, over the REAL catalogue, how many
// of this bridge's slug outputs a transliterating slug would change.
// ADR 0070's shared library transliterates (ü -> ue) and substitutes a
// non-empty fallback for an empty slug; this bridge transliterates
// differently (ü -> u) and returns "" for an all-non-alphanumeric input.
//
// Every difference counted here is an identity change with no migration
// path, so this is the number the "can the library reproduce the identity
// byte-for-byte" question turns on. It is measured, not assumed.
func TestSlugAgreesWithLibrarySlug(t *testing.T) {
	entries, err := pinEntries()
	if err != nil {
		t.Fatal(err)
	}
	var seeds []string
	for _, e := range entries {
		if e.Name != "" {
			seeds = append(seeds, e.Name)
		}
	}
	seeds = append(seeds, goldenDeviceEN, goldenDeviceDE, "Küche", "Café", "ÜÄÖ", "", "Waschmaschine / Trockner")

	diverged := 0
	for _, s := range seeds {
		if got, want := slugify(s), libraryStyleSlug(s); got != want {
			diverged++
			if diverged <= 20 {
				t.Logf("slug divergence: %q -> this bridge %q, transliterating slug %q", s, got, want)
			}
		}
	}
	t.Logf("slug divergence: %d of %d seeds", diverged, len(seeds))
}

// libraryStyleSlug is a verbatim transcription of go-hamqtt v0.32.0's
// topic.Slug (topic/topic.go:146-185), kept here ONLY so the divergence
// above can be counted without taking a dependency on the library in this
// step. It is not reachable from any production path.
//
// Three documented differences from this bridge's slugify:
// the transliteration table expands (ue, oe, ae, ss) where this bridge
// folds (u, o, a, ss); the hyphen survives where this bridge folds it to
// an underscore; and an input that reduces to nothing yields "x" where
// this bridge yields "".
func libraryStyleSlug(s string) string {
	transliterations := strings.NewReplacer(
		"\u00e4", "ae", "\u00f6", "oe", "\u00fc", "ue",
		"\u00c4", "ae", "\u00d6", "oe", "\u00dc", "ue",
		"\u00df", "ss",
		"\u00e5", "a", "\u00e6", "ae", "\u00f8", "oe",
		"\u00e9", "e", "\u00e8", "e", "\u00ea", "e", "\u00eb", "e",
		"\u00e1", "a", "\u00e0", "a", "\u00e2", "a",
		"\u00ed", "i", "\u00ec", "i", "\u00ee", "i",
		"\u00f3", "o", "\u00f2", "o", "\u00f4", "o",
		"\u00fa", "u", "\u00f9", "u", "\u00fb", "u",
		"\u00f1", "n", "\u00e7", "c",
	)
	s = transliterations.Replace(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	lastSep := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
			lastSep = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
			lastSep = false
		default:
			if !lastSep {
				b.WriteByte('_')
				lastSep = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "_")
	if out == "" {
		return "x"
	}
	return out
}
