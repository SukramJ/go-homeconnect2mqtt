// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
)

// This file drives the attribution rule over the whole shipped catalogue
// rather than over hand-written rows, because the defect it exists for was
// invisible to every hand-written row in the repository: a second instance
// rooted UNDER the first satisfies the topic-prefix test on every topic it
// names, so the outer instance claimed all 687 of the inner one's
// components and tombstoned the 510 that are live.
//
// A predicate table can state that `other_root/…` is somebody else's. It
// cannot state that `homeconnect/kitchen/…` is, because that string is one
// the old rule was written to accept.
//
// The payloads are the PINNED ones, not freshly rendered ones, and a
// sibling's are those re-rooted. That is a deliberate trade of render time
// for coverage — a render of 687 components costs over a second under the
// race detector, and this file would otherwise add a minute to the package
// — and it is not taken on trust: TestAReRootedPayloadIsWhatASiblingReally
// Publishes renders a real sibling instance and requires the re-rooting to
// reproduce it exactly.

// nestedSiblingRoot is a second instance of this daemon whose MQTT_TOPIC is
// a topic level UNDER ours. internal/config/validate.go requires only that
// MQTT_TOPIC be non-empty, so this is a legal, unremarkable configuration —
// and a tidier-looking one than two disjoint names.
const nestedSiblingRoot = goldenRoot + "/kitchen"

// disjointSiblingRoots are the roots the rule handled correctly before and
// must go on handling correctly. "homeconnect2" and "homeconnectx" are the
// two a STRING prefix (rather than a topic-level one) would get wrong in
// the other direction: they would be declined as ours.
var disjointSiblingRoots = []string{"homeconnect2", "hc", "homeconnectx", "home", "other_root"}

// reRooted is one instance's payload as the instance rooted at root would
// have rendered it.
//
// Every MQTT topic in a payload is `"<root>/…"`; no other string in one
// begins with `"homeconnect/`, because every identity string
// (`unique_id`, `identifiers`, `default_entity_id`) uses the `homeconnect_`
// underscore form — which is F8 restated: MQTT_TOPIC appears in none of
// them, and the topics are the only place two instances differ.
func reRooted(t *testing.T, raw []byte, root string) []byte {
	t.Helper()
	swapped := bytes.ReplaceAll(raw, []byte(`"`+goldenRoot+`/`), []byte(`"`+root+`/`))
	if root != goldenRoot && bytes.Equal(swapped, raw) {
		t.Fatalf("re-rooting to %q changed nothing: this payload names no topic under %q, so "+
			"it cannot say anything about attribution", root, goldenRoot)
	}
	return swapped
}

// rawPayloads is each pinned payload as the bytes it was published as,
// marshalled once: everything below is a byte substitution on these, so a
// configuration costs one encode per component rather than one per
// component per root.
func rawPayloads(t *testing.T, rows []goldenRow) [][]byte {
	t.Helper()
	out := make([][]byte, 0, len(rows))
	for i := range rows {
		raw, err := json.Marshal(rows[i].Payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", rows[i].Topic, err)
		}
		out = append(out, raw)
	}
	return out
}

func discoveryAt(t *testing.T, tc goldenCase, root string) *Discovery {
	t.Helper()
	d := New(nil, goldenPrefix, root, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
	if tc.enriched {
		d.SetEnricher(pinEnricher(t))
	}
	return d
}

// TestAReRootedPayloadIsWhatASiblingReallyPublishes is what makes the
// re-rooting above evidence rather than an assumption.
//
// One configuration is enough — the question is whether a payload's root is
// the only thing an instance's MQTT_TOPIC changes, which is a property of
// the renderer, not of the language or the curated filter — and one is what
// the render budget allows: this is the only real render in this file.
func TestAReRootedPayloadIsWhatASiblingReallyPublishes(t *testing.T) {
	tc := goldenCases[0]
	sibling := discoveryAt(t, tc, nestedSiblingRoot)
	rows, err := sibling.hamqttComponents(tc.device, pincatalog.Info, pinEntities(t))
	if err != nil {
		t.Fatalf("hamqttComponents at %q: %v", nestedSiblingRoot, err)
	}
	want := readGoldenRows(t, tc.file)
	if len(rows) != len(want) {
		t.Fatalf("the sibling rendered %d components, the pin holds %d", len(rows), len(want))
	}
	byKey := map[string]map[string]any{}
	for i := range rows {
		var body map[string]any
		if err := json.Unmarshal(rows[i].Payload, &body); err != nil {
			t.Fatalf("%s: %v", rows[i].Topic, err)
		}
		_, key := platformAndKeyOf(t, rows[i].Topic)
		byKey[key] = body
	}
	for _, row := range want {
		_, key := platformAndKeyOf(t, row.Topic)
		got, ok := byKey[key]
		if !ok {
			t.Fatalf("the sibling did not render %s", key)
		}
		var expect map[string]any
		if err := json.Unmarshal(reRooted(t, rawOf(t, row.Payload), nestedSiblingRoot), &expect); err != nil {
			t.Fatalf("%s: re-rooted pin is not JSON: %v", key, err)
		}
		compareJSON(t, key, expect, got)
	}
	t.Logf("%d components: re-rooting the pinned payload reproduces a real instance rooted at %q",
		len(want), nestedSiblingRoot)
}

// TestAttributionOverTheWholeCatalogue is the pin for the nested-sibling
// defect, over all four pinned configurations.
//
// Four claims, and the middle two are the finding:
//
//   - Ours are all ours. Every component this instance publishes is
//     claimed, in every configuration — the over-correction direction, and
//     the one that decides whether the orphan sweep still works at all.
//   - A nested sibling's are NONE of ours. This was 687 of 687 accepted,
//     and the 510 of them a curated instance does not declare were written
//     into the document as removals.
//   - The outer instance's are none of the nested one's. That direction was
//     already correct and is asserted separately, because the break was
//     asymmetric: only the ancestor ate the descendant.
//   - A disjoint sibling's are none of ours, which is what the rule already
//     did and must go on doing.
func TestAttributionOverTheWholeCatalogue(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			t.Parallel()
			ours := discoveryAt(t, tc, goldenRoot)
			nested := discoveryAt(t, tc, nestedSiblingRoot)
			rows := readGoldenRows(t, tc.file)
			if len(rows) == 0 {
				t.Fatal("the pin holds no payloads")
			}
			raws := rawPayloads(t, rows)
			mine, theirs, declinedByNested := 0, 0, 0
			for _, raw := range raws {
				if ours.IsOwnConfig(raw) {
					mine++
				}
				if ours.IsOwnConfig(reRooted(t, raw, nestedSiblingRoot)) {
					theirs++
				}
				if !nested.IsOwnConfig(raw) {
					declinedByNested++
				}
			}
			if mine != len(rows) {
				t.Errorf("we claim %d of our own %d components, want all of them", mine, len(rows))
			}
			if theirs != 0 {
				t.Errorf("nested sibling root %q: we claim %d of its %d components, want 0 "+
					"(this was 687 of 687, and the live ones were deleted out of Home Assistant)",
					nestedSiblingRoot, theirs, len(rows))
			}
			if declinedByNested != len(rows) {
				t.Errorf("the nested instance claims %d of our %d components, want 0",
					len(rows)-declinedByNested, len(rows))
			}
			// The disjoint roots are driven over ONE configuration. They
			// are the direction that already worked and must not regress,
			// and what decides them is the root string alone — not the
			// language, not the curated filter — so the other three
			// configurations would re-measure the same property at the
			// price of the render budget this package does not have.
			if tc.file != goldenCases[0].file {
				t.Logf("%d components: ours all claimed, nested sibling 0", len(rows))
				return
			}
			for _, root := range disjointSiblingRoots {
				claimed := 0
				for _, raw := range raws {
					if ours.IsOwnConfig(reRooted(t, raw, root)) {
						claimed++
					}
				}
				if claimed != 0 {
					t.Errorf("disjoint sibling root %q: we claim %d of its %d components, want 0",
						root, claimed, len(rows))
				}
			}
			t.Logf("%d components: ours all claimed, nested sibling 0, %d disjoint roots 0",
				len(rows), len(disjointSiblingRoots))
		})
	}
}

// TestTheReadBackDeclinesANestedSiblingsWholeDocument carries the same
// question onto the artefact the tombstone path actually reads, because
// that is where the cost is: a component the read-back accepts and the new
// render does not declare becomes a removal, and a removal for a live
// component deletes a working entity.
//
// The document is assembled from the pinned per-entity payloads by the two
// transformations TestTheDocumentsComponentsArePinnedPayloads enumerates
// and asserts against a real document — drop `device`, add `platform` —
// so it is the shape a document really has.
func TestTheReadBackDeclinesANestedSiblingsWholeDocument(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			t.Parallel()
			ours := discoveryAt(t, tc, goldenRoot)
			rows := readGoldenRows(t, tc.file)
			doc := documentOf(t, rows)
			own := ours.BundleComponents(doc)
			if len(own) != len(rows) {
				t.Fatalf("the read-back kept %d of our OWN %d components; nothing below can "+
					"fail meaningfully", len(own), len(rows))
			}
			if got := ours.BundleComponents(reRooted(t, doc, nestedSiblingRoot)); len(got) != 0 {
				t.Errorf("nested sibling root %q: the read-back kept %d of its components as "+
					"prior state, want 0 — each live one becomes a removal that deletes its entity",
					nestedSiblingRoot, len(got))
			}
			t.Logf("%d components read back from our own document, 0 from the nested sibling's",
				len(rows))
		})
	}
}

// documentOf is the retained device document this instance publishes for
// these pinned payloads. A sibling's is this one re-rooted, which is the
// same substitution applied to the whole artefact at once.
func documentOf(t *testing.T, rows []goldenRow) []byte {
	t.Helper()
	comps := map[string]map[string]any{}
	for _, row := range rows {
		platform, key := platformAndKeyOf(t, row.Topic)
		body := map[string]any{}
		for k, v := range row.Payload {
			if k == "device" {
				continue
			}
			body[k] = v
		}
		body["platform"] = platform
		comps[key] = body
	}
	raw, err := json.Marshal(map[string]any{"components": comps})
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return raw
}

// rawOf marshals one pinned payload.
func rawOf(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}
