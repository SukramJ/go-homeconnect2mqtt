// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/slug"
)

// DeviceConfig is one entry of the operator-maintained devices file
// (docs/06-architecture.md §4).
type DeviceConfig struct {
	Name string `yaml:"name"`
	// HaID is the appliance's Home Connect id, the device segment of every
	// MQTT topic since 0.15.0. Optional when the cached description records
	// it (hc-util parse writes it there); see [ResolveHaID].
	HaID           string         `yaml:"haid"`
	Host           string         `yaml:"host"`
	ManualHost     bool           `yaml:"manual_host"`
	ConnectionType ConnectionType `yaml:"connection_type"`
	PSK64          string         `yaml:"psk64"`
	IV64           string         `yaml:"iv64"`
	Description    string         `yaml:"description"` // path to a cached description JSON
}

type devicesFile struct {
	Devices []DeviceConfig `yaml:"devices"`
}

// LoadDevices reads and validates the devices file.
func LoadDevices(path string) ([]DeviceConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("profile: read devices %s: %w", path, err)
	}
	var df devicesFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return nil, fmt.Errorf("profile: parse devices %s: %w", path, err)
	}
	if len(df.Devices) == 0 {
		return nil, fmt.Errorf("profile: devices file %s has no devices", path)
	}
	seen := map[string]bool{}
	// The SECOND uniqueness rule, and the one that costs entities when it
	// is missing. Two names that are merely different are fine; two names
	// that FOLD to one discovery node id are not, because hass.Discovery
	// derives the node id with slug.Slug, and the fold is many-to-one:
	// "My Oven" and "my-oven" are both "my_oven".
	//
	// Everything downstream of the node id then collides — one retained
	// device-document topic, one `unique_id` namespace, and one key in
	// bridge.priorDocuments, which is what the tombstone read-back is held
	// under. Each appliance's pass reads the OTHER one's document as its
	// own previous state and tombstones every component the other declares
	// and it does not; the other pass does the same back, on every
	// connection. Before tombstones existed this collision was confusing
	// but inert, which is why the guard checked the raw name.
	byNodeID := map[string]string{}
	for i := range df.Devices {
		d := &df.Devices[i]
		d.ConnectionType = ConnectionType(strings.ToUpper(string(d.ConnectionType)))
		if d.Name == "" {
			return nil, fmt.Errorf("profile: device #%d has no name", i)
		}
		if err := validateDeviceName(d.Name); err != nil {
			return nil, fmt.Errorf("profile: device #%d: %w", i, err)
		}
		if seen[d.Name] {
			return nil, fmt.Errorf("profile: duplicate device name %q", d.Name)
		}
		seen[d.Name] = true
		node := slug.Slug(d.Name)
		if first, clash := byNodeID[node]; clash {
			return nil, fmt.Errorf(
				"profile: device names %q and %q both become Home Assistant node id %q: "+
					"they would share one discovery document, one unique_id namespace and one "+
					"removal memory, and would delete each other's entities on every pass",
				first, d.Name, node,
			)
		}
		byNodeID[node] = d.Name
		if d.HaID != "" && !ValidHaID(d.HaID) {
			return nil, fmt.Errorf("profile: device %q has an invalid haid %q", d.Name, d.HaID)
		}
		if d.ConnectionType != ConnectionAES && d.ConnectionType != ConnectionTLS {
			return nil, fmt.Errorf("profile: device %q has invalid connection_type %q", d.Name, d.ConnectionType)
		}
		if d.PSK64 == "" {
			return nil, fmt.Errorf("profile: device %q is missing psk64", d.Name)
		}
		if d.ConnectionType == ConnectionAES && d.IV64 == "" {
			return nil, fmt.Errorf("profile: AES device %q is missing iv64", d.Name)
		}
	}
	return df.Devices, nil
}

// SaveDescriptionJSON writes a parsed description to a cache file.
func SaveDescriptionJSON(path string, d *Description) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("profile: marshal description: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("profile: write description %s: %w", path, err)
	}
	return nil
}

// LoadDescriptionJSON reads a cached description and rebuilds its indexes.
func LoadDescriptionJSON(path string, logger *slog.Logger) (*Description, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("profile: read description %s: %w", path, err)
	}
	var d Description
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrParser, path, err)
	}
	d.rebuildIndex()
	_ = logger
	return &d, nil
}

// ErrNoHaID is returned by [ResolveHaID] for a device whose haId is known
// from none of its sources.
var ErrNoHaID = errors.New("profile: device has no haid")

// HaIDSource says where [ResolveHaID] found a device's haId.
type HaIDSource int

// The sources, in the order they are consulted.
const (
	// HaIDFromConfig is the `haid` key of the devices.yaml entry.
	HaIDFromConfig HaIDSource = iota + 1
	// HaIDFromDescription is the haId hc-util parse 0.15.0 and later
	// record in the cached description.
	HaIDFromDescription
	// HaIDFromFileName is the cached description's file name without its
	// extension. hc-util parse has always written the cache as
	// `<out>/<haId>.json` and printed `description: <out>/<haId>.json`, so
	// on the documented setup path — and in the add-on, which builds the
	// path the same way — this IS the haId. It is the only source every
	// installation from before 0.15.0 has, and without it every one of them
	// would refuse to start on upgrade. A renamed file yields whatever name
	// it was given, which is why the caller says so.
	HaIDFromFileName
)

// ResolveHaID is the appliance's haId, the device segment of every MQTT
// topic, and where it came from: the `haid` from devices.yaml, else the one
// the cached description recorded, else the description's file name when
// that is a valid haId (see [HaIDFromFileName]).
//
// There is deliberately no fallback to the device name: a name-derived
// segment would move every topic again the day the haId is filled in. A
// device with no usable source is refused at start, with what to do about
// it — see [ErrNoHaID].
func ResolveHaID(dc DeviceConfig, desc *Description) (string, HaIDSource, error) {
	switch {
	case dc.HaID != "":
		if !ValidHaID(dc.HaID) {
			return "", 0, fmt.Errorf("profile: device %q has an invalid haid %q", dc.Name, dc.HaID)
		}
		return dc.HaID, HaIDFromConfig, nil
	case desc != nil && desc.HaID != "":
		if !ValidHaID(desc.HaID) {
			return "", 0, fmt.Errorf("profile: device %q: its description records an invalid haid %q", dc.Name, desc.HaID)
		}
		return desc.HaID, HaIDFromDescription, nil
	}
	if base := filepath.Base(dc.Description); dc.Description != "" {
		if id := strings.TrimSuffix(base, filepath.Ext(base)); id != "" && ValidHaID(id) {
			return id, HaIDFromFileName, nil
		}
	}
	return "", 0, fmt.Errorf("%w: device %q — add `haid: <haId>` to its devices.yaml entry "+
		"(the haId is the name of its <haId>.json in the profile archive), or re-run "+
		"`hc-util parse` so its cached description records it", ErrNoHaID, dc.Name)
}

// validateDeviceName rejects a device name that cannot safely become a
// Home Assistant node id, or that earlier releases could not put into a
// topic.
//
// The name is the operator's. Since 0.15.0 it is in no MQTT topic any more
// — the haId is the device segment — but it is still slugified into the
// discovery config topic's node id and the device identifiers (F2 of
// notes/adr0070-phase7-measurement.md), and the migration sweep still
// rebuilds the topics an earlier release published under it. The rules
// below are the ones those releases enforced, kept unchanged so a
// devices.yaml that started before starts now.
//
// Non-ASCII is fine and is deliberately allowed — MQTT topic names are
// UTF-8 (§1.5.4) and this project's default language is German, so
// "Geschirrspüler" is the expected case, not an edge one. What is not
// fine:
//
//   - "+" and "#" are wildcards. In the subscription "<root>/<name>/#"
//     they are not escaped, so a device named "#" would subscribe the
//     daemon to every topic on the broker and a device named "+" would
//     swallow every sibling appliance's command tree — including
//     re-dispatching another appliance's writes onto this one.
//   - A control character or a non-UTF-8 byte is a protocol violation
//     (§1.5.4) that a strict broker rejects at CONNECT — after the
//     daemon has already reported itself healthy.
//   - A name with no ASCII alphanumeric slugifies to the empty string,
//     producing a discovery topic with an empty node id
//     ("homeassistant/sensor//x/config") and a device identifier of
//     "homeconnect_".
//
// It does NOT reconcile the raw/slugified asymmetry itself. Both forms
// are load-bearing: the slug is half of every unique_id and of
// device.identifiers, and the raw name is in every topic an installed
// base already subscribes to. Neither can move without re-keying Home
// Assistant's registries or orphaning retained state, and that decision
// belongs to the migration, not to a validation function.
func validateDeviceName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("device name %q is not valid UTF-8; MQTT topic names must be (§1.5.4)", name)
	}
	// "/" is deliberately NOT rejected. It does add levels to the topic
	// tree, which is untidy, but it is functional end to end: the device
	// prefix is matched whole, so Device.Relative still inverts a command
	// topic under it, and the "<root>/<name>/#" subscription still covers
	// exactly that sub-tree. "Waschmaschine / Trockner" is a plausible
	// operator name and an installation using one works today; refusing to
	// start on it would break a working deployment for a nicety.
	for _, bad := range []string{"+", "#"} {
		if strings.Contains(name, bad) {
			return fmt.Errorf("device name %q must not contain %q: it is used unescaped as an MQTT topic segment and in the %q subscription filter",
				name, bad, "<root>/<name>/#")
		}
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("device name %q must not contain control characters; MQTT topic names must not (§1.5.4)", name)
		}
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return nil
		}
	}
	return fmt.Errorf("device name %q contains no ASCII letter or digit, so it slugifies to the empty string: "+
		"the Home Assistant node id and device identifier would both be empty", name)
}
