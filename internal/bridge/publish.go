// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package bridge orchestrates the Home Connect <-> MQTT mirror: one
// isolated worker per appliance connects, mirrors every feature to MQTT
// state topics and (P7) applies write commands. mirrors the coordinator of
// the sister project go-mtec2mqtt.
package bridge

import (
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// payloadNone is Home Assistant's "no value" state: an mqtt select clears its
// selection and an enum sensor goes unknown when its value template renders
// it. It is published as the `val` of a program item that has no program to
// show — see [statusValue].
const payloadNone = "None"

// deviceTopics is this package's view of the shared topic layout in
// internal/layout. It adds only the entity-typed convenience the workers
// use; the strings themselves are composed in exactly one place, which is
// the point of the layout package (F3).
type deviceTopics struct {
	layout.Device
}

func newDeviceTopics(inst layout.Instance, haID string) deviceTopics {
	return deviceTopics{Device: inst.Device(haID)}
}

func (t deviceTopics) state(e *homeconnect.Entity) string {
	return t.State(e.Name(), e.UID())
}

// isProgramKind reports whether the entry is the active/selected program.
func isProgramKind(k profile.EntryKind) bool {
	return k == profile.KindActiveProgram || k == profile.KindSelectedProgram
}

// statusValue is an entity's value as the `val` of its status object
// (mqtt-smarthome 2.0 §5.2): a JSON boolean, a JSON number, a string, or the
// structured value an Object feature carries. Nil means "no value", which
// the plane publishes as an empty retained payload.
//
// An enum carries its TOKEN — the member name the appliance speaks, e.g.
// "BSH.Common.EnumType.PowerState.On" — and never the localized label it
// used to: a label changes with LANGUAGE and is not what a write has to
// carry back (openccu-loom ADR 0083). The labels live in the discovery
// payload, which maps between the two for Home Assistant.
func statusValue(e *homeconnect.Entity) any {
	v := e.Value()
	if v == nil {
		return nil
	}
	// An active/selected program reported as a raw uid — idle (uid 0) or a
	// program the profile does not name — publishes "None" so the program
	// select/sensor clears instead of rejecting a value that is not one of its
	// options. The raw uid survives as a string when the element carries no
	// type, so an unresolved enum member counts as raw here too.
	//
	// "None" rather than an empty payload, because an empty retained payload
	// renders nothing through the value template and Home Assistant keeps the
	// last program on display; and rather than `{"val":null}`, which the
	// status object refuses. It is a stable token like every other `val`.
	if isProgramKind(e.Desc.Kind) {
		s, ok := v.(string)
		if !ok || (e.Desc.IsEnum() && !e.HasEnumName(s)) {
			return payloadNone
		}
	}
	return v
}
