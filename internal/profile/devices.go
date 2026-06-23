// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DeviceConfig is one entry of the operator-maintained devices file
// (docs/06-architektur-konzept.md §4).
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
		return nil, fmt.Errorf("%w: %s: %v", ErrParser, path, err)
	}
	d.rebuildIndex()
	_ = logger
	return &d, nil
}
