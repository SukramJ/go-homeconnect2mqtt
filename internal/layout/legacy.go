// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package layout

import "strings"

// This file is the topic layout every release before 0.15.0 published:
//
//	<root>/status                                   online/offline, the Last Will
//	<root>/<device name>/<Feature/Path>/state       a feature's value
//	<root>/<device name>/<Feature/Path>/set         a feature write
//	<root>/<device name>/availability               online/offline
//	<root>/<device name>/connection_state           the appliance link state
//	<root>/<device name>/_control/<key>/set         a synthetic program control
//
// Nothing publishes it any more. It stays for exactly two readers, both of
// which need the OLD strings rather than the new ones:
//
//   - the migration sweep (internal/bridge/migrate.go), which clears what
//     the old release left retained, and may only ever clear an exact old
//     shape for a device name this instance is configured with;
//   - the attribution of retained discovery configs and device documents
//     (internal/hass's IsOwnConfig), because every config an old release
//     published names these topics, and the orphan sweep and the tombstone
//     read-back must keep recognising them as this instance's own.
//
// The device segment is the operator's device name, raw — which is exactly
// why 0.15.0 moved it to the haId.

// LegacyBridge is the pre-0.15.0 daemon status topic, `<root>/status`.
//
// Under the new grammar `<name>/status` is a function prefix and never a
// topic of its own, so the exact two-level string is unambiguous.
func LegacyBridge(root string) string { return trimRoot(root) + "/status" }

// LegacyDevice is the pre-0.15.0 topic layout of one appliance.
type LegacyDevice struct{ base string }

// NewLegacyDevice builds the old layout for device under root.
func NewLegacyDevice(root, device string) LegacyDevice {
	return LegacyDevice{base: trimRoot(root) + "/" + device}
}

func trimRoot(root string) string { return strings.TrimRight(root, "/") }

// Base is the device sub-tree prefix, "<root>/<device>".
func (d LegacyDevice) Base() string { return d.base }

// Availability is the old online/offline topic.
func (d LegacyDevice) Availability() string { return d.base + "/availability" }

// ConnectionState is the old appliance link-state topic.
func (d LegacyDevice) ConnectionState() string { return d.base + "/connection_state" }

// State is the old state topic of a feature.
func (d LegacyDevice) State(feature string, uid int) string {
	return d.base + "/" + legacyFeaturePath(feature, uid) + "/state"
}

// Command is the old command topic of a feature.
func (d LegacyDevice) Command(feature string, uid int) string {
	return d.base + "/" + legacyFeaturePath(feature, uid) + "/set"
}

// ControlCommand is the old command topic of a synthetic program control.
func (d LegacyDevice) ControlCommand(key string) string {
	return d.base + "/" + ControlPath(key) + "/set"
}

// legacyFeaturePath is the old feature path: dots to slashes and nothing
// else, which is what every pre-0.15.0 topic carries.
func legacyFeaturePath(name string, uid int) string {
	return strings.Join(FeatureItem(name, uid), "/")
}
