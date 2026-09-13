// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"fmt"
	"log/slog"
	"strings"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"
	hatopic "github.com/SukramJ/go-hamqtt/topic"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// This file was ADR 0070 phase 7, step 4: a SECOND rendering path, built on
// github.com/SukramJ/go-hamqtt, that produces the same bytes the hand-built
// map[string]any path in payload.go and discovery.go produces — and
// published none of them. At step 6 one of its two entry points became the
// production one: BundleFor renders the device document this daemon now
// publishes, and hamqttComponents stays what it always was, the per-entity
// rendering nothing publishes and everything is compared against.
//
// That division is the point rather than a leftover. The five pinned files
// in testdata/ record the per-entity form, they are READ and never
// regenerated — the update flags are not reachable from the tests that use
// them — and TestTheDocumentsComponentsArePinnedPayloads compares every
// component of every document against them, under two enumerated
// differences. So the artefact that ships is proved equal, entity by
// entity, to bytes written before this library existed.
//
// What is being proved, and what is not. The library replaces the
// RENDERING — turning a description into Home Assistant's JSON keys and
// into topics — not the CLASSIFICATION. classify, deviceClassAndUnit,
// stateClassFor, entityCategoryFor, enabledByDefault, humanize, enumOptions
// and the enrichment chain are this bridge's own domain logic over the Home
// Connect profile, and they stay exactly where they are; the functions below
// project their results onto a model.Description and let go-hamqtt render it.
//
// The one place that logic is mirrored rather than reused is
// sanitizeForPlatform, because it edits a finished map and the library edits
// a Description before there is a map. sanitizeDescriptionForPlatform is a
// transcription of it, clause for clause, and the 687 x 4 byte comparison is
// what proves the transcription faithful: an enum sensor, a select, a
// catalogue-supplied device class on a binary sensor and a unit on a switch
// all pass through it in the pin.

// hamqttOriginName is the origin block's name. It exists only so
// discovery.Validate has the field it requires on a bundle; the per-entity
// form this bridge publishes today carries no origin block, and
// RenderComponent only attaches one when the name is non-empty — which is
// why hamqttComponents passes the zero Origin and only BundleFor passes
// this.
const hamqttOriginName = "go-homeconnect2mqtt"

// ---------------------------------------------------------------------------
// the layout
// ---------------------------------------------------------------------------

// Layout is this daemon's MQTT topic schema expressed as a
// go-hamqtt topic.Layout. Every method delegates to internal/layout, so the
// library path and the daemon's own publish path cannot disagree about a
// topic — that is F3's fix carried forward rather than re-implemented, and
// TestHamqttLayoutAgreesWithTheDaemonsOwnBuilders asserts it builder against
// builder rather than through the goldens.
//
// topic.Default is not usable here for two independent reasons, both
// measured in notes/adr0070-phase7-measurement.md §4:
//
//   - Default.Bridge() renders "<root>/bridge/status"; this daemon's bridge
//     status topic — the one its Last Will writes and every payload declares
//     as its first availability source — is "<root>/status".
//   - Default.State() renders "<root>/<address>/<bucket>/<path...>"; this
//     daemon renders "<root>/<raw device name>/<Dotted/Feature/Path>/state",
//     with no bucket segment, the ORIGINAL casing of the feature name, and
//     the RAW device name rather than the slug that is the device's address
//     (F2 — the config topic's node id is slugified and nothing else is).
//
// # Which parts of a Slot this Layout reads, and which it ignores
//
// It reads Scope[0] and Path. It ignores Address, Channel and Bucket, and
// that is deliberate rather than an oversight:
//
//   - Address is model.Device.UID(), i.e. "homeconnect_geschirrspuler" —
//     the identity string, which F2 measured to be a DIFFERENT string from
//     the topic segment "Geschirrspüler". A layout that read Address would
//     move every state, command and availability topic of a non-ASCII device
//     name and strand its retained state.
//   - Channel and Bucket have no counterpart in this tree at all: a Home
//     Connect feature is a dotted name of variable depth and nothing
//     sub-addresses it.
//
// Those three are therefore INERT here, and an inert field is a blind spot a
// golden can never see — a mutation to it changes nothing, so nothing fails.
// TestHamqttLayoutIgnoresAddressChannelAndBucket asserts the inertness
// explicitly, which is the only way to tell "deliberately ignored" from
// "silently dropped".
type Layout struct{ root string }

var _ hatopic.Layout = Layout{}

// NewLayout is the layout under the bridge root, e.g. "homeconnect".
//
// Exported because three call sites need the SAME value and a second
// construction is how they drift: the discovery renderer hands it to
// discovery.StdContext, publisher.Config takes it so the daemon's status
// topic is checkable against Layout.Bridge rather than free-form, and
// cmd/homeconnect2mqtt builds both.
func NewLayout(root string) Layout { return Layout{root: strings.TrimRight(root, "/")} }

// device resolves the appliance a slot belongs to. The raw operator device
// name travels in Scope[0]: it is the topic segment, not the identity, and
// model.Slot has no other field that means "the container this datapoint
// sits in".
func (l Layout) device(s model.Slot) layout.Device {
	name := ""
	if len(s.Scope) > 0 {
		name = s.Scope[0]
	}
	return layout.NewDevice(l.root, name)
}

// State implements topic.Layout.
func (l Layout) State(s model.Slot) string {
	return l.device(s).Base() + "/" + strings.Join(s.Path, "/") + "/state"
}

// Command implements topic.Layout.
func (l Layout) Command(s model.Slot) string {
	return l.device(s).Base() + "/" + strings.Join(s.Path, "/") + "/set"
}

// Availability implements topic.Layout.
func (l Layout) Availability(s model.Slot) string { return l.device(s).Availability() }

// Bridge implements topic.Layout.
func (l Layout) Bridge() string { return layout.Bridge(l.root) }

// ---------------------------------------------------------------------------
// the context
// ---------------------------------------------------------------------------

// hamqttContext supplies the three identity strings Home Assistant keys its
// registries on. All three override StdContext's defaults, and all three
// call this bridge's own slugify.
//
// That is F10, decided in writing at step 2 and not reopened here: 0 of 686
// feature names differ between slugify and topic.Slug, but 5 of 7
// device-name probes do — "Geschirrspüler" slugs to "geschirrspuler" here
// and "geschirrspueler" there. The library's transliteration is the more
// correct one (it is what Home Assistant's own slugify does) and it is
// unusable, because the device slug is embedded in the node id, in
// device.identifiers[0] and in the device half of both unique_id and
// default_entity_id, and Home Assistant has a migration path for none of
// them.
type hamqttContext struct {
	discovery.StdContext
}

var _ discovery.Context = hamqttContext{}

// NodeID implements discovery.Context: the config topic's third segment,
// slugify(<device name>).
func (c hamqttContext) NodeID(dev *model.Device) string { return slugify(dev.Name.Default) }

// UniqueID implements discovery.Context: "<device identifier>_<entity key>",
// which is deviceBlockFor's id prefix plus featureKey.
func (c hamqttContext) UniqueID(dev *model.Device, e model.Entity) string {
	return dev.UID() + "_" + e.Key()
}

// ObjectID implements discovery.Context: the default_entity_id SEED, which
// the render pipeline prefixes with the platform. Note it is slugified over
// the joined string rather than over the two halves separately — slugify
// collapses runs of separators, so a device name ending in a non-alphanumeric
// character produces one underscore here and two under any per-half
// composition.
func (c hamqttContext) ObjectID(dev *model.Device, e model.Entity) string {
	return slugify(dev.Name.Default + "_" + e.Key())
}

// hamqttContext builds the render context for this Discovery's configuration.
//
// RawEncoding, not the default EnvelopeEncoding: this daemon publishes the
// bare value on a state topic, so no entity carries a value_template. The
// zero Encoding would attach one to all 667 entities that have a state topic.
func (d *Discovery) hamqttContext() hamqttContext {
	return hamqttContext{StdContext: discovery.StdContext{
		Layout: NewLayout(d.rootTopic),
		Lang:   d.lang,
		Enc:    discovery.RawEncoding,
	}}
}

// ---------------------------------------------------------------------------
// the model
// ---------------------------------------------------------------------------

// hamqttDevice is deviceBlockFor as a model.Device. The identifier is taken
// verbatim, with no model.Identifier.Namespace: a namespace would render
// "<ns>:homeconnect_geschirrspuler" and re-key the device registry.
func hamqttDevice(device string, info profile.DeviceInfo) *model.Device {
	modelName := info.Model
	if modelName == "" {
		modelName = info.Type
	}
	return &model.Device{
		Identity:     model.Identity{IDs: []model.Identifier{{Value: "homeconnect_" + sanitize(device)}}},
		Name:         model.L(device),
		Manufacturer: info.Brand,
		Model:        modelName,
	}
}

// hamqttSlot is one datapoint's coordinate. Scope carries the RAW device
// name because that is the topic segment (F2); Address carries the device's
// identity because model.Slot.Valid requires one and because
// discovery.DeviceSlot rebuilds it from the device when it resolves
// model.LevelDevice. The two are different strings on purpose.
func hamqttSlot(dev *model.Device, device string, path ...string) model.Slot {
	return model.Slot{
		Scope:   []string{device},
		Address: dev.UID(),
		Bucket:  model.BucketValues,
		Path:    path,
	}
}

// describe projects payloadFor's non-topic keys onto a model.Description.
//
// The three payload_* keys go to Extra because Home Assistant declares them
// per platform and go-hamqtt models neither: payload_on/payload_off are
// binary_sensor and switch vocabulary, payload_press is button vocabulary.
// Extra is validated like any other key, so this is not an escape from the
// schema check.
func (d *Discovery) describe(e *homeconnect.Entity, platform string) *model.Description {
	deviceClass, unit := deviceClassAndUnit(e)
	desc := &model.Description{
		Name:        model.L(humanize(e)),
		DeviceClass: model.DeviceClass(deviceClass),
		Unit:        model.Unit(unit),
		StateClass:  hacatalog.StateClass(stateClassFor(e, platform)),
		Category:    hacatalog.EntityCategory(entityCategoryFor(e)),
	}
	switch platform {
	case platformSwitch:
		desc.Extra = map[string]any{"payload_on": "true", "payload_off": "false"}
	case platformBinarySensor:
		if e.Desc.Kind == profile.KindEvent {
			desc.Extra = map[string]any{"payload_on": "Present", "payload_off": "Off"}
		} else {
			desc.Extra = map[string]any{"payload_on": "true", "payload_off": "false"}
		}
	case platformButton:
		desc.Extra = map[string]any{"payload_press": commandPressPayload}
	case platformSelect:
		desc.Options = &model.Enum{Codes: enumOptions(e)}
	case platformSensor:
		if e.Desc.IsEnum() {
			desc.Options = &model.Enum{Codes: enumOptions(e)}
		}
	case platformNumber:
		b := e.Bounds()
		if b.HasMin {
			desc.Min = model.Ptr(b.Min)
		}
		if b.HasMax {
			desc.Max = model.Ptr(b.Max)
		}
		if b.HasStep {
			desc.Step = model.Ptr(b.Step)
		}
	}
	// Home Assistant defaults to enabled; only the long tail says otherwise.
	if !enabledByDefault(e) {
		desc.Enabled = model.Ptr(false)
	}
	return desc
}

// enrichDescription is applyEnrichment against a Description instead of a map,
// including its refusal of a device_class the platform does not declare (F13).
func (d *Discovery) enrichDescription(e *homeconnect.Entity, desc *model.Description, platform string) {
	if d.enrich == nil || e.Name() == "" {
		return
	}
	f := e.Name()
	if name, ok := d.enrich.LocalizedName(f, d.lang); ok {
		desc.Name = model.L(name)
	}
	if dc, ok := d.enrich.DeviceClass(f); ok {
		if deviceClassAllowed(platform, dc) {
			desc.DeviceClass = model.DeviceClass(dc)
		} else {
			d.logRefusedDeviceClass(f, platform, dc)
		}
	}
	if unit, ok := d.enrich.Unit(f); ok {
		desc.Unit = model.Unit(unit)
	}
	if sc, ok := d.enrich.StateClass(f); ok {
		desc.StateClass = hacatalog.StateClass(sc)
	}
	if ec, ok := d.enrich.EntityCategory(f); ok {
		desc.Category = hacatalog.EntityCategory(ec)
	}
	if val, ok := d.enrich.EnabledByDefault(f); ok {
		if val {
			desc.Enabled = nil
		} else {
			desc.Enabled = model.Ptr(false)
		}
	}
}

// localizeDescriptionOptions is localizeOptions against a Description.
//
// The codes are replaced by their labels rather than carried as
// model.Enum.Labels, because this bridge publishes the localized string as
// the entity's STATE too (bridge/publish.go) — Home Assistant compares the
// state against the options list verbatim, so the two must be the same
// strings, and a label/code split would advertise the codes.
func (d *Discovery) localizeDescriptionOptions(desc *model.Description) {
	if desc.Options == nil {
		return
	}
	loc := make([]string, len(desc.Options.Codes))
	for i, o := range desc.Options.Codes {
		loc[i] = i18n.EnumLabel(o, d.lang)
	}
	sortLocalized(loc)
	desc.Options = &model.Enum{Codes: loc}
}

// sanitizeDescriptionForPlatform is sanitizeForPlatform, clause for clause,
// against a Description instead of a finished map. See the note at the top
// of this file for why it is transcribed rather than reused.
//
// One shape does not survive the transcription and it is worth naming: the
// old builder could emit `"options": []`, because it wrote the key
// unconditionally on a select, and discovery.Component omits an empty
// options list. F5 removed the only path that produced one (a writable
// selected-program element on an appliance exposing no programs is now a
// sensor), so the two agree today — TestNoSelectIsPublishedWithoutOptions is
// what keeps it that way.
func sanitizeDescriptionForPlatform(desc *model.Description, platform string) {
	if dc := string(desc.DeviceClass); dc != "" && !deviceClassAllowed(platform, dc) {
		desc.DeviceClass = ""
	}
	if platform != platformSensor && platform != platformNumber {
		desc.Unit = ""
	}
	if platform != platformSensor {
		desc.StateClass = ""
		return
	}
	if string(desc.DeviceClass) == deviceClassEnum {
		if desc.Options == nil {
			desc.DeviceClass = ""
			return
		}
		desc.Unit = ""
		desc.StateClass = ""
		return
	}
	desc.Options = nil
}

// hamqttModel builds the device and the entity set PublishDevice would
// publish for it, in the same order, under the same exclusion, curation and
// classification rules.
//
// The third return is how many entities the CURATED filter dropped, and it
// exists because that number is the cost of an add-on option nobody is
// told the price of. See [Discovery.warnCuratedOmissions].
func (d *Discovery) hamqttModel(device string, info profile.DeviceInfo, entities []*homeconnect.Entity) (*model.Device, []model.Entity, int) {
	dev := hamqttDevice(device, info)
	out := make([]model.Entity, 0, len(entities)+2)
	curated := 0
	for _, e := range entities {
		if d.enrich != nil && e.Name() != "" && d.enrich.Excluded(e.Name()) {
			continue
		}
		platform, ok := classify(e)
		if !ok {
			continue
		}
		desc := d.describe(e, platform)
		d.enrichDescription(e, desc, platform)
		d.localizeDescriptionOptions(desc)
		sanitizeDescriptionForPlatform(desc, platform)
		if d.curated && desc.Enabled != nil && !*desc.Enabled {
			curated++
			continue
		}
		slot := hamqttSlot(dev, device, strings.Split(layout.FeaturePath(e.Name(), e.UID()), "/")...)
		out = append(out, &model.Basic{
			EntityKey:      featureKey(e),
			EntityPlatform: hacatalog.Platform(platform),
			Description:    *desc,
			Binds:          hamqttBindings(e, platform, slot),
		})
	}
	return dev, append(out, d.hamqttProgramControls(device, dev, entities)...), curated
}

// warnCuratedOmissions says out loud what HASS_DISCOVERY: curated costs on
// an installation that has already published the full set.
//
// A device document does not remove a component by leaving it out, so
// flipping full -> curated does not shrink anything in Home Assistant: the
// 510 components (of 687, measured on the pin catalogue) the curated filter
// drops keep their retained per-entity registry entries, keep BOTH
// availability sources — the bridge status topic and the device
// availability topic, both of which this daemon goes on publishing — and
// keep RECEIVING LIVE STATE, because `curated` is read only in this package
// and the state plane never sees it. In Home Assistant they are
// indistinguishable from real entities. The operator who set the option to
// reduce clutter sees no change at all, which is the worst possible
// outcome for an option: it appears to do nothing, so it gets set again.
//
// The tombstone path that would actually remove them is still deferred (see
// TestOmittingAComponentDoesNotRemoveIt and the changelog), so this is the
// honest interim: quantify it, name it per appliance, and point at the
// documented manual remedy. Once per device per process — it is a statement
// about a configuration, not about a publish, and every (re)connect
// republishes.
func (d *Discovery) warnCuratedOmissions(device string, omitted, kept int) {
	if omitted == 0 {
		return
	}
	d.curatedMu.Lock()
	warned := d.curatedWarned[device]
	if d.curatedWarned == nil {
		d.curatedWarned = map[string]bool{}
	}
	d.curatedWarned[device] = true
	d.curatedMu.Unlock()
	if warned {
		return
	}
	d.logger.Warn("hass.curated_components_omitted",
		slog.String("device", device),
		slog.Int("omitted", omitted),
		slog.Int("published", kept),
		slog.String("consequence",
			"HASS_DISCOVERY: curated omits these components from the device document, and a "+
				"document does not delete a component by omitting it: any entity Home Assistant "+
				"already registered for them stays, stays available and keeps receiving state. "+
				"Remove them by hand — restart Home Assistant (or reload the MQTT integration) "+
				"first, then delete them on the device page (see addon/DOCS.md)"))
}

// hamqttBindings is payloadFor's topic decision expressed as bindings: a
// button is write-only and everything else reads, with a second writable
// binding only on the three platforms payloadFor advertises a command_topic
// for.
func hamqttBindings(e *homeconnect.Entity, platform string, slot model.Slot) []model.Binding {
	if platform == platformButton {
		return []model.Binding{{Role: model.RoleCommand, Slot: slot, Mode: model.Write}}
	}
	binds := []model.Binding{{Role: model.RoleState, Slot: slot, Mode: model.Read}}
	if e.Desc.Writable() && (platform == platformSwitch || platform == platformSelect || platform == platformNumber) {
		binds = append(binds, model.Binding{Role: model.RoleCommand, Slot: slot, Mode: model.Write})
	}
	return binds
}

// hamqttProgramControls is publishProgramControls as entities. The three
// deliberate differences from a command-derived button (F6) survive
// unchanged: payload_press "PRESS", no entity_category, no
// enabled_by_default.
func (d *Discovery) hamqttProgramControls(device string, dev *model.Device, entities []*homeconnect.Entity) []model.Entity {
	hasProgram := false
	for _, e := range entities {
		if e.Desc.Kind == profile.KindActiveProgram || e.Desc.Kind == profile.KindSelectedProgram {
			hasProgram = true
			break
		}
	}
	if !hasProgram {
		return nil
	}
	controls := []struct{ key, nameEN, nameDE string }{
		{layout.ControlStartProgram, "Start program", "Programm starten"},
		{layout.ControlStopProgram, "Stop program", "Programm stoppen"},
	}
	out := make([]model.Entity, 0, len(controls))
	for _, c := range controls {
		name := c.nameEN
		if d.lang == "de" {
			name = c.nameDE
		}
		desc := &model.Description{
			Name:  model.L(name),
			Extra: map[string]any{"payload_press": controlPressPayload},
		}
		sanitizeDescriptionForPlatform(desc, platformButton)
		slot := hamqttSlot(dev, device, strings.Split(layout.ControlPath(c.key), "/")...)
		out = append(out, &model.Basic{
			EntityKey:      c.key,
			EntityPlatform: hacatalog.Platform(platformButton),
			Description:    *desc,
			Binds:          []model.Binding{{Role: model.RoleCommand, Slot: slot, Mode: model.Write}},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

// hamqttRow is one rendered per-entity discovery config: exactly what
// PublishDevice would hand the MQTT client, and handed to nobody.
type hamqttRow struct {
	Topic    string
	Platform string
	Payload  []byte
	Comp     discovery.Component
}

// hamqttComponents renders the per-entity form — the form this bridge's
// installed fleet WAS on, and which step 6 retracts — for every entity of
// one device. Nothing publishes it: it exists so the document's components
// can be compared against the pins entity by entity.
//
// The topic is built by publisher.EntityConfigTopic, the library's own
// renderer for the five-segment form, from the library's own NodeID. That is
// deliberate: configTopic in discovery.go composes the same string by hand,
// and the whole point of step 4 is to find out whether the library agrees
// with it rather than to assume so.
func (d *Discovery) hamqttComponents(device string, info profile.DeviceInfo, entities []*homeconnect.Entity) ([]hamqttRow, error) {
	dev, ents, _ := d.hamqttModel(device, info, entities)
	ctx := d.hamqttContext()
	nodeID := ctx.NodeID(dev)
	rows := make([]hamqttRow, 0, len(ents))
	for _, e := range ents {
		comp, err := discovery.RenderComponent(ctx, dev, e, discovery.Origin{})
		if err != nil {
			return nil, fmt.Errorf("render %q: %w", e.Key(), err)
		}
		body, err := comp.EntityJSON()
		if err != nil {
			return nil, fmt.Errorf("encode %q: %w", e.Key(), err)
		}
		rows = append(rows, hamqttRow{
			Topic:    publisher.EntityConfigTopic(d.baseTopic, string(e.Platform()), nodeID, e.Key()),
			Platform: string(e.Platform()),
			Payload:  body,
			Comp:     comp,
		})
	}
	return rows, nil
}

// BundleFor renders one appliance's device document: the same entities
// hamqttComponents renders per-entity, addressed as one retained document
// at <prefix>/device/<node id>/config instead of 687 topics.
//
// It shares hamqttModel with the per-entity path rather than deriving the
// component set a second time, which is what makes the byte-equality proof
// of step 4 carry over: the components inside this document are the
// payloads pinned in testdata/, minus the four keys a document hoists to
// its own level (`device`, `origin`, `availability`, `availability_mode`)
// and plus the `platform` the per-entity form carried in its topic.
// TestTheDocumentsComponentsAreThePinnedPayloads is what asserts that
// rather than stating it.
func (d *Discovery) BundleFor(device string, info profile.DeviceInfo, entities []*homeconnect.Entity) (*discovery.Bundle, error) {
	dev, ents, omitted := d.hamqttModel(device, info, entities)
	d.warnCuratedOmissions(device, omitted, len(ents))
	b, err := discovery.Render(d.hamqttContext(), dev, ents, discovery.Origin{Name: hamqttOriginName})
	if err != nil {
		return nil, fmt.Errorf("render bundle: %w", err)
	}
	return b, nil
}
