// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package hass generates Home Assistant MQTT discovery payloads from the
// appliance entity model. It maps every feature to a platform via a
// heuristic (docs/04-device-mapping.md §1) and emits one config payload
// per entity, plus birth/LWT re-publish handling.
//
// Entity ids are seeded English and language-independent via
// `default_entity_id` (the replacement for the removed `object_id`), while the
// friendly `name` is localized. Most of the long tail of features is published
// disabled-by-default and categorized as diagnostic/config so Home Assistant
// stays uncluttered without dropping the "expose everything" promise.
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

// Entity categories (Home Assistant).
const (
	categoryDiagnostic = "diagnostic"
	categoryConfig     = "config"
)

// classify maps an entity to a Home Assistant platform. ok is false when
// the entity should not be exposed via discovery (e.g. a raw program node).
func classify(e *homeconnect.Entity) (platform string, ok bool) {
	switch e.Desc.Kind {
	case profile.KindCommand:
		return platformButton, true
	case profile.KindEvent:
		return platformBinarySensor, true
	case profile.KindActiveProgram, profile.KindSelectedProgram:
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
	case profile.KindSetting:
		if e.Desc.Writable() {
			return categoryConfig
		}
		return categoryDiagnostic
	case profile.KindStatus, profile.KindOption, profile.KindEvent, profile.KindCommand:
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
		return "enum", ""
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
	p := map[string]any{
		"unique_id":          dev.idPrefix + "_" + featureKey(e),
		"name":               humanize(e),
		"default_entity_id":  platform + "." + slugify(device+"_"+featureKey(e)),
		"state_topic":        t.state,
		"availability_topic": t.availability,
		"device":             dev.block,
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
