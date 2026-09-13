// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
)

// The device-document form, as an artefact.
//
// Step 4 proved go-hamqtt reproduces this bridge's 2 238 pinned per-entity
// payloads byte for byte. This file carries that proof across to the form
// that is actually published from now on, and it does it against the SAME
// five pinned files: the goldens are read, never regenerated, and the
// update flags are not reachable from here.

// bundleRecorder is a ConfigWriter that records the documents and configs
// handed to it, and can refuse one.
type bundleRecorder struct {
	mu      sync.Mutex
	bundles []*discovery.Bundle
	configs []string
	err     error
}

func (r *bundleRecorder) Publish(_ context.Context, topic string, _ []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs = append(r.configs, topic)
	return true, nil
}

func (r *bundleRecorder) PublishBundle(_ context.Context, b *discovery.Bundle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return false, r.err
	}
	r.bundles = append(r.bundles, b)
	return true, nil
}

func (r *bundleRecorder) written() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bundles) + len(r.configs)
}

// TestTheDocumentsComponentsAreThePinnedPayloads carries step 4's
// byte-equality proof onto the form this step publishes.
//
// A component inside a device document and the per-entity config of the
// same entity differ in exactly two keys, and the whole value of this test
// is that the two are ENUMERATED rather than assumed:
//
//   - `device` is hoisted to the document, so the component drops it.
//   - `platform` moves out of the topic and into the entry, because a
//     document's components are not addressed by platform any more.
//
// Everything else — every topic, every `unique_id`, every
// `default_entity_id`, both availability sources, `availability_mode`,
// `options`, `device_class`, `payload_press` — must be byte-identical to
// the file pinned before the library existed. Those are the strings Home
// Assistant keys its registries on and has a migration path for none of;
// a document that moved one would not migrate a user's fleet, it would
// replace it.
//
// It runs over all four pinned configurations, which is 2 238 components.
func TestTheDocumentsComponentsArePinnedPayloads(t *testing.T) {
	total := 0
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			d := New(nil, goldenPrefix, goldenRoot, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
			if tc.enriched {
				d.SetEnricher(pinEnricher(t))
			}
			b, err := d.BundleFor(tc.device, pincatalog.Info, pinEntities(t))
			if err != nil {
				t.Fatalf("BundleFor: %v", err)
			}
			want := readGoldenRows(t, tc.file)
			if len(want) != len(b.Components) {
				t.Errorf("%s pins %d payloads, the document carries %d components",
					tc.file, len(want), len(b.Components))
			}
			for _, row := range want {
				platform, key := platformAndKeyOf(t, row.Topic)
				comp, ok := b.Components[key]
				if !ok {
					t.Errorf("%s is pinned and the document has no component %q", row.Topic, key)
					continue
				}
				if string(comp.Platform) != platform {
					t.Errorf("%s: component platform %q, topic says %q", key, comp.Platform, platform)
				}
				got := decodeComponent(t, comp)
				// The two enumerated differences, applied to the pin rather
				// than to the component: a key silently missing from the
				// component then still fails.
				expect := map[string]any{}
				for k, v := range row.Payload {
					if k == "device" {
						continue
					}
					expect[k] = v
				}
				expect["platform"] = platform
				compareJSON(t, key, expect, got)
			}
			total += len(b.Components)
		})
	}
	t.Logf("the device document reproduces %d pinned payloads across %d configurations",
		total, len(goldenCases))
}

// TestTheDocumentCarriesThePinnedDeviceBlock. `device` is hoisted out of
// every component into the document, which is the one key the comparison
// above deletes — so it is asserted here instead of dropped.
//
// `identifiers[0]` is the string Home Assistant keys the DEVICE registry
// on. If it moved, every entity would be re-parented and every automation,
// area assignment and dashboard card that names the device would break,
// with no migration path.
func TestTheDocumentCarriesThePinnedDeviceBlock(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(strings.TrimSuffix(tc.file, ".json"), func(t *testing.T) {
			d := New(nil, goldenPrefix, goldenRoot, tc.lang, tc.curated, slog.New(slog.DiscardHandler))
			if tc.enriched {
				d.SetEnricher(pinEnricher(t))
			}
			b, err := d.BundleFor(tc.device, pincatalog.Info, pinEntities(t))
			if err != nil {
				t.Fatalf("BundleFor: %v", err)
			}
			rows := readGoldenRows(t, tc.file)
			pinned, ok := rows[0].Payload["device"].(map[string]any)
			if !ok {
				t.Fatalf("%s row 0 carries no device block", tc.file)
			}
			raw, err := json.Marshal(b.Device)
			if err != nil {
				t.Fatalf("marshal device: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("device block: %v", err)
			}
			compareJSON(t, "device", pinned, got)
		})
	}
}

// platformAndKeyOf takes a pinned five-segment config topic apart.
// publisher.ParseConfigTopic is the library's own parser, so the test reads
// the topic the way the sweep does rather than by index arithmetic of its
// own.
func platformAndKeyOf(t *testing.T, topic string) (platform, key string) {
	t.Helper()
	parsed, ok := publisher.ParseConfigTopic(goldenPrefix, topic)
	if !ok || parsed.Bundle {
		t.Fatalf("%s is not a per-entity config topic", topic)
	}
	return parsed.Platform, parsed.ObjectID
}

func decodeComponent(t *testing.T, comp discovery.Component) map[string]any {
	t.Helper()
	raw, err := json.Marshal(comp)
	if err != nil {
		t.Fatalf("marshal component: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("component is not a JSON object: %v", err)
	}
	return out
}

func compareJSON(t *testing.T, what string, want, got map[string]any) {
	t.Helper()
	wj, _ := json.Marshal(canonicalJSON(want))
	gj, _ := json.Marshal(canonicalJSON(got))
	if !bytes.Equal(wj, gj) {
		t.Errorf("%s:\n  pinned: %s\n  document: %s", what, wj, gj)
	}
}

// canonicalJSON sorts map keys so two JSON objects with the same content
// compare equal whatever order they were written in.
func canonicalJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, 0, len(keys)*2)
		for _, k := range keys {
			out = append(out, k, canonicalJSON(t[k]))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = canonicalJSON(t[i])
		}
		return out
	default:
		return v
	}
}

// TestTheBundleTopicIsTheOneTheRendererAddresses. Two derivations of the
// same string, compared, because the string leaves this repository: it is
// the topic an operator types into a `mosquitto_pub -r -n` to roll back.
//
// The node id is the SLUG of the device name and not the name — the
// standing trap, and go-mtec2mqtt fell into it in three of the five places
// it documented the command.
func TestTheBundleTopicIsTheOneTheRendererAddresses(t *testing.T) {
	for _, device := range []string{"Geschirrspüler", "Dishwasher", "Wash Machine", "Herd/Backofen"} {
		d := New(nil, goldenPrefix, goldenRoot, "de", false, slog.New(slog.DiscardHandler))
		b, err := d.BundleFor(device, pincatalog.Info, pinEntities(t))
		if err != nil {
			t.Fatalf("BundleFor(%q): %v", device, err)
		}
		want := publisher.BundleConfigTopic(goldenPrefix, b.NodeID)
		if got := d.BundleTopic(device); got != want {
			t.Errorf("BundleTopic(%q) = %q, the renderer addresses %q", device, got, want)
		}
		if strings.Contains(want, device) && device != "Dishwasher" {
			t.Errorf("BundleTopic(%q) = %q carries the raw device name — the node id is its slug", device, want)
		}
	}
}

// documentedDowngradeTopic is the line every operator-facing file must
// carry, extracted rather than spelled: a `mosquitto_pub … -r -n` clearing
// a retained device document.
var documentedDowngradeTopic = regexp.MustCompile(`-t '([^']*/device/[^']*/config)'`)

// downgradeDocDevice is the appliance the documented example uses. It is a
// non-ASCII name on purpose: it is the only kind that makes the difference
// between the raw name, its lower case and its slug visible.
const downgradeDocDevice = "Geschirrspüler"

// mosquittoPubCommands is every mosquitto_pub invocation in raw, one entry
// per command, with shell line-continuations folded back into one line.
//
// The folding is the point: the documented command is wrapped across two
// lines in all four files, so "the line containing mosquitto_pub" holds the
// host and the credentials while the topic lives on the next one. A check
// that read only the first line would be as blind to a missing `-t` as a
// whole-file check is to a missing `-u`.
func mosquittoPubCommands(raw string) []string {
	var out []string
	lines := strings.Split(raw, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.Contains(lines[i], "mosquitto_pub") {
			continue
		}
		cmd := strings.TrimSpace(lines[i])
		for strings.HasSuffix(strings.TrimSpace(lines[i]), "\\") && i+1 < len(lines) {
			i++
			cmd = strings.TrimSuffix(strings.TrimSpace(cmd), "\\") + " " + strings.TrimSpace(lines[i])
		}
		out = append(out, cmd)
	}
	return out
}

// TestTheDocumentedDowngradeTopicMatchesTheCode is the pin for a string
// that leaves this repository and is executed by a human.
//
// A user who installs this release and then rolls back finds the device
// document retained, and Home Assistant's refusal is symmetric: the old
// release's per-entity configs are refused while the document stands, with
// one WARNING line and no entities. The documented remedy is one
// `mosquitto_pub -r -n`, and it is worth exactly as much as its topic is
// correct. go-mtec2mqtt documented that command in five places and had the
// topic wrong in three, because the node id is the SLUG of the device name,
// not the raw value and not merely its lower case.
//
// So the topic is not compared against a literal here. It is read out of
// every operator-facing file and compared against the string the publishing
// code actually addresses — and the files are compared against each other,
// because four copies that agree with the code today are four copies that
// can drift apart tomorrow.
func TestTheDocumentedDowngradeTopicMatchesTheCode(t *testing.T) {
	d := New(nil, goldenPrefix, goldenRoot, "de", false, slog.New(slog.DiscardHandler))
	want := d.BundleTopic(downgradeDocDevice)

	files := []string{"changelog.md", "addon/CHANGELOG.md", "README.md", "addon/DOCS.md"}
	for _, name := range files {
		path := filepath.Join("..", "..", name)
		raw, err := os.ReadFile(path) //nolint:gosec // fixed repository path
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		found := documentedDowngradeTopic.FindAllStringSubmatch(string(raw), -1)
		if len(found) == 0 {
			t.Errorf("%s documents no device-document retraction — a user who rolls back "+
				"after migrating has no way to get their entities back", name)
			continue
		}
		for _, m := range found {
			if m[1] != want {
				t.Errorf("%s documents the topic %q, the daemon publishes %q", name, m[1], want)
			}
		}
		// The add-on's broker is authenticated: script/run.sh takes the
		// username and password from the Supervisor MQTT service. A command
		// without credentials fails for every add-on user.
		//
		// Read out of the COMMAND, not out of the file. The previous
		// version asked whether the file contained "mosquitto_pub" and
		// whether it contained "-u " — two questions about one document
		// that are not the same question about one command line. Any
		// unrelated `-u ` anywhere in the same file (another example, a
		// prose mention, a second broker command) would have answered the
		// second one, and the credential could then go missing from the
		// command an operator actually pastes.
		cmds := mosquittoPubCommands(string(raw))
		if len(cmds) == 0 {
			t.Errorf("%s documents the retraction topic but no mosquitto_pub command to apply it with", name)
			continue
		}
		for _, cmd := range cmds {
			for _, flag := range []string{"-h ", "-u ", "-P "} {
				if !strings.Contains(cmd, flag) {
					t.Errorf("%s documents `%s` without %q — the add-on's broker is authenticated",
						name, cmd, flag)
				}
			}
		}
	}
}

// TestAnEmptyDocumentIsWithheld. A document with no components supersedes
// nothing and declares nothing, so publishing it would retract nothing and
// leave the whole per-entity fleet orphaned — and the state it describes,
// an appliance that classified to zero entities, is a fault upstream that
// the sweep would turn into a fleet-wide deletion on the next pass.
func TestAnEmptyDocumentIsWithheld(t *testing.T) {
	rec := &bundleRecorder{}
	d := New(rec, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))
	topic, err := d.PublishDeviceBundle(t.Context(), goldenDeviceEN, pincatalog.Info, nil)
	if err == nil {
		t.Fatal("an empty document was published")
	}
	// The error has to be THIS refusal, not any refusal, and not one whose
	// TEXT merely resembles it. discovery.Validate rejects an empty
	// document too, and its message is `bundle "x": bundle has no
	// components` — so a substring check passes with the guard removed, and
	// the gate would silently become the validator's: a different gate,
	// with a different reason, under no obligation to keep saying no. The
	// discriminator is the TYPE.
	var ve *discovery.ValidationError
	if errors.As(err, &ve) {
		t.Errorf("err = %v, and it came from discovery.Validate — this guard is being masked "+
			"by the validator rather than doing its own work", err)
	}
	if !strings.Contains(err.Error(), "device document for") {
		t.Errorf("err = %v, want the empty-document refusal", err)
	}
	if want := d.BundleTopic(goldenDeviceEN); topic != want {
		t.Errorf("topic = %q, want %q — the caller has to be able to name what was withheld", topic, want)
	}
	if rec.written() != 0 {
		t.Errorf("a withheld document still wrote %d messages", rec.written())
	}
}

// TestABlockingDocumentIsWithheld. Home Assistant validates a device
// document as one unit and drops the whole thing, so a single refused
// component costs the appliance every entity it has. That is what made F13
// a blocker for this step (#43): eleven silently-refused rows would have
// become 687 lost ones.
//
// The document is made blocking by a deliberately invalid component rather
// than by a mocked validator, so the test breaks if discovery.Validate's
// idea of "blocking" changes under it.
func TestABlockingDocumentIsWithheld(t *testing.T) {
	rec := &bundleRecorder{}
	d := New(rec, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))
	b, err := d.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t))
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	// Two components on the same platform sharing a unique_id: a real
	// registry collision, which Render passes through and Validate refuses.
	keys := b.Keys()
	first := b.Components[keys[0]]
	for _, k := range keys[1:] {
		if c := b.Components[k]; c.Platform == first.Platform {
			c.UniqueID = first.UniqueID
			b.Components[k] = c
			break
		}
	}
	var ve *discovery.ValidationError
	if !errors.As(discovery.Validate(b), &ve) || !ve.Blocking() {
		t.Fatal("the fixture is not blocking, so this test proves nothing")
	}
	topic, err := d.publishBundle(t.Context(), goldenDeviceEN, b)
	if err == nil {
		t.Fatal("a blocking document was published — Home Assistant drops such a document " +
			"whole, so that costs the appliance every entity it has")
	}
	if want := d.BundleTopic(goldenDeviceEN); topic != want {
		t.Errorf("topic = %q, want %q", topic, want)
	}
	if rec.written() != 0 {
		t.Errorf("a blocking document wrote %d messages", rec.written())
	}
}

// TestOmittingAComponentDoesNotRemoveIt records the one capability this
// step gives up, as a measurement rather than as a note.
//
// Under the per-entity form an entity that leaves this daemon's emit set is
// removed by the orphan sweep: its retained config is cleared and Home
// Assistant drops the entity. Under a device document, OMITTING a component
// does not remove anything — Home Assistant needs an entry that is present
// and carries a platform and nothing else — and
// publisher.SupersededTopics renders no retraction for a component that is
// not in the document, so the old per-entity config is not cleared either.
//
// The result is a stranded entity that reads AVAILABLE rather than
// unavailable, because its availability list names the bridge status topic
// and the device availability topic, and this daemon goes on publishing
// both.
//
// The triggers are operator-reachable on this bridge, which is why the
// deferral is argued in the PR body rather than assumed harmless: a feature
// excluded in mapping.yaml, HASS_DISCOVERY switched from `full` to
// `curated` (510 of 687 components on the pin fixture), an appliance
// replaced by one with a different feature list, and a firmware update that
// changes that list.
//
// This test is the record, and it asserts BOTH halves — that omission is
// inert and that the removal the library offers works — so the day
// tombstones are implemented it is the test that already describes them.
func TestOmittingAComponentDoesNotRemoveIt(t *testing.T) {
	d := New(nil, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))
	full, err := d.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t))
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	key := full.Keys()[0]
	was := full.Components[key]

	// Half one: the entity simply left the catalogue, so it is not rendered.
	omitted := &discovery.Bundle{
		NodeID: full.NodeID, Device: full.Device, Origin: full.Origin,
		Components: map[string]discovery.Component{},
	}
	for k, c := range full.Components {
		if k != key {
			omitted.Components[k] = c
		}
	}
	if _, present := omitted.Components[key]; present {
		t.Fatal("the fixture did not omit the component")
	}
	legacy := publisher.EntityConfigTopic(goldenPrefix, string(was.Platform), full.NodeID, key)
	if slices.Contains(publisher.SupersededTopics(goldenPrefix, omitted), legacy) {
		t.Errorf("an omitted component still retracts %s — if this is now true, omission "+
			"removes the entity and this finding is closed", legacy)
	}

	// Half two: the removal Home Assistant actually reads. The entry is
	// present, carries a platform, and carries NOTHING else — a `unique_id`
	// here would un-remove the entity the entry exists to remove.
	removed := &discovery.Bundle{
		NodeID: full.NodeID, Device: full.Device, Origin: full.Origin,
		Components: map[string]discovery.Component{},
	}
	for k, c := range omitted.Components {
		removed.Components[k] = c
	}
	removed.RemoveComponents(full.Components, key)
	entry, present := removed.Components[key]
	if !present {
		t.Fatal("RemoveComponents wrote no tombstone")
	}
	body, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal tombstone: %v", err)
	}
	if want := `{"platform":"` + string(was.Platform) + `"}`; string(body) != want {
		t.Errorf("tombstone = %s, want %s", body, want)
	}
	if entry.UniqueID != "" {
		t.Error("the tombstone carries a unique_id, which un-removes the entity")
	}
	if !slices.Contains(publisher.SupersededTopics(goldenPrefix, removed), legacy) {
		t.Errorf("a tombstoned component does not retract %s, so the stale per-entity config "+
			"would re-create the entity on every MQTT-integration restart", legacy)
	}
	// And the identity the retraction needs, kept outside the payload.
	if removed.Tombstones[key].UniqueID != was.UniqueID {
		t.Errorf("Bundle.Tombstones[%q].UniqueID = %q, want %q", key,
			removed.Tombstones[key].UniqueID, was.UniqueID)
	}
	_ = hacatalog.Platform("")
}

// TestCuratedOmissionsAreCountedAndSaidOutLoud quantifies the deferral,
// which is the half the deferral was missing.
//
// "An omitted component is not removed" understates what HASS_DISCOVERY:
// curated costs on an installation that has already published the full
// set. The trigger is not firmware and not a rare replacement: it is one
// add-on option, and flipping it leaves every component the curated filter
// drops behind in Home Assistant — with its retained registry entry, with
// BOTH availability sources still published `online`, and still receiving
// live state, because `curated` is read only in this package and the state
// plane never sees it. They are indistinguishable from real entities, so
// the operator who set the option to reduce clutter sees no change at all.
//
// The number is DERIVED from the two documents rather than written down:
// what the warning says must be exactly what the curated render dropped,
// which is what makes it a measurement instead of a claim. On the pin
// catalogue it is 510 of 687.
func TestCuratedOmissionsAreCountedAndSaidOutLoud(t *testing.T) {
	// The shipped catalogue on both sides: `enabled_by_default` is an
	// enrichment, so a Discovery without the enricher curates against a
	// different set than the daemon does and the number would be a
	// different number than the one an operator pays.
	fullD := New(nil, goldenPrefix, goldenRoot, "en", false, slog.New(slog.DiscardHandler))
	fullD.SetEnricher(pinEnricher(t))
	full, err := fullD.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t))
	if err != nil {
		t.Fatalf("BundleFor(full): %v", err)
	}

	var buf bytes.Buffer
	curated := New(nil, goldenPrefix, goldenRoot, "en", true,
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	curated.SetEnricher(pinEnricher(t))
	got, err := curated.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t))
	if err != nil {
		t.Fatalf("BundleFor(curated): %v", err)
	}

	dropped := len(full.Components) - len(got.Components)
	if dropped <= 0 {
		t.Fatalf("curated rendered %d components against full's %d — the fixture no longer "+
			"exercises the option this warning is about", len(got.Components), len(full.Components))
	}
	t.Logf("curated drops %d of %d components on the pin catalogue", dropped, len(full.Components))

	line := buf.String()
	if !strings.Contains(line, "hass.curated_components_omitted") {
		t.Fatalf("a curated render dropped %d components and said nothing: %q", dropped, line)
	}
	if want := fmt.Sprintf("omitted=%d", dropped); !strings.Contains(line, want) {
		t.Errorf("the warning does not carry %s — it reads %q", want, line)
	}
	if want := fmt.Sprintf("published=%d", len(got.Components)); !strings.Contains(line, want) {
		t.Errorf("the warning does not carry %s — it reads %q", want, line)
	}

	// Once per appliance per process: the condition is a configuration, and
	// the document is re-rendered on every (re)connect and every Home
	// Assistant restart. A warning that repeats per republish is a warning
	// an operator filters out.
	before := strings.Count(buf.String(), "hass.curated_components_omitted")
	if _, err := curated.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t)); err != nil {
		t.Fatalf("BundleFor(curated, again): %v", err)
	}
	if after := strings.Count(buf.String(), "hass.curated_components_omitted"); after != before {
		t.Errorf("the warning was repeated on a re-render: %d lines, want %d", after, before)
	}

	// And the full set says nothing: the warning is about the option, not
	// about every boot.
	var quiet bytes.Buffer
	quietD := New(nil, goldenPrefix, goldenRoot, "en", false,
		slog.New(slog.NewTextHandler(&quiet, &slog.HandlerOptions{Level: slog.LevelWarn})))
	quietD.SetEnricher(pinEnricher(t))
	if _, err := quietD.BundleFor(goldenDeviceEN, pincatalog.Info, pinEntities(t)); err != nil {
		t.Fatalf("BundleFor(full, again): %v", err)
	}
	if strings.Contains(quiet.String(), "hass.curated_components_omitted") {
		t.Errorf("HASS_DISCOVERY: full warned about omissions it did not make: %q", quiet.String())
	}
}
