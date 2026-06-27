// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package mapping loads the optional, operator-patchable enrichment
// catalogue (mapping.yaml). It augments features with Home Assistant hints
// (localized names, device_class/unit, state_class, entity_category,
// enabled-by-default, exclusion) without affecting the generic exposure of
// every feature. Loading is lenient: a missing file yields an empty
// catalogue and malformed entries are skipped (docs/04-device-mapping.md §6.2).
package mapping

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// FeatureMapping is the per-feature enrichment. Every field is optional; an
// unset field means "fall back to the discovery heuristic".
type FeatureMapping struct {
	Name             string `yaml:"name"`               // English friendly name
	NameDE           string `yaml:"name_de"`            // German friendly name
	DeviceClass      string `yaml:"device_class"`       // HA device_class
	Unit             string `yaml:"unit"`               // unit_of_measurement
	StateClass       string `yaml:"state_class"`        // measurement|total|total_increasing
	EntityCategory   string `yaml:"entity_category"`    // diagnostic|config (empty = primary)
	EnabledByDefault *bool  `yaml:"enabled_by_default"` // tri-state: nil = heuristic
	Exclude          bool   `yaml:"exclude"`            // never expose this feature
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
	return field(c, feature, func(m FeatureMapping) string { return m.DeviceClass })
}

// Unit returns the operator-configured unit for a feature.
func (c *Catalog) Unit(feature string) (string, bool) {
	return field(c, feature, func(m FeatureMapping) string { return m.Unit })
}

// StateClass returns the configured state_class for a feature.
func (c *Catalog) StateClass(feature string) (string, bool) {
	return field(c, feature, func(m FeatureMapping) string { return m.StateClass })
}

// EntityCategory returns the configured entity_category for a feature.
func (c *Catalog) EntityCategory(feature string) (string, bool) {
	return field(c, feature, func(m FeatureMapping) string { return m.EntityCategory })
}

// LocalizedName returns the configured friendly name for a feature in lang
// ("de" prefers name_de, falling back to the English name).
func (c *Catalog) LocalizedName(feature, lang string) (string, bool) {
	m, ok := c.features[feature]
	if !ok {
		return "", false
	}
	if lang == "de" && m.NameDE != "" {
		return m.NameDE, true
	}
	if m.Name != "" {
		return m.Name, true
	}
	return "", false
}

// EnabledByDefault returns the configured tri-state. val is meaningful only
// when ok is true.
func (c *Catalog) EnabledByDefault(feature string) (val, ok bool) {
	m, found := c.features[feature]
	if !found || m.EnabledByDefault == nil {
		return false, false
	}
	return *m.EnabledByDefault, true
}

// Excluded reports whether the feature is marked exclude: true.
func (c *Catalog) Excluded(feature string) bool {
	return c.features[feature].Exclude
}

// Len reports how many features carry enrichment.
func (c *Catalog) Len() int { return len(c.features) }

func field(c *Catalog, feature string, pick func(FeatureMapping) string) (string, bool) {
	m, ok := c.features[feature]
	if !ok || pick(m) == "" {
		return "", false
	}
	return pick(m), true
}
