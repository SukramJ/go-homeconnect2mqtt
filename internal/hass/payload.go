// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package hass generates Home Assistant MQTT discovery payloads from the
// appliance entity model. It maps every feature to a platform via a
// heuristic (docs/04-device-mapping.md §1) and emits one config payload
// per entity, plus birth/LWT re-publish handling.
//
// Entity ids are seeded English and language-independent via
// `default_entity_id`, while the friendly `name` is localized. The former
// `object_id` key is no longer published: Home Assistant's MQTT discovery
// schemas are extra=REMOVE_EXTRA and none of the 32 MQTT platforms declares
// `object_id` any more (measured against HA 2026.9), so it was silently
// dropped on arrival. Only `name` is localized; `unique_id` stays independent.
// Most of the long tail of features is published disabled-by-default and
// categorized as diagnostic/config so Home Assistant stays uncluttered without
// dropping the "expose everything" promise.
package hass

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	hacatalog "github.com/SukramJ/go-ha-catalog"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/slug"
)

// Platforms.
const (
	platformSwitch       = "switch"
	platformSelect       = "select"
	platformSensor       = "sensor"
	platformBinarySensor = "binary_sensor"
	platformNumber       = "number"
	platformButton       = "button"
)

// Availability payloads. These are the two words the daemon writes to the
// bridge status topic and to every device availability topic, and the two
// every entity is told to read there. Home Assistant's own defaults happen
// to be the same pair, but an entity that reads a topic must not depend on
// a default agreeing with a publisher in another package: they are spelled
// once, here, and read by internal/bridge and cmd/homeconnect2mqtt.
const (
	PayloadAvailable    = "online"
	PayloadNotAvailable = "offline"
)

// availabilityModeAll requires EVERY declared source to say online. It is
// written out rather than left to Home Assistant, whose default is
// `latest` — which with two sources means whichever message arrived last
// wins, so a live daemon reporting an unreachable appliance would read as
// available.
const availabilityModeAll = "all"

// Entity categories (Home Assistant).
const (
	categoryDiagnostic = "diagnostic"
	categoryConfig     = "config"
)

// deviceClassEnum is HA's enumeration class. It exists on `sensor` only and
// requires an `options` list.
const deviceClassEnum = "enum"

// commandPressPayload is what a command button writes. A command feature is
// executed by writing to it (POST /ro/values, per docs/01-protocol.md §7), so
// the press payload must be the value to write — not HA's default "PRESS",
// which the boolean cast would turn into `false`.
const commandPressPayload = "true"

// controlPressPayload is what the two synthetic program buttons write. They
// back no feature: internal/bridge's handleProgramControl recognises the
// topic and ignores the payload entirely, so this is Home Assistant's own
// default rather than a value to write. The difference from
// commandPressPayload is deliberate and is the one difference between the
// two button paths that survives their convergence (F6).
const controlPressPayload = "PRESS"

// deviceClasses is Home Assistant's per-platform device-class vocabulary,
// decoded once from go-ha-catalog's embedded snapshot of home-assistant/core.
//
// It replaces three hand-maintained sets and, more importantly, a
// `case platformSensor: return true` that trusted the operator catalogue
// absolutely (F13). Home Assistant validates a discovery config strictly and
// discards the WHOLE entity when device_class is not one of the classes the
// target platform declares — silently, before the entity exists, with no
// error on the wire and no log line. `sensor` is not the open vocabulary the
// old code assumed: it declares 62 classes and `door`, `plug`,
// `battery_charging`, `connectivity` and `light` are not among them.
//
// The table is a data file embedded in go-ha-catalog, so a decode failure is
// a broken build of this binary rather than a runtime condition, and
// TestDeviceClassTableLoads asserts it. When it does fail, deviceClassAllowed
// fails CLOSED — no entity carries a device_class at all. That costs every
// entity an icon and its class semantics; the open failure costs the whole
// entity, and at ADR 0070 step 6, where a device bundle is validated as one
// document, it costs every entity on the device.
var deviceClasses = sync.OnceValue(func() map[string]map[string]bool {
	raw, err := hacatalog.LoadDeviceClasses()
	if err != nil {
		return nil
	}
	out := make(map[string]map[string]bool, len(raw))
	for platform, classes := range raw {
		set := make(map[string]bool, len(classes))
		for _, c := range classes {
			set[c] = true
		}
		out[platform] = set
	}
	return out
})

// sensorRelationTables is what Home Assistant says a sensor's device_class,
// unit_of_measurement and state_class may be COMBINED as, decoded once from
// go-ha-catalog: the state classes each device class admits (the table
// go-hamqtt's discovery.Validate checks a document against), the units each
// device class admits, and which device classes are numeric at all.
type sensorRelationTables struct {
	stateClasses map[string][]string
	units        map[string][]string
	numeric      map[string]bool
}

var sensorRelations = sync.OnceValue(func() *sensorRelationTables {
	rel, err := hacatalog.LoadRelations()
	if err != nil {
		return nil
	}
	sensor, err := hacatalog.LoadSensor()
	if err != nil {
		return nil
	}
	numeric := make(map[string]bool, len(sensor.NumericDeviceClasses))
	for _, c := range sensor.NumericDeviceClasses {
		numeric[c] = true
	}
	return &sensorRelationTables{
		stateClasses: rel.SensorDeviceClassStateClasses,
		units:        sensor.DeviceClassUnits,
		numeric:      numeric,
	}
})

// reconcileSensorClasses returns the device_class, unit and state_class a
// non-enum sensor may carry TOGETHER, given the three it was derived with.
//
// It exists because the three come from different places — the heuristic
// derives a unit and a state class from the wire type, the catalogue
// overlays a device class afterwards — and nothing checked the result as a
// combination. 0.13.0 to 0.15.0 rendered `BSH.Common.Option.RemainingProgramTime`
// as device_class `timestamp` (from mapping.yaml) with unit `s` and state_class
// `measurement` (from its integer wire type). Home Assistant admits no state
// class and no unit on a timestamp, discovery.Validate refused the device
// document, and because Home Assistant drops a document whole, every
// appliance exposing that one option published no document at all.
//
// The rules, each one Home Assistant's own:
//
//   - A non-numeric device class (timestamp, date, uptime) carries neither a
//     unit nor a state class: the sensor's state is not a number.
//   - A unit the device class does not declare is refused by Home
//     Assistant's MQTT sensor schema, which drops the entity — and in a
//     device document, the document. The DEVICE CLASS is what goes: the unit
//     describes what the appliance actually reports, the class is an overlay.
//   - A state class the device class does not admit is dropped; the entity
//     loses long-term statistics, not its existence.
//
// A table that fails to load fails CLOSED, like deviceClassAllowed: the
// device class is dropped, which is always a combination Home Assistant
// accepts.
func reconcileSensorClasses(dc, unit, sc string) (outDC, outUnit, outSC string) {
	return reconcileSensorClassesIn(sensorRelations(), dc, unit, sc)
}

// reconcileSensorClassesIn is reconcileSensorClasses against given tables,
// so the fail-closed branch is reachable from a test.
func reconcileSensorClassesIn(t *sensorRelationTables, dc, unit, sc string) (outDC, outUnit, outSC string) {
	if dc == "" {
		return dc, unit, sc
	}
	if t == nil {
		return "", unit, sc
	}
	if !t.numeric[dc] {
		return dc, "", ""
	}
	if units, known := t.units[dc]; known && unit != "" && !slices.Contains(units, unit) {
		return "", unit, sc
	}
	if allowed, known := t.stateClasses[dc]; known && sc != "" && !slices.Contains(allowed, sc) {
		sc = ""
	}
	return dc, unit, sc
}

// classify maps an entity to a Home Assistant platform. ok is false when
// the entity should not be exposed via discovery (e.g. a raw program node).
func classify(e *homeconnect.Entity) (platform string, ok bool) {
	switch e.Desc.Kind {
	case profile.KindCommand:
		return platformButton, true
	case profile.KindEvent:
		return platformBinarySensor, true
	case profile.KindActiveProgram:
		// Read-only: shows the running program. Starting is done via the
		// synthetic "start program" control, not by writing this.
		return platformSensor, true
	case profile.KindSelectedProgram:
		// Choosable when writable AND there is something to choose from ->
		// a select listing the programs to stage; otherwise a read-only
		// sensor showing the current selection.
		//
		// The programs are normally folded into the enumeration by the
		// parser, but an appliance that exposes none leaves it empty, and
		// this used to route on the kind alone: the result was a select
		// with "options": [], a visible, writable dropdown with nothing in
		// it that can never be set (F5). Falling back to the sensor is
		// what the read-only branch below already does, and it still shows
		// the current selection. The superseded select config retracts
		// itself — reconcileOrphans clears the retained config topics this
		// daemon owns and no longer publishes, on the next discovery run.
		if e.Desc.Writable() && e.Desc.IsEnum() {
			return platformSelect, true
		}
		return platformSensor, true
	case profile.KindProgram, profile.KindProtectionPort:
		return "", false
	default:
		// status / setting / option are classified by type below.
	}

	writable := e.Desc.Writable()
	if e.Desc.IsEnum() {
		if writable {
			return platformSelect, true
		}
		return platformSensor, true
	}
	switch e.Desc.ProtocolType {
	case profile.ProtocolBoolean:
		if writable {
			return platformSwitch, true
		}
		return platformBinarySensor, true
	case profile.ProtocolInteger, profile.ProtocolFloat:
		if writable {
			return platformNumber, true
		}
		return platformSensor, true
	default:
		return platformSensor, true
	}
}

// primarySuffixes are the operationally important feature leaf names that stay
// enabled and uncategorized by default (the rest is disabled-by-default).
var primarySuffixes = map[string]bool{
	"PowerState": true, "OperationState": true, "DoorState": true,
	"RemainingProgramTime": true, "ProgramProgress": true, "StartInRelative": true,
	"ActiveProgram": true, "SelectedProgram": true,
	"RemoteControlActive": true, "RemoteControlStartAllowed": true,
	"BackendConnected": true, "ProgramFinished": true, "ProgramAborted": true,
	"BatteryLevel": true, "ChargingState": true,
}

func leafName(e *homeconnect.Entity) string {
	n := e.Name()
	if _, after, ok := strings.CutLast(n, "."); ok {
		return after
	}
	return n
}

// isPrimary reports whether an entity belongs to the curated, enabled-by-
// default set.
func isPrimary(e *homeconnect.Entity) bool {
	if e.Desc.Kind == profile.KindActiveProgram || e.Desc.Kind == profile.KindSelectedProgram {
		return true
	}
	return primarySuffixes[leafName(e)]
}

// enabledByDefault is the heuristic default-enabled state. Primary entities are
// on; everything else is published but disabled (one click to enable in HA).
func enabledByDefault(e *homeconnect.Entity) bool { return isPrimary(e) }

// entityCategoryFor classifies non-primary entities into HA's device-page
// sections: writable settings -> config, read-only status/option/event ->
// diagnostic. Primary entities stay uncategorized (prominent).
//
// This is the INTENT, judged on the feature; whether the platform the entity
// lands on can carry it is a second question, answered by
// entityCategoryAllowed after enrichment. A writable string setting is a
// `sensor` (classify has no text platform), and a sensor refuses `config`.
func entityCategoryFor(e *homeconnect.Entity) string {
	if isPrimary(e) {
		return ""
	}
	switch e.Desc.Kind {
	case profile.KindSetting, profile.KindOption:
		// A writable setting/option is a control -> Configuration; a read-only
		// one is informational -> Diagnostic.
		if e.Desc.Writable() {
			return categoryConfig
		}
		return categoryDiagnostic
	case profile.KindCommand:
		return categoryConfig // a button/action is a control
	case profile.KindStatus, profile.KindEvent:
		return categoryDiagnostic
	default:
		return ""
	}
}

// entityCategoryAllowed reports whether Home Assistant will add an entity of
// platform with entity_category cat. The rule is the base components', read
// from home-assistant/core 2026.10:
//
//   - The value must be one of EntityCategory's two members:
//     ENTITY_CATEGORIES_SCHEMA is `Coerce(EntityCategory)`
//     (helpers/entity.py:212), and the MQTT schema applies it to every
//     platform (components/mqtt/schemas.py:183), so anything else costs the
//     component.
//   - `sensor` and `binary_sensor` refuse `config`: both raise
//     "cannot be added as the entity category is set to config" from
//     async_internal_added_to_hass (sensor/__init__.py:309-313,
//     binary_sensor/__init__.py:79-83). The entity never exists, and nothing
//     on the wire or in a validator says so — discovery.Validate (go-hamqtt
//     v0.36.0) does not check it.
//   - `switch`, `select`, `number` and `button` accept both; their base
//     components carry no such check.
//
// An empty category is always allowed: it is the absence of the key.
func entityCategoryAllowed(platform, cat string) bool {
	switch cat {
	case "":
		return true
	case categoryDiagnostic:
		return true
	case categoryConfig:
		return platform != platformSensor && platform != platformBinarySensor
	default:
		return false
	}
}

// renderableCategory is the entity_category an entity of platform is
// published with, given the category it was derived or configured with.
//
// A `config` entity on a read-only platform becomes `diagnostic`, rather
// than losing its category. Home Assistant defines diagnostic as "an entity
// exposing some configuration parameter, or diagnostics of a device"
// (const.py:1037-1039) — which is exactly a setting the appliance reports
// but this platform cannot write — and groups it with the diagnostic
// entities on the device page, below the controls and out of the default
// dashboards, where a `config` entity would have been had it existed.
// Dropping the category instead would promote a long-tail setting to a
// primary, uncategorized entity. A value Home Assistant does not know is
// dropped: there is nothing it could be mapped to.
func renderableCategory(platform, cat string) string {
	if entityCategoryAllowed(platform, cat) {
		return cat
	}
	if cat == categoryConfig {
		return categoryDiagnostic
	}
	return ""
}

// numberMinStep is the smallest step Home Assistant's MQTT number schema
// accepts: `Range(min=1e-3)` (components/mqtt/number.py:98-100).
const numberMinStep = 1e-3

// numberDefaultMin and numberDefaultMax are what Home Assistant assumes for
// an absent min or max (components/number/const.py:93-94). They matter to
// the min <= max check, which it applies to the values after defaulting
// (components/mqtt/number.py:79-80).
const (
	numberDefaultMin = 0.0
	numberDefaultMax = 100.0
)

// numberBounds is the min, max and step a number entity may be published
// with, from the bounds the appliance described. A nil return is a key that
// is not written, which leaves Home Assistant's default in place.
//
// Each guard is a refusal by Home Assistant's MQTT number schema, which
// costs the entity:
//
//   - a step below 1e-3 — zero or negative from a description that says
//     stepSize="0" — fails `Range(min=1e-3)`; the command path already
//     ignores such a step (internal/bridge's writeValue uses only a Step > 0);
//   - min > max, after HA's own 0/100 defaults fill the absent one, fails
//     validate_config. Both bounds are then left out: there is no way to
//     tell which of the two is wrong, and HA's defaults are a valid pair;
//   - a non-finite bound cannot be encoded in JSON at all.
func numberBounds(b homeconnect.Bounds) (minV, maxV, step *float64) {
	finite := func(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
	if b.HasMin && finite(b.Min) {
		minV = new(b.Min)
	}
	if b.HasMax && finite(b.Max) {
		maxV = new(b.Max)
	}
	effMin, effMax := numberDefaultMin, numberDefaultMax
	if minV != nil {
		effMin = *minV
	}
	if maxV != nil {
		effMax = *maxV
	}
	if effMin > effMax {
		minV, maxV = nil, nil
	}
	if b.HasStep && finite(b.Step) && b.Step >= numberMinStep {
		step = new(b.Step)
	}
	return minV, maxV, step
}

// stateClassFor derives a sensor state_class for numeric read-only sensors so
// they get long-term statistics. Counters increase monotonically.
func stateClassFor(e *homeconnect.Entity, platform string) string {
	if platform != platformSensor || e.Desc.IsEnum() {
		return ""
	}
	switch e.Desc.ProtocolType {
	case profile.ProtocolInteger, profile.ProtocolFloat:
		if strings.Contains(e.Name(), "Count") {
			return "total_increasing"
		}
		return "measurement"
	default:
		return ""
	}
}

// deviceClassAndUnit derives the HA device_class and unit from the fine
// content type (docs/04 §2).
func deviceClassAndUnit(e *homeconnect.Entity) (deviceClass, unit string) {
	if e.Desc.IsEnum() {
		return deviceClassEnum, ""
	}
	switch e.Desc.ContentType {
	case "temperatureCelsius":
		return "temperature", "°C"
	case "temperatureFahrenheit":
		return "temperature", "°F"
	case "percent":
		return "", "%"
	case "timeSpan":
		return "duration", "s"
	case "dbm":
		return "signal_strength", "dBm"
	case "rpm":
		return "", "rpm"
	case "power":
		return "power", "W"
	case "energy":
		return "energy", "Wh"
	case "weight":
		return "weight", "g"
	default:
		return "", ""
	}
}

// payloadFor builds the discovery config payload for an entity on a platform.
// It seeds an English, language-independent entity id and a heuristic English
// name; the Discovery layer then localizes the name and applies catalogue
// overrides.
func payloadFor(e *homeconnect.Entity, platform, device string, t entityTopics, dev deviceBlock) map[string]any {
	deviceClass, unit := deviceClassAndUnit(e)
	p := basePayload(platform, device, featureKey(e), humanize(e), t, dev)
	if platform == platformButton {
		// A button is write-only: HA requires command_topic and knows no state.
		p["command_topic"] = t.command
		p["payload_press"] = commandPressPayload
	} else {
		p["state_topic"] = t.state
	}
	if e.Desc.Writable() && (platform == platformSwitch || platform == platformSelect || platform == platformNumber) {
		p["command_topic"] = t.command
	}
	switch platform {
	case platformSwitch:
		p["payload_on"] = "true"
		p["payload_off"] = "false"
	case platformBinarySensor:
		if e.Desc.Kind == profile.KindEvent {
			p["payload_on"] = "Present"
			p["payload_off"] = "Off"
		} else {
			p["payload_on"] = "true"
			p["payload_off"] = "false"
		}
	case platformSelect:
		p["options"] = enumOptions(e)
	case platformSensor:
		if e.Desc.IsEnum() {
			p["options"] = enumOptions(e) // device_class=enum sensors need options
		}
	case platformNumber:
		minV, maxV, step := numberBounds(e.Bounds())
		if minV != nil {
			p["min"] = *minV
		}
		if maxV != nil {
			p["max"] = *maxV
		}
		if step != nil {
			p["step"] = *step
		}
	}
	if deviceClass != "" {
		p["device_class"] = deviceClass
	}
	if unit != "" {
		p["unit_of_measurement"] = unit
	}
	if sc := stateClassFor(e, platform); sc != "" {
		p["state_class"] = sc
	}
	if cat := entityCategoryFor(e); cat != "" {
		p["entity_category"] = cat
	}
	// HA defaults to enabled; only emit the key to disable the long tail.
	if !enabledByDefault(e) {
		p["enabled_by_default"] = false
	}
	return p
}

// applyAvailability attaches the entity's availability declaration: both
// levels, and the mode that requires both.
//
// Until F1 was fixed every payload declared exactly one flat
// `availability_topic`, the DEVICE topic — and the daemon's own Last Will
// wrote the BRIDGE topic, which no payload referenced. The device
// availability topic is only ever written by the daemon itself, so when
// the daemon was killed, crashed or lost the broker without a clean
// shutdown, nothing ever wrote `offline` anywhere an entity was reading:
// all 687 entities stayed available, showing their last retained value
// indefinitely. The will fired into a topic bound to nothing.
//
// Both sources are genuinely published, which is what makes mode `all`
// safe here: the bridge topic by the will, the OnConnect birth and the
// shutdown path in cmd/homeconnect2mqtt; the device topic by the device
// worker on every connection-state change. A declared source that is
// never published is not neutral under `all` — it is a permanently
// unavailable entity with nothing in the log to say why.
//
// The flat `availability_topic` is REMOVED rather than left alongside the
// list. Home Assistant accepts only one of the two forms; a payload
// carrying both is a contradiction it resolves silently.
func applyAvailability(p map[string]any, t entityTopics) {
	delete(p, "availability_topic")
	p["availability"] = []map[string]any{
		{"topic": t.bridge, "payload_available": PayloadAvailable, "payload_not_available": PayloadNotAvailable},
		{"topic": t.availability, "payload_available": PayloadAvailable, "payload_not_available": PayloadNotAvailable},
	}
	p["availability_mode"] = availabilityModeAll
}

// basePayload builds the keys every entity this daemon publishes
// carries, whatever built it: the two identity strings Home Assistant keys
// its registries on, the entity-id seed, the availability declaration and
// the device block.
//
// It exists because there are two payload builders — payloadFor for the
// 685 feature-derived entities and publishProgramControls for the two
// synthetic program buttons — and the second shared nothing with the first
// (F6). A key added to one was simply absent from the other, and an
// absent identity key is not a visible failure: Home Assistant registers
// the entity anyway, under a different key, beside the one it replaced.
//
// key is the per-entity id: featureKey(e) for a feature, the control key
// for a synthetic button.
func basePayload(platform, device, key, name string, t entityTopics, dev deviceBlock) map[string]any {
	p := map[string]any{
		"unique_id":         dev.idPrefix + "_" + key,
		"name":              name,
		"default_entity_id": platform + "." + slugify(device+"_"+key),
		"device":            dev.block,
	}
	// Attached here, in the one place both payload builders funnel
	// through, rather than in each of them: an entity whose availability
	// was forgotten is indistinguishable from a healthy one until the
	// daemon dies, which is the failure mode this key exists to close.
	applyAvailability(p, t)
	return p
}

// deviceClassAllowed reports whether dc may be published on platform, against
// Home Assistant's own per-platform tables rather than against a judgement
// about which platforms have an "open" vocabulary. None of them does: `select`
// declares no device class at all, `switch` two, `button` three,
// `binary_sensor` 28, `number` 58 and `sensor` 62 — and the two largest are
// not supersets of the smallest. `enum` is sensor-only; `door`, `plug`,
// `connectivity`, `light` and `battery_charging` are binary_sensor-only.
//
// An unknown platform and a failed table load both answer false. Publishing no
// device class is recoverable; publishing one the platform refuses is not
// visible at all.
func deviceClassAllowed(platform, dc string) bool {
	return deviceClassAllowedIn(deviceClasses(), platform, dc)
}

// deviceClassAllowedIn is deviceClassAllowed against a given table, so the
// fail-closed branch is reachable from a test. It cannot be reached from a
// correct build — TestDeviceClassTableLoads asserts the table decodes — and a
// branch nothing can exercise is a branch nobody has checked.
func deviceClassAllowedIn(t map[string]map[string]bool, platform, dc string) bool {
	if t == nil {
		return false
	}
	return t[platform][dc]
}

// sanitizeForPlatform strips attributes the target platform rejects. It runs
// last, after enrichment, so neither the heuristic nor an operator override can
// produce a config Home Assistant refuses to load.
func sanitizeForPlatform(p map[string]any, platform string) {
	if dc, ok := p["device_class"].(string); ok && !deviceClassAllowed(platform, dc) {
		delete(p, "device_class")
	}
	if cat, ok := p["entity_category"].(string); ok {
		if cat = renderableCategory(platform, cat); cat == "" {
			delete(p, "entity_category")
		} else {
			p["entity_category"] = cat
		}
	}
	// unit_of_measurement is sensor/number only, state_class sensor only.
	if platform != platformSensor && platform != platformNumber {
		delete(p, "unit_of_measurement")
	}
	if platform != platformSensor {
		delete(p, "state_class")
		return
	}
	// On a sensor, `options` and device_class `enum` imply each other, and an
	// enum sensor carries neither a unit nor a state_class.
	if p["device_class"] == deviceClassEnum {
		if _, ok := p["options"]; !ok {
			delete(p, "device_class")
			return
		}
		delete(p, "unit_of_measurement")
		delete(p, "state_class")
		return
	}
	delete(p, "options")
	// The three classes as a combination. See reconcileSensorClasses.
	dc, _ := p["device_class"].(string)
	unit, _ := p["unit_of_measurement"].(string)
	sc, _ := p["state_class"].(string)
	dc, unit, sc = reconcileSensorClasses(dc, unit, sc)
	for key, val := range map[string]string{"device_class": dc, "unit_of_measurement": unit, "state_class": sc} {
		if val == "" {
			delete(p, key)
		}
	}
}

// enumOptions returns the sorted enum value names for a select.
func enumOptions(e *homeconnect.Entity) []string {
	opts := make([]string, 0, len(e.Desc.Enumeration))
	for _, name := range e.Desc.Enumeration {
		opts = append(opts, name)
	}
	sortStrings(opts)
	return opts
}

// featureKey is the stable per-feature id used in unique_id and topics.
func featureKey(e *homeconnect.Entity) string {
	if e.Name() == "" {
		return "uid_" + strconv.Itoa(e.UID())
	}
	return slugify(e.Name())
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// humanize derives a short English friendly name from the feature leaf,
// splitting CamelCase and digit boundaries: "OperationState" -> "Operation
// State". It is the fallback before catalogue localization.
func humanize(e *homeconnect.Entity) string {
	if e.Name() == "" {
		return "UID " + strconv.Itoa(e.UID())
	}
	runes := []rune(leafName(e))
	var b strings.Builder
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			if (isUpper(r) && !isUpper(prev)) || (isDigit(r) && !isDigit(prev)) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// slugify lowercases, transliterates umlauts and reduces any run of
// non-alphanumeric characters to a single underscore (HA-compatible).
//
// The fold itself lives in internal/slug, because internal/profile has to
// apply the SAME one to reject two configured appliances whose names fold
// to one node id, and it cannot import this package. See slug.Slug.
func slugify(s string) string { return slug.Slug(s) }

// sanitize is slugify kept under its historical name for the device id prefix.
func sanitize(s string) string { return slugify(s) }

// sortLocalized orders display labels the way a reader of the target
// language expects. The key lowercases and folds the German umlauts the
// way DIN 5007-1 collates them (ä/ö/ü under a/o/u, ß under ss) — a plain
// byte sort would file every umlaut after "z", because their UTF-8
// encodings sit above ASCII. Equal keys fall back to the raw string so
// the order stays deterministic.
//
// This is a display ordering, not a collation library: the project has no
// third-party dependencies and both shipped languages are covered by the
// same fold that slugify already applies.
func sortLocalized(s []string) {
	sort.SliceStable(s, func(i, j int) bool {
		a, b := collateKey(s[i]), collateKey(s[j])
		if a != b {
			return a < b
		}
		return s[i] < s[j]
	})
}

func collateKey(s string) string { return slug.Fold(s) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
