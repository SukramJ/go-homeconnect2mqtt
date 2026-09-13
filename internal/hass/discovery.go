// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// ConfigWriter is the narrow slice of publisher.Runtime this package
// publishes retained discovery configs through.
//
// It is an interface declared here rather than the concrete type for two
// reasons. It keeps internal/hass — which renders — from importing the
// package that publishes, so the daemon can hold one runtime and hand the
// same one to the renderer, the state plane and the Last Will. And it
// takes no QoS and no retain flag, because neither is this package's to
// decide any more: a retained discovery config at the operator's MQTT_QOS
// is the runtime's policy, stated once in internal/haplane, rather than
// an argument every call site could get wrong.
//
// The bool reports whether the payload actually reached the broker. The
// runtime deduplicates against what it has already published, so a
// re-publish of an unchanged config writes nothing — on this bridge that
// is 687 retained writes per appliance per (re)connect that now cost one
// comparison each.
type ConfigWriter interface {
	Publish(ctx context.Context, topic string, payload []byte) (bool, error)

	// PublishBundle writes one appliance's retained device document,
	// retracting the per-entity configs it supersedes first and refusing
	// the whole migration if the broker would not accept a packet that
	// size. Both halves are haplane.Plane's; see there for why the refusal
	// has to happen before the retraction rather than after it.
	PublishBundle(ctx context.Context, b *discovery.Bundle) (bool, error)
}

// Enricher supplies operator-configured per-feature overrides (implemented by
// mapping.Catalog). Every lookup reports ok=false when nothing is configured,
// leaving the discovery heuristic in place.
type Enricher interface {
	LocalizedName(feature, lang string) (string, bool)
	DeviceClass(feature string) (string, bool)
	Unit(feature string) (string, bool)
	StateClass(feature string) (string, bool)
	EntityCategory(feature string) (string, bool)
	EnabledByDefault(feature string) (val, ok bool)
	Excluded(feature string) bool
}

// Discovery publishes Home Assistant MQTT discovery config payloads.
type Discovery struct {
	mqtt      ConfigWriter
	baseTopic string // discovery prefix, e.g. "homeassistant"
	rootTopic string // bridge MQTT root, e.g. "homeconnect"
	lang      string // display language for friendly names ("de"/"en")
	curated   bool   // publish only the enabled-by-default (primary) set
	logger    *slog.Logger
	enrich    Enricher

	// curatedWarned keeps the curated-omission warning to one line per
	// appliance per process: the condition is a configuration, and the
	// document is re-published on every broker (re)connect and every Home
	// Assistant restart. See Discovery.warnCuratedOmissions.
	curatedMu     sync.Mutex
	curatedWarned map[string]bool
}

// SetEnricher installs an optional enrichment source.
func (d *Discovery) SetEnricher(e Enricher) { d.enrich = e }

// New builds a Discovery publisher. lang selects the friendly-name language;
// curated restricts discovery to the enabled-by-default (primary) entities.
func New(pub ConfigWriter, baseTopic, rootTopic, lang string, curated bool, logger *slog.Logger) *Discovery {
	if logger == nil {
		logger = slog.Default()
	}
	return &Discovery{
		mqtt:      pub,
		baseTopic: strings.TrimRight(baseTopic, "/"),
		rootTopic: strings.TrimRight(rootTopic, "/"),
		lang:      lang,
		curated:   curated,
		logger:    logger,
	}
}

// BirthTopic is the Home Assistant status topic to watch; a payload of
// "online" means HA (re)started and discovery must be re-published.
//
// It is publisher.BirthTopic rather than a local concatenation on
// purpose. go-mtec2mqtt shipped `prefix + "/status"` against an operator
// prefix of "homeassistant/", which subscribes "homeassistant//status" —
// an empty MQTT level is legal and a DIFFERENT topic from the one Home
// Assistant announces on, so after every Home Assistant restart its
// entities were gone until the daemon itself restarted, with nothing in
// either log (its PR #54, finding F10). This daemon trims the prefix at
// construction too; the library call is the second lock.
func (d *Discovery) BirthTopic() string { return publisher.BirthTopic(d.baseTopic) }

type entityTopics struct {
	state        string
	command      string
	availability string
	// bridge is the daemon's own status topic — the one the Last Will
	// writes, and the one every payload declares alongside the device
	// topic (F1).
	bridge string
}

type deviceBlock struct {
	idPrefix string
	block    map[string]any
}

func (d *Discovery) topicsFor(device string, e *homeconnect.Entity) entityTopics {
	dt := layout.NewDevice(d.rootTopic, device)
	return entityTopics{
		state:        dt.State(e.Name(), e.UID()),
		command:      dt.Command(e.Name(), e.UID()),
		availability: dt.Availability(),
		bridge:       layout.Bridge(d.rootTopic),
	}
}

func (d *Discovery) deviceBlockFor(device string, info profile.DeviceInfo) deviceBlock {
	id := "homeconnect_" + sanitize(device)
	model := info.Model
	if model == "" {
		model = info.Type
	}
	return deviceBlock{
		idPrefix: id,
		block: map[string]any{
			"identifiers":  []string{id},
			"manufacturer": info.Brand,
			"model":        model,
			"name":         device,
		},
	}
}

// applyEnrichment localizes the friendly name and applies catalogue overrides
// on top of the heuristic payload.
//
// A device_class override is applied only when the platform the entity landed
// on declares it (F13). mapping.yaml is generated to mirror the official
// `home_connect` integration, where thirteen of its features are BINARY
// sensors; whenever an appliance models one of them as anything but a
// read-only boolean this bridge classifies it as a `sensor`, and `sensor`
// declares none of `door`, `plug`, `connectivity`, `light` or
// `battery_charging`. Home Assistant then discards the entity whole, during
// schema validation, with nothing on the wire and nothing in a log to say so.
//
// The refusal leaves the HEURISTIC class in place rather than clearing the
// key, which is the same contract an absent catalogue entry has: an override
// that cannot be published is an override that did not apply. That is also
// what keeps an enum sensor's `options`, since the heuristic class it falls
// back to is `enum` and sanitizeForPlatform keys the options branch on it.
//
// sanitizeForPlatform still runs afterwards and still filters the same pair.
// This is not a replacement for it — the heuristic can produce a class the
// platform refuses too — it is the point at which the daemon can still say
// WHICH override it dropped and why.
func (d *Discovery) applyEnrichment(e *homeconnect.Entity, payload map[string]any, platform string) {
	if d.enrich == nil || e.Name() == "" {
		return
	}
	f := e.Name()
	if name, ok := d.enrich.LocalizedName(f, d.lang); ok {
		payload["name"] = name
	}
	if dc, ok := d.enrich.DeviceClass(f); ok {
		if deviceClassAllowed(platform, dc) {
			payload["device_class"] = dc
		} else {
			d.logRefusedDeviceClass(f, platform, dc)
		}
	}
	if unit, ok := d.enrich.Unit(f); ok {
		payload["unit_of_measurement"] = unit
	}
	if sc, ok := d.enrich.StateClass(f); ok {
		payload["state_class"] = sc
	}
	if ec, ok := d.enrich.EntityCategory(f); ok {
		payload["entity_category"] = ec
	}
	if val, ok := d.enrich.EnabledByDefault(f); ok {
		if val {
			delete(payload, "enabled_by_default")
		} else {
			payload["enabled_by_default"] = false
		}
	}
}

// logRefusedDeviceClass names an operator override the target platform cannot
// carry. Home Assistant says nothing when it drops such an entity, which is
// the whole reason F13 survived unnoticed in the shipped catalogue; this is
// the one place the daemon knows both halves of the pair.
func (d *Discovery) logRefusedDeviceClass(feature, platform, dc string) {
	d.logger.Warn("hass.device_class_refused",
		slog.String("feature", feature),
		slog.String("platform", platform),
		slog.String("device_class", dc),
		slog.String("action", "override dropped, heuristic class kept"))
}

// localizeOptions translates a select's enum options to the configured display
// language so HA dropdown labels match the (also localized) published state,
// and orders them in that language. Uncatalogued values pass through
// unchanged, keeping options and state aligned.
func (d *Discovery) localizeOptions(payload map[string]any) {
	opts, ok := payload["options"].([]string)
	if !ok {
		return
	}
	loc := make([]string, len(opts))
	for i, o := range opts {
		loc[i] = i18n.EnumLabel(o, d.lang)
	}
	// Sort AFTER translating, not before. enumOptions sorts the raw
	// enumeration member names, which are English, so a German dropdown
	// used to come out ordered by its English originals: OperationState
	// read ["Auto", "Aus", "Ein"], i.e. Auto/Off/On (F11). The user sees
	// only the left column.
	sortLocalized(loc)
	payload["options"] = loc
}

func (d *Discovery) configTopic(platform, device string, e *homeconnect.Entity) string {
	return d.baseTopic + "/" + platform + "/" + sanitize(device) + "/" + featureKey(e) + "/config"
}

// disabledByDefault reports whether the built payload ends up disabled.
func disabledByDefault(payload map[string]any) bool {
	v, ok := payload["enabled_by_default"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return !b
}

// PublishDevice emits a discovery config for every exposable entity of a
// device. Errors are logged, never fatal (publish what you can). It returns
// the set of config topics it published, so the caller can clear orphaned ones.
func (d *Discovery) PublishDevice(ctx context.Context, device string, info profile.DeviceInfo, entities []*homeconnect.Entity) map[string]bool {
	dev := d.deviceBlockFor(device, info)
	published := map[string]bool{}
	for _, e := range entities {
		if d.enrich != nil && e.Name() != "" && d.enrich.Excluded(e.Name()) {
			continue
		}
		platform, ok := classify(e)
		if !ok {
			continue
		}
		payload := payloadFor(e, platform, device, d.topicsFor(device, e), dev)
		d.applyEnrichment(e, payload, platform)
		d.localizeOptions(payload)
		sanitizeForPlatform(payload, platform)
		// Curated mode: only publish the enabled-by-default (primary) entities.
		if d.curated && disabledByDefault(payload) {
			continue
		}
		b, err := json.Marshal(payload)
		if err != nil {
			d.logger.Warn("hass.marshal", slog.String("feature", e.Name()), slog.String("err", err.Error()))
			continue
		}
		topic := d.configTopic(platform, device, e)
		published[topic] = true
		if _, err := d.mqtt.Publish(ctx, topic, b); err != nil {
			d.logger.Warn("hass.publish", slog.String("topic", topic), slog.String("err", err.Error()))
		}
	}
	d.publishProgramControls(ctx, device, entities, dev, published)
	return published
}

// publishProgramControls emits synthetic Start/Stop buttons for appliances that
// run programs. The appliances expose no start command of their own, so the user
// stages a program with the selected-program select and then presses Start
// (which posts the selected program to /ro/activeProgram); Stop aborts it.
//
// The payload shares basePayload and sanitizeForPlatform with the
// feature-derived entities (F6). Three things stay deliberately different,
// and none of them is drift:
//
//   - payload_press is Home Assistant's own "PRESS", not the "true" a
//     command feature writes: handleProgramControl recognises the topic
//     and never writes a value.
//   - No entity_category and no enabled_by_default, i.e. enabled and
//     prominent. These two ARE the primary program controls — the same
//     status isPrimary already gives the active/selected-program entities
//     they operate — so the curated set keeps them, as it must: a curated
//     install that could stage a program but not start it is not a
//     smaller feature set, it is a broken one.
//   - No enrichment and no exclusion. Both are keyed on a feature name and
//     these back no feature, so there is nothing for the operator
//     catalogue to match. Excluding them is not currently expressible;
//     that is a gap in the catalogue's vocabulary, not a divergence
//     between the two builders, and it is left as it is.
func (d *Discovery) publishProgramControls(ctx context.Context, device string, entities []*homeconnect.Entity, dev deviceBlock, published map[string]bool) {
	hasProgram := false
	for _, e := range entities {
		if e.Desc.Kind == profile.KindActiveProgram || e.Desc.Kind == profile.KindSelectedProgram {
			hasProgram = true
			break
		}
	}
	if !hasProgram {
		return
	}
	dt := layout.NewDevice(d.rootTopic, device)
	controls := []struct{ key, nameEN, nameDE string }{
		{layout.ControlStartProgram, "Start program", "Programm starten"},
		{layout.ControlStopProgram, "Stop program", "Programm stoppen"},
	}
	t := entityTopics{
		command:      "", // no feature behind it; the control topic is its own
		availability: dt.Availability(),
		bridge:       layout.Bridge(d.rootTopic),
	}
	for _, c := range controls {
		name := c.nameEN
		if d.lang == "de" {
			name = c.nameDE
		}
		// The same five common keys as every feature-derived entity, from
		// the same function, so a key added to one path cannot be silently
		// missing from the other (F6).
		payload := basePayload(platformButton, device, c.key, name, t, dev)
		payload["command_topic"] = dt.ControlCommand(c.key)
		payload["payload_press"] = controlPressPayload
		// Run the same last-pass platform filter the derived entities get.
		// It is a no-op on these two today; that is the point — it stays a
		// no-op by construction rather than by nobody having added a key.
		sanitizeForPlatform(payload, platformButton)
		b, err := json.Marshal(payload)
		if err != nil {
			d.logger.Warn("hass.marshal", slog.String("feature", c.key), slog.String("err", err.Error()))
			continue
		}
		topic := d.baseTopic + "/button/" + sanitize(device) + "/" + c.key + "/config"
		published[topic] = true
		if _, err := d.mqtt.Publish(ctx, topic, b); err != nil {
			d.logger.Warn("hass.publish", slog.String("topic", topic), slog.String("err", err.Error()))
		}
	}
}

// BundleTopic is where this appliance's device document is retained:
// <HASS_BASE_TOPIC>/device/<slugify(device name)>/config.
//
// Exported because three readers outside the publish path need the exact
// string and a second derivation of it is how they drift. The
// HASS_DISCOVERY_REFRESH migration clears it; the operator documentation
// for a DOWNGRADE names it in a `mosquitto_pub -r -n` the user runs by
// hand; and a test compares both against the node id the renderer actually
// produces. go-mtec2mqtt documented the topic in five places and got it
// wrong in three, because the node id is the SLUG of the device name and
// not the name — here "Geschirrspüler" is addressed as `geschirrspuler`,
// which is neither the raw name nor merely its lower case.
func (d *Discovery) BundleTopic(device string) string {
	return publisher.BundleConfigTopic(d.baseTopic, d.BundleNodeID(device))
}

// BundleNodeID is the device document's node id — the third topic segment,
// and the key a parsed publisher.ConfigTopic reports for it.
//
// Exported so the tombstone read-back can look its own document up in what
// the snapshot window delivered without composing the topic a second time.
// It is slugify(<device name>), the same string
// [Discovery.hamqttContext]'s NodeID renders, which is neither the raw
// appliance name nor merely its lower case: "Geschirrspüler" is addressed
// as "geschirrspuler". TestTheBundleNodeIDIsTheOneTheTopicCarries is what
// keeps the two from drifting.
func (d *Discovery) BundleNodeID(device string) string { return sanitize(device) }

// BundleFilter is the MQTT filter that matches every device document under
// this daemon's discovery prefix, and nothing else.
//
// It is deliberately narrower than the orphan sweep's `<prefix>/#`. The
// tombstone read-back runs immediately before the one publish that cannot
// be undone, so what it costs on the wire is what the migration costs: this
// filter replays one retained message per appliance, where `<prefix>/#`
// replays all 687 per-entity configs and the ~473 KB document as well. It
// is also what keeps the read-back's window distinguishable from the
// sweep's in a SUBSCRIBE list, which is where the sweep is pinned as having
// not run.
func (d *Discovery) BundleFilter() string { return d.baseTopic + "/device/+/config" }

// BundleNodeIDOf reads the node id back out of a device document topic,
// reporting false for anything that is not one under this prefix.
//
// It is publisher.ParseConfigTopic rather than a split on "/", because the
// question "is this a device document" is the library's to answer and
// getting it wrong here means reading a per-entity config as a document.
func (d *Discovery) BundleNodeIDOf(topic string) (string, bool) {
	t, ok := publisher.ParseConfigTopic(d.baseTopic, topic)
	if !ok || !t.Bundle || t.NodeID == "" {
		return "", false
	}
	return t.NodeID, true
}

// PublishDeviceBundle renders one appliance's device document and publishes
// it, retracting the per-entity configs it supersedes first.
//
// It returns the config topic it addressed and the error that stopped it,
// if any. The topic is returned even on a failure, because the caller has
// to be able to name what it did NOT publish.
//
// # Why every refusal here is a refusal to write anything at all
//
// The document and the retraction of the 687 per-entity configs it replaces
// are one migration, and Home Assistant forces their order: a retained
// per-entity config and a document carrying the same `unique_id` cannot
// coexist, and whichever arrives second is refused with a single
// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` and
// no entities. publisher.Runtime.PublishBundle therefore retracts first and
// publishes second, which means every failure mode between the two leaves
// the appliance with NO discovery config rather than with its old one.
//
// So each of the four gates below fails before the retraction:
//
//   - A render error. Nothing to publish, and a half-rendered document is
//     not a smaller fleet, it is a wrong one.
//   - An empty component set. A document with no components supersedes
//     nothing and declares nothing, so publishing it would retract nothing
//     and leave 687 orphans; but the state it describes — an appliance that
//     classified to zero entities — is a fault somewhere upstream, and
//     writing it to the broker turns that fault into a fleet-wide deletion
//     the moment the sweep runs.
//   - A BLOCKING discovery.Validate. Home Assistant validates a document as
//     one unit and drops the whole thing, so a single refused component
//     costs the appliance all of its entities — which is exactly why F13
//     had to be fixed before this step (#43). An advisory ValidationError
//     is logged and published, because advisory means Home Assistant
//     accepts it.
//   - A packet the broker will not take. That refusal lives in
//     haplane.Plane.PublishBundle, before the retraction, and is reported
//     here as haplane.ErrDocumentTooLarge.
//
// # prior, and what it adds
//
// prior is the component set the PREVIOUS document declared, read back from
// the broker once per connection — see [Discovery.BundleComponents] and
// internal/bridge's Bridge.priorComponentsFor. Every key it holds that this
// render does not produce is written into the document as a TOMBSTONE:
// `{"platform":"…"}`, the entry Home Assistant reads as a removal. A
// component this daemon stops publishing is therefore removed rather than
// stranded — which is what `HASS_DISCOVERY: curated` needed and did not
// have.
//
// A nil prior is exactly the old behaviour, and every failure direction of
// the read-back degrades to it. See the file comment in tombstone.go.
//
// The second return is the LIVE component set that was published: what the
// caller remembers as the previous state of the document it just wrote. It
// is nil on every refusal, because a document that was not written did not
// become anybody's previous document.
func (d *Discovery) PublishDeviceBundle(
	ctx context.Context,
	device string,
	info profile.DeviceInfo,
	entities []*homeconnect.Entity,
	prior map[string]discovery.Component,
) (topic string, live map[string]discovery.Component, err error) {
	b, err := d.BundleFor(device, info, entities)
	if err != nil {
		d.logger.Error("hass.bundle_render", slog.String("device", device), slog.String("err", err.Error()))
		return "", nil, err
	}
	if gone := ApplyTombstones(b, prior); len(gone) > 0 {
		d.logger.Info("hass.components_removed",
			slog.String("device", device), slog.Int("removed", len(gone)),
			slog.String("consequence",
				"no longer published; the document carries a platform-only entry for each, "+
					"which is how Home Assistant is told to delete the entity"))
	}
	topic, err = d.publishBundle(ctx, device, b)
	if err != nil {
		return topic, nil, err
	}
	return topic, LiveComponents(b), nil
}

// publishBundle is PublishDeviceBundle's gates and its write, separated
// from the render so a test can drive them with a document of its own.
//
// Without that separation the blocking-validation gate is unreachable from
// a test: a document this daemon renders is never blocking (that is what
// #43 fixed), so the only way to assert that a blocking one is withheld is
// to hand one in.
//
// The empty-document gate counts LIVE components rather than entries, and
// that distinction arrived with tombstones. A tombstone IS an entry, so an
// appliance that classified to zero entities against a prior document of
// 687 renders a document of 687 entries and none of them declares anything
// — which walked straight past a `len(b.Components) == 0` test and
// published a fleet-wide deletion as if it were a migration.
// TestADocumentOfNothingButTombstonesIsWithheld is the pin.
func (d *Discovery) publishBundle(ctx context.Context, device string, b *discovery.Bundle) (string, error) {
	topic := publisher.BundleConfigTopic(d.baseTopic, b.NodeID)
	live := len(LiveComponents(b))
	if live == 0 {
		err := fmt.Errorf("hass: device document for %q has no components", device)
		d.logger.Error("hass.bundle_empty",
			slog.String("device", device), slog.String("topic", topic),
			slog.Int("entries", len(b.Components)),
			slog.String("consequence", "withheld; the per-entity configs are left in place"))
		return topic, err
	}
	if err := d.validateBundle(device, topic, b); err != nil {
		return topic, err
	}
	sent, err := d.mqtt.PublishBundle(ctx, b)
	if err != nil {
		d.logger.Error("hass.bundle_publish",
			slog.String("device", device), slog.String("topic", topic),
			slog.Int("components", live), slog.Int("tombstones", len(b.Components)-live),
			slog.String("err", err.Error()))
		return topic, err
	}
	d.logger.Info("hass.bundle_published",
		slog.String("device", device), slog.String("topic", topic),
		slog.Int("components", live), slog.Int("tombstones", len(b.Components)-live),
		slog.Bool("written", sent))
	return topic, nil
}

// validateBundle runs discovery.Validate and decides what a finding costs.
//
// Blocking refuses the publish; advisory logs and continues. The split
// matters because the two are not degrees of the same thing: Home Assistant
// drops a document it cannot validate in its entirety, so "blocking" means
// zero entities for the appliance, while "advisory" means Home Assistant
// accepts the document as it is.
func (d *Discovery) validateBundle(device, topic string, b *discovery.Bundle) error {
	err := discovery.Validate(b)
	if err == nil {
		return nil
	}
	var ve *discovery.ValidationError
	if !errors.As(err, &ve) {
		d.logger.Error("hass.bundle_invalid",
			slog.String("device", device), slog.String("topic", topic), slog.String("err", err.Error()))
		return err
	}
	if ve.Blocking() {
		d.logger.Error("hass.bundle_invalid",
			slog.String("device", device), slog.String("topic", topic),
			slog.Int("components", len(b.Components)),
			slog.String("err", err.Error()),
			slog.String("consequence",
				"withheld; Home Assistant drops a device document whole, so publishing it would cost the appliance every entity"))
		return err
	}
	d.logger.Warn("hass.bundle_advisory",
		slog.String("device", device), slog.String("topic", topic), slog.String("err", err.Error()))
	return nil
}

// publishedPlatforms is the set of Home Assistant platforms this daemon
// emits. Home Assistant declares 32; six of them are reachable from
// classify, and a retained config on any of the other 26 belongs to
// somebody else however its topic is shaped.
var publishedPlatforms = map[string]bool{
	platformSensor:       true,
	platformBinarySensor: true,
	platformSwitch:       true,
	platformSelect:       true,
	platformNumber:       true,
	platformButton:       true,
}

// OwnsConfigTopic builds the ownership predicate publisher.SweepRequest
// takes: does this retained discovery config TOPIC fall inside the
// namespace this daemon publishes for devices?
//
// It is a namespace test and nothing more, because a topic is all the
// broker offers the sweep — the payload reaches the caller through
// publisher.SweepRequest.Inspect, and [Discovery.IsOwnConfig] is the
// second, stronger test that runs there. Both are needed and neither is
// redundant: this one keeps a foreign integration's configs out of the
// window's judgement at all, and that one keeps a SIBLING INSTANCE of
// this same daemon out of it (F8) — two instances with different
// MQTT_TOPIC roots publish identical config topics for an appliance they
// both call "Geschirrspüler", so no predicate that sees only the topic
// can tell them apart. The state topic inside the payload can.
//
// Four narrowings, each one refusing a class this daemon does not publish:
//
//   - The device-document form (<prefix>/device/<node>/config). Step 6 of
//     the ADR 0070 rollout made this daemon the publisher of exactly that
//     form, and the line was revisited rather than inherited: it stays,
//     deliberately. The document is now the SINGLE retained topic holding
//     every entity of an appliance, so admitting it to a pass whose job is
//     to delete what nothing claims trades a leak for a fleet-wide
//     deletion — and the payload half could not narrow it again, because
//     [Discovery.IsOwnConfig] reads a top-level `unique_id` and topics
//     that a document does not have at its top level.
//     TestTheSweepNeverOffersTheDocumentItJustPublished is the pin.
//
//     What that costs is stated rather than hidden: a document whose node
//     id is no longer configured — an appliance renamed or removed in
//     devices.yaml — is never retracted by this daemon. It LEAKS, it does
//     not delete, and it is cleared by hand with the one retained-empty
//     publish the changelog, README and DOCS already document for the
//     rollback path. The same is true of that appliance's per-entity
//     configs and always has been, for the same reason: the last
//     narrowing below scopes every judgement to the node ids this process
//     is configured for.
//
//   - Any form without a node id. publisher.ParseConfigTopic accepts the
//     four-segment (<prefix>/<platform>/<object>/config) and three-segment
//     forms as well, which is what both sibling bridges and Tasmota
//     publish into a shared discovery tree. This daemon's fleet is
//     five-segment — measured, 687 of 687 — so a topic with no node id is
//     never ours.
//
//   - A platform this daemon never emits.
//
//   - A node id that is not one of the device names this process is
//     configured for. devices is the caller's scope, and it is
//     deliberately a parameter rather than the whole fleet: a per-device
//     reconcile that judged the whole fleet would call a second
//     appliance's configs orphans during the window in which only the
//     first has published, and retract them.
func (d *Discovery) OwnsConfigTopic(devices ...string) func(publisher.ConfigTopic) bool {
	nodes := make(map[string]bool, len(devices))
	for _, dev := range devices {
		if node := sanitize(dev); node != "" {
			nodes[node] = true
		}
	}
	return func(t publisher.ConfigTopic) bool {
		if t.Bundle || t.Platform == "" || t.NodeID == "" || t.ObjectID == "" {
			return false
		}
		if !publishedPlatforms[t.Platform] {
			return false
		}
		return nodes[t.NodeID]
	}
}

// ConfigTopicFor rebuilds the retained config topic a parsed ConfigTopic
// came from, so the sweep's Inspect callback can hand the caller a topic
// to retract rather than re-deriving one.
func (d *Discovery) ConfigTopicFor(t publisher.ConfigTopic) string {
	return publisher.EntityConfigTopic(d.baseTopic, t.Platform, t.NodeID, t.ObjectID)
}

// IsOwnConfig reports whether a retained Home Assistant discovery config
// payload was published by THIS instance of this daemon.
//
// It is the second half of the ownership rule, and on the hard case it is
// the only half. Two instances with different MQTT_TOPIC roots, the same
// HASS_BASE_TOPIC and an appliance name in common publish byte-identical
// config topics, node ids, platforms and `unique_id`s — MQTT_TOPIC appears
// in none of the four identity strings (F8) — so nothing in the TOPIC can
// tell them apart and nothing in the identity plane can either. The only
// place the two differ is the set of MQTT topics the payload points at,
// every one of which sits under the publishing instance's own root.
//
// # Why a prefix is not an identity, and what a nested sibling did with it
//
// This used to read: `unique_id` has our prefix, at least one topic is
// named, and every topic named begins with `<root>/`. That is a topic
// PREFIX test, and a prefix is not an identity. MQTT_TOPIC is only
// required to be non-empty, so a second instance may legally be rooted
// UNDER the first — `homeconnect` and `homeconnect/kitchen` is a more
// natural way to keep one broker tidy than inventing two disjoint names —
// and every topic the inner instance names then begins with the outer
// instance's root. Driven over the shipped catalogue, the outer instance
// accepted 687 of 687 of the inner one's components and tombstoned the
// 510 of them that are LIVE: Home Assistant deletes those entities from
// its registry, the inner instance republishes them, and the outer one
// deletes them again on its next connection. That is go-daikin2mqtt #80's
// permanent ping-pong, reached through the predicate that was supposed to
// make it unreachable here.
//
// The break is asymmetric, which is what made it easy to miss: only the
// strict topic-level ANCESTOR eats the descendant's components. The inner
// instance declines the outer's correctly, because `homeconnect/…` does
// not begin with `homeconnect/kitchen/`. Disjoint roots were and remain
// safe — `homeconnect2`, `hc`, `homeconnectx`, `home` all decline 0 of
// 687.
//
// # The rule
//
// Attribution is an EXACT match against a topic that only this instance
// renders, not a prefix that every instance nested under it also satisfies:
//
//   - `<root>/status` — [layout.Bridge], the daemon's own status topic,
//     carried by every payload since F1 as the first of its two
//     availability sources. `homeconnect/kitchen/status` is a different
//     string from `homeconnect/status`, so no nesting can produce it.
//   - `<root>/<device>/availability` with `<device>` a SINGLE topic level
//     — the device availability topic, which is what a pre-F1 payload
//     carries in the flat `availability_topic` key instead of the list.
//     An instance rooted at `<root>/<s>` renders `<root>/<s>/status` and
//     `<root>/<s>/<device>/availability`; neither can equal
//     `<root>/<one level>/availability`, because `<s>/<device>` is never
//     a single level. The anchor is kept for a reason that is not
//     hypothetical: every installation upgrading from 0.12.0 has a
//     retained tree of flat-availability payloads, and without it the
//     orphan sweep would stop recognising its OWN stale configs and
//     strand them forever.
//
// The prefix test stays, as the second half rather than the whole: every
// topic a payload names must still be under this root, so a payload that
// mixes roots is not ours whatever else it says. Both halves are needed —
// the anchor proves WHOSE, the prefix proves that nothing else in the
// payload points somewhere we never publish.
//
// And both directions of "at least one anchor" are deliberate:
//
//   - A payload that names no topic at all, or names only topics that
//     could belong to an instance nested under us, cannot be PROVEN ours,
//     and ownership that cannot be proven is not claimed. No config this
//     daemon has ever published is in that position: every one carries
//     either the availability list (since F1) or the flat availability
//     topic (before it).
//   - Being strict leaves a stale entity behind at worst. Being loose
//     deletes a live instance's fleet — which is what the prefix rule did.
func (d *Discovery) IsOwnConfig(payload []byte) bool {
	var cfg retainedConfig
	if json.Unmarshal(payload, &cfg) != nil {
		return false
	}
	if !strings.HasPrefix(cfg.UniqueID, "homeconnect_") {
		return false
	}
	root := d.rootTopic + "/"
	anchored := false
	for _, topic := range cfg.topics() {
		if topic == "" {
			continue
		}
		if !strings.HasPrefix(topic, root) {
			return false
		}
		if d.isIdentityAnchor(topic) {
			anchored = true
		}
	}
	return anchored
}

// isIdentityAnchor reports whether topic is one this instance renders and
// no instance nested under it can. See [Discovery.IsOwnConfig] for why
// attribution needs an exact match rather than a prefix, and why there are
// two anchors rather than one.
func (d *Discovery) isIdentityAnchor(topic string) bool {
	if topic == layout.Bridge(d.rootTopic) {
		return true
	}
	rel, ok := strings.CutPrefix(topic, d.rootTopic+"/")
	if !ok {
		return false
	}
	device, ok := strings.CutSuffix(rel, "/availability")
	if !ok {
		return false
	}
	return device != "" && !strings.Contains(device, "/")
}

// retainedConfig is the part of a retained per-entity discovery config
// [Discovery.IsOwnConfig] reads: the identity string, and every MQTT topic
// the payload points at.
//
// The four topic-bearing keys are not interchangeable and none is
// redundant. `state_topic` covers 667 of this appliance's 687 configs;
// `command_topic` is what a BUTTON has instead, and buttons are the 20 that
// used to fall through to the bare namespace prefix; `availability` is the
// two-source list every config has carried since F1; `availability_topic`
// is the flat key those payloads had before it, which is exactly the shape
// of the stale configs a sweep exists to clear.
type retainedConfig struct {
	UniqueID          string `json:"unique_id"`
	StateTopic        string `json:"state_topic"`
	CommandTopic      string `json:"command_topic"`
	AvailabilityTopic string `json:"availability_topic"`
	Availability      []struct {
		Topic string `json:"topic"`
	} `json:"availability"`
}

func (c retainedConfig) topics() []string {
	out := make([]string, 0, 3+len(c.Availability))
	out = append(out, c.StateTopic, c.CommandTopic, c.AvailabilityTopic)
	for _, a := range c.Availability {
		out = append(out, a.Topic)
	}
	return out
}
