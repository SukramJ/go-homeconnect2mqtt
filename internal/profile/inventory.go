// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"encoding/json"
	"fmt"
	"os"
)

// InventoryEntry is one appliance's onboarding data extracted from a profile
// ZIP: the haId plus the transport secrets. It lets the add-on entrypoint
// auto-fill a device from just its haId, so the operator only supplies a name
// and host.
type InventoryEntry struct {
	HaID           string `json:"haId"`
	ConnectionType string `json:"connectionType"`
	PSK64          string `json:"psk64"`
	IV64           string `json:"iv64,omitempty"`
	Type           string `json:"type,omitempty"`
	Brand          string `json:"brand,omitempty"`
	Vib            string `json:"vib,omitempty"`
	DefaultHost    string `json:"defaultHost,omitempty"`
}

// WriteInventory writes the appliance inventory — including the PSK secrets —
// to path with 0600 permissions. It is consumed by the add-on entrypoint to
// auto-fill per-device keys from the profile ZIPs; it stays on the local host
// and is NEVER logged or published (the redaction rule applies to logs/MQTT,
// not to this local runtime file, which mirrors the existing /data/devices.yaml).
func WriteInventory(path string, profiles []*DeviceProfile) error {
	entries := make([]InventoryEntry, 0, len(profiles))
	for _, p := range profiles {
		entries = append(entries, InventoryEntry{
			HaID:           p.HaID,
			ConnectionType: string(p.ConnectionType),
			PSK64:          p.PSK64,
			IV64:           p.IV64,
			Type:           p.Type,
			Brand:          p.Brand,
			Vib:            p.Vib,
			DefaultHost:    p.DefaultHost(),
		})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("profile: marshal inventory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("profile: write inventory %s: %w", path, err)
	}
	return nil
}
