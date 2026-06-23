// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package hass generates Home Assistant MQTT discovery payloads from the
// appliance entity model. It maps every feature to a platform via a
// heuristic (docs/04-geraete-mapping.md §1) and emits one config payload
// per entity, plus birth/LWT re-publish handling.
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

// payloadFor builds the discovery config payload for an entity on a
// platform. topics supplies the precomputed MQTT topics.
func payloadFor(e *homeconnect.Entity, platform string, t entityTopics, dev deviceBlock) map[string]any {
	deviceClass, unit := deviceClassAndUnit(e)
	p := map[string]any{
		"unique_id":          dev.idPrefix + "_" + featureKey(e),
		"name":               displayName(e),
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
		if minV, hasMin, maxV, hasMax, step, hasStep := e.MinMaxStep(); hasMin || hasMax || hasStep {
			if hasMin {
				p["min"] = minV
			}
			if hasMax {
				p["max"] = maxV
			}
			if hasStep {
				p["step"] = step
			}
		}
	}
	if deviceClass != "" {
		p["device_class"] = deviceClass
	}
	if unit != "" {
		p["unit_of_measurement"] = unit
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
	return sanitize(e.Name())
}

// displayName derives a short, human-friendly name from the feature name.
func displayName(e *homeconnect.Entity) string {
	if e.Name() == "" {
		return "uid " + strconv.Itoa(e.UID())
	}
	parts := strings.Split(e.Name(), ".")
	return parts[len(parts)-1]
}

// sanitize lowercases and replaces non-alphanumerics with underscores.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
