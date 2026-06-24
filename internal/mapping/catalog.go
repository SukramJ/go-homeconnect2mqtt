// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package mapping loads the optional, operator-patchable enrichment
// catalogue (mapping.yaml). It augments features with Home Assistant
// device_class/unit hints without affecting the generic exposure of every
// feature. Loading is lenient: a missing file yields an empty catalogue
// and malformed entries are skipped (docs/04-device-mapping.md §6.2).
package mapping

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// FeatureMapping is the per-feature enrichment.
type FeatureMapping struct {
	DeviceClass string `yaml:"device_class"`
	Unit        string `yaml:"unit"`
}

// Catalog holds enrichment keyed by feature name.
type Catalog struct {
	features map[string]FeatureMapping
}

type catalogFile struct {
	Features map[string]FeatureMapping `yaml:"features"`
}

// Empty returns an empty catalogue (no enrichment).
func Empty() *Catalog { return &Catalog{features: map[string]FeatureMapping{}} }

// Load reads mapping.yaml leniently. A non-existent path is not an error
// (returns an empty catalogue); only a malformed file fails.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		if os.IsNotExist(err) {
			return Empty(), nil
		}
		return nil, fmt.Errorf("mapping: read %s: %w", path, err)
	}
	var cf catalogFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("mapping: parse %s: %w", path, err)
	}
	if cf.Features == nil {
		cf.Features = map[string]FeatureMapping{}
	}
	return &Catalog{features: cf.Features}, nil
}

// DeviceClass returns the operator-configured device_class for a feature.
func (c *Catalog) DeviceClass(feature string) (string, bool) {
	m, ok := c.features[feature]
	if !ok || m.DeviceClass == "" {
		return "", false
	}
	return m.DeviceClass, true
}

// Unit returns the operator-configured unit for a feature.
func (c *Catalog) Unit(feature string) (string, bool) {
	m, ok := c.features[feature]
	if !ok || m.Unit == "" {
		return "", false
	}
	return m.Unit, true
}

// Len reports how many features carry enrichment.
func (c *Catalog) Len() int { return len(c.features) }
