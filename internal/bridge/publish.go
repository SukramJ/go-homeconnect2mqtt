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
	"strings"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// availability payload values.
const (
	availOnline  = "online"
	availOffline = "offline"
)

// deviceTopics holds the precomputed topic prefixes for a device.
type deviceTopics struct {
	base string
}

func newDeviceTopics(rootTopic, device string) deviceTopics {
	return deviceTopics{base: strings.TrimRight(rootTopic, "/") + "/" + device}
}

func (t deviceTopics) availability() string    { return t.base + "/availability" }
func (t deviceTopics) connectionState() string { return t.base + "/connection_state" }
func (t deviceTopics) state(e *homeconnect.Entity) string {
	return t.base + "/" + featurePath(e.Name(), e.UID()) + "/state"
}

// featurePath maps a dotted feature name to a slash-separated MQTT path.
// Unnamed features fall back to a uid-based path so nothing is lost (FK-8).
func featurePath(name string, uid int) string {
	if name == "" {
		return "_uid/" + strconv.Itoa(uid)
	}
	return strings.ReplaceAll(name, ".", "/")
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
	// An active/selected program reported as a raw uid (idle, or an unknown
	// program) publishes empty so a program select shows "no selection".
	if isProgramKind(e.Desc.Kind) {
		if _, ok := v.(string); !ok {
			return ""
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
