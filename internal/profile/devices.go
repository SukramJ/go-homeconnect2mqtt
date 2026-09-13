// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// DeviceConfig is one entry of the operator-maintained devices file
// (docs/06-architecture.md §4).
type DeviceConfig struct {
	Name           string         `yaml:"name"`
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

// validateDeviceName rejects a device name that cannot safely become an
// MQTT topic segment or a Home Assistant node id.
//
// The name is the operator's, and it goes into the topic tree twice, in
// two different forms (F2 of notes/adr0070-phase7-measurement.md): raw
// into every state, command and availability topic and into the device's
// "<root>/<name>/#" command subscription, and slugified into the
// discovery config topic's node id. Nothing validated it.
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
