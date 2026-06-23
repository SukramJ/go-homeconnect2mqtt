// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

// secretKeys are the fields that must never be logged or published in
// clear text (docs/03-profil-format.md §6).
var secretKeys = map[string]bool{
	"psk": true, "key": true, "psk64": true,
	"iv": true, "aes_iv": true, "iv64": true,
	"deviceID": true, "serialNumber": true, "shipSki": true, "mac": true,
}

// IsSecretKey reports whether a field name holds a secret value.
func IsSecretKey(name string) bool { return secretKeys[name] }

// Redact masks a secret value, keeping only enough to confirm presence.
func Redact(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "****"
	}
	return value[:2] + "…" + "****"
}
