// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// slog.LogValuer implementations for the secret-bearing profile types.
// Logging one of these structs (e.g. slog.Any("device", cfg)) is safe by
// construction: identifying fields stay in the clear while every secret
// field is masked via Redact (docs/03-profile-format.md §6). Value
// receivers are used so both values and non-nil pointers resolve through
// LogValue.

package profile

import "log/slog"

// Compile-time contract: the secret-bearing types resolve through
// LogValue when handed to slog.
var (
	_ slog.LogValuer = DeviceConfig{}
	_ slog.LogValuer = (*DeviceProfile)(nil)
	_ slog.LogValuer = InventoryEntry{}
)

// LogValue renders a DeviceConfig with its PSK/IV masked.
func (c DeviceConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", c.Name),
		slog.String("host", c.Host),
		slog.String("connectionType", string(c.ConnectionType)),
		slog.String("psk64", Redact(c.PSK64)),
		slog.String("iv64", Redact(c.IV64)),
	)
}

// LogValue renders a DeviceProfile with its PSK/IV masked.
func (p DeviceProfile) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("haId", p.HaID),
		slog.String("connectionType", string(p.ConnectionType)),
		slog.String("brand", p.Brand),
		slog.String("type", p.Type),
		slog.String("vib", p.Vib),
		slog.String("model", p.Model),
		slog.String("psk64", Redact(p.PSK64)),
		slog.String("iv64", Redact(p.IV64)),
	)
}

// LogValue renders an InventoryEntry with its PSK/IV masked.
func (e InventoryEntry) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("haId", e.HaID),
		slog.String("connectionType", e.ConnectionType),
		slog.String("type", e.Type),
		slog.String("brand", e.Brand),
		slog.String("vib", e.Vib),
		slog.String("host", e.DefaultHost),
		slog.String("psk64", Redact(e.PSK64)),
		slog.String("iv64", Redact(e.IV64)),
	)
}
