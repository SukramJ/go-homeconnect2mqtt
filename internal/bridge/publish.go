// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package bridge orchestrates the Home Connect <-> MQTT mirror: one
// isolated worker per appliance connects, mirrors every feature to MQTT
// state topics and (P7) applies write commands. mirrors the coordinator of
// the sister project go-mtec2mqtt.
package bridge

import (
	"encoding/json"
	"strconv"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// availability payload values. Spelled once in internal/hass, because the
// entity that reads this topic is told there which two words to expect
// (F1); a third spelling here could drift from it silently.
const (
	availOnline  = hass.PayloadAvailable
	availOffline = hass.PayloadNotAvailable
)

// payloadNone is Home Assistant's "no value" payload: an mqtt select clears its
// selection and an enum sensor goes unknown. An empty payload would only be
// ignored, leaving the last value in place.
const payloadNone = "None"

// deviceTopics is this package's view of the shared topic layout in
// internal/layout. It adds only the entity-typed convenience the workers
// use; the strings themselves are composed in exactly one place, which is
// the point of the layout package (F3).
type deviceTopics struct {
	layout.Device
}

func newDeviceTopics(rootTopic, device string) deviceTopics {
	return deviceTopics{Device: layout.NewDevice(rootTopic, device)}
}

func (t deviceTopics) state(e *homeconnect.Entity) string {
	return t.State(e.Name(), e.UID())
}

// isProgramKind reports whether the entry is the active/selected program.
func isProgramKind(k profile.EntryKind) bool {
	return k == profile.KindActiveProgram || k == profile.KindSelectedProgram
}

// payloadFor renders an entity's display value as an MQTT payload.
func payloadFor(e *homeconnect.Entity, lang string) string {
	v := e.Value()
	if v == nil {
		return ""
	}
	// An active/selected program reported as a raw uid — idle (uid 0) or a
	// program the profile does not name — publishes "None" so the program
	// select/sensor clears instead of rejecting a value that is not one of its
	// options. The raw uid survives as a string when the element carries no
	// type, so an unresolved enum member counts as raw here too.
	if isProgramKind(e.Desc.Kind) {
		s, ok := v.(string)
		if !ok || (e.Desc.IsEnum() && !e.HasEnumName(s)) {
			return payloadNone
		}
	}
	switch t := v.(type) {
	case string:
		if e.Desc.IsEnum() {
			return i18n.EnumLabel(t, lang) // localized dropdown/enum value
		}
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		// Object values (parsed JSON) are re-marshalled.
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return ""
	}
}
