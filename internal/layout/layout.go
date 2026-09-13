// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package layout is the single source of this daemon's MQTT topic layout.
//
// It exists because the layout used to be composed in six places across
// three packages, from two separate featurePath implementations and two
// separate spellings of the synthetic program-control path:
//
//	internal/hass/discovery.go    featurePath(e) + topicsFor()   -> state_topic, command_topic, availability_topic
//	internal/hass/discovery.go    publishProgramControls()       -> "_control/<key>/set", availability, inline
//	internal/bridge/publish.go    featurePath(name, uid) + deviceTopics
//	internal/bridge/command.go    the "/#" subscribe filter
//	internal/bridge/command.go    handleSet()/resolveEntity(), the INVERSE of featurePath
//	internal/bridge/command.go    controlStartProgram/controlStopProgram, the inverse of the second spelling
//	cmd/homeconnect2mqtt/main.go  the bridge status topic the Last Will writes
//
// The first two of those feed the retained discovery config — what Home
// Assistant is told to read and write — and the rest are where the daemon
// actually reads and writes. A divergence between any advertising site and
// its acting counterpart is silent in every direction: the entity points at
// a topic nobody writes (permanently `unknown`) or a button writes to a
// topic nobody handles (a press that does nothing), with nothing in the log
// and nothing in Home Assistant's registry to notice. See
// notes/adr0070-phase7-measurement.md, findings F1, F3 and F6.
//
// Every function here is a pure function of the bridge root, the operator's
// device name and the feature name. Nothing is slugified: the state and
// command tree preserves the device name and the feature's original dotted
// casing exactly as it has always been published. The discovery *config*
// topic, which does slugify the device name, is not part of this layout —
// it is identity-bearing and belongs to internal/hass (see F2).
//
// At ADR 0070 phase 7 step 4 this package is what a go-hamqtt topic.Layout
// will be written against.
package layout

import (
	"strconv"
	"strings"
)

// Synthetic program-control keys. They back no appliance feature: the
// discovery layer publishes a button whose command_topic is
// Device.ControlCommand(key), and the command handler recognises the
// matching Device.Relative() result. Spelling them once is the point.
const (
	ControlStartProgram = "start_program"
	ControlStopProgram  = "stop_program"
)

// controlPrefix namespaces the synthetic controls away from the feature
// tree, which is composed from dotted feature names and so can never
// produce a segment starting with an underscore.
const controlPrefix = "_control"

// unnamedPrefix is the path segment for a feature the appliance
// description does not name; the uid follows (FK-8).
const unnamedPrefix = "_uid"

// Bridge is the daemon-level status topic: the one the Last Will writes
// and the one OnConnect writes "online" to.
//
// It is a single function so the will/birth publisher (cmd/…/main.go) and
// the discovery builder (internal/hass) cannot drift. Before F1 was fixed
// only the former knew this topic existed, so the Last Will landed on a
// topic no entity referenced and a killed daemon left every entity showing
// its last value forever.
//
// It is bridge-level, not device-level: this daemon mirrors several
// appliances over one broker connection, so there is no device to put it
// under. (The sibling go-mtec2mqtt had the inverse constraint — one device,
// but its status went out at CONNECT before its serial was known.)
func Bridge(root string) string { return trimRoot(root) + "/status" }

// Device is the topic layout of one appliance under the bridge root.
type Device struct{ base string }

// NewDevice builds the layout for device under root.
func NewDevice(root, device string) Device {
	return Device{base: trimRoot(root) + "/" + device}
}

func trimRoot(root string) string { return strings.TrimRight(root, "/") }

// Base is the device sub-tree prefix, "<root>/<device>".
func (d Device) Base() string { return d.base }

// Availability is where the device worker writes online/offline, and what
// every discovery payload declares as its device-level availability.
func (d Device) Availability() string { return d.base + "/availability" }

// ConnectionState is the appliance link state. It is published retained
// and referenced by no discovery payload (F7, deliberately left).
func (d Device) ConnectionState() string { return d.base + "/connection_state" }

// State is where a feature's value is published and what the discovery
// payload advertises as state_topic.
func (d Device) State(feature string, uid int) string {
	return d.base + "/" + FeaturePath(feature, uid) + "/state"
}

// Command is where a feature is written and what the discovery payload
// advertises as command_topic.
func (d Device) Command(feature string, uid int) string {
	return d.base + "/" + FeaturePath(feature, uid) + "/set"
}

// ControlCommand is the command_topic of a synthetic program control.
func (d Device) ControlCommand(key string) string {
	return d.base + "/" + ControlPath(key) + "/set"
}

// CommandFilter is the subscription that covers every command topic of
// this device. The feature path is variable-depth, so no fixed-arity
// filter fits; see F4 for what the daemon does about the echo this
// implies.
func (d Device) CommandFilter() string { return d.base + "/#" }

// Relative strips the device prefix and the "/set" suffix from an inbound
// command topic, yielding the value ControlPath or FeaturePath produced.
// ok is false when topic is not a command topic of this device.
func (d Device) Relative(topic string) (rel string, ok bool) {
	rest, ok := strings.CutPrefix(topic, d.base+"/")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, "/set")
	if !ok {
		return "", false
	}
	return rest, true
}

// ControlPath is the relative path of a synthetic control.
func ControlPath(key string) string { return controlPrefix + "/" + key }

// FeaturePath maps a dotted feature name to its slash-separated MQTT path.
// An unnamed feature falls back to a uid-based path so nothing is lost.
func FeaturePath(name string, uid int) string {
	if name == "" {
		return unnamedPrefix + "/" + strconv.Itoa(uid)
	}
	return strings.ReplaceAll(name, ".", "/")
}

// FeatureName is FeaturePath's inverse: it maps a relative path back to
// either a dotted feature name or a uid. Exactly one of the two results is
// meaningful, as reported by byUID.
func FeatureName(rel string) (name string, uid int, byUID bool) {
	if s, ok := strings.CutPrefix(rel, unnamedPrefix+"/"); ok {
		n, err := strconv.Atoi(s)
		if err != nil {
			return "", 0, false
		}
		return "", n, true
	}
	return strings.ReplaceAll(rel, "/", "."), 0, false
}
