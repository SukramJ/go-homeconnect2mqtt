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
	"strconv"
	"strings"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
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

// Device classes Home Assistant accepts per platform. HA validates a discovery
// config strictly and discards the WHOLE entity when device_class is not one of
// them, so a class derived from the content type (or set by the operator
// catalogue) is filtered against the platform it lands on before publishing.
var (
	binarySensorClasses = classSet("battery", "battery_charging", "carbon_monoxide", "cold",
		"connectivity", "door", "garage_door", "gas", "heat", "light", "lock", "moisture",
		"motion", "moving", "occupancy", "opening", "plug", "power", "presence", "problem",
		"running", "safety", "smoke", "sound", "tamper", "update", "vibration", "window")
	switchClasses = classSet("outlet", "switch")
	buttonClasses = classSet("identify", "restart", "update")
)

func classSet(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
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
	if i := strings.LastIndex(n, "."); i >= 0 {
		return n[i+1:]
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
		b := e.Bounds()
		if b.HasMin {
			p["min"] = b.Min
		}
		if b.HasMax {
			p["max"] = b.Max
		}
		if b.HasStep {
			p["step"] = b.Step
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

// deviceClassAllowed reports whether dc may be published on platform. sensor
// and number carry HA's open-ended value classes (the operator catalogue is
// trusted there); the boolean platforms take a small fixed set and select takes
// none at all. `enum` is sensor-only — attaching it to the event binary_sensors
// (both the enum heuristic and the catalogue used to) makes HA reject them.
func deviceClassAllowed(platform, dc string) bool {
	switch platform {
	case platformSensor:
		return true
	case platformNumber:
		return dc != deviceClassEnum
	case platformBinarySensor:
		return binarySensorClasses[dc]
	case platformSwitch:
		return switchClasses[dc]
	case platformButton:
		return buttonClasses[dc]
	default: // select
		return false
	}
}

// sanitizeForPlatform strips attributes the target platform rejects. It runs
// last, after enrichment, so neither the heuristic nor an operator override can
// produce a config Home Assistant refuses to load.
func sanitizeForPlatform(p map[string]any, platform string) {
	if dc, ok := p["device_class"].(string); ok && !deviceClassAllowed(platform, dc) {
		delete(p, "device_class")
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

// umlautReplacer transliterates German umlauts to match HA's slugify.
var umlautReplacer = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss")

// slugify lowercases, transliterates umlauts and reduces any run of
// non-alphanumeric characters to a single underscore (HA-compatible).
func slugify(s string) string {
	s = umlautReplacer.Replace(strings.ToLower(s))
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// sanitize is slugify kept under its historical name for the device id prefix.
func sanitize(s string) string { return slugify(s) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
