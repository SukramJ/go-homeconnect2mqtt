// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"log/slog"
	"strings"
)

// secretKeys are the fields that must never be logged or published in
// clear text (docs/03-profile-format.md §6). Keys are stored in
// normalized form (lower-case, separators stripped) so that common
// casings such as "deviceId", "device_id", "serial-number" or "PSK64"
// all match the same entry.
var secretKeys = map[string]bool{
	"psk": true, "key": true, "psk64": true,
	"iv": true, "aesiv": true, "iv64": true,
	"deviceid": true, "serialnumber": true, "shipski": true, "mac": true,
}

// normalizeKey folds a field name to its canonical lookup form:
// lower-case with "_" and "-" separators removed.
func normalizeKey(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "")
	return strings.ReplaceAll(name, "-", "")
}

// IsSecretKey reports whether a field name holds a secret value. The
// match is case-insensitive and ignores "_"/"-" separators, so all
// common casings of the contract keys (psk/iv/serialNumber/mac/
// shipSki/deviceID, docs/03-profile-format.md §6) are covered.
func IsSecretKey(name string) bool {
	if secretKeys[name] {
		return true
	}
	// Fast path for the logging hot path: a key that is already in
	// canonical form (all lower-case, no separators) missed above and
	// cannot match after normalization either — skip the allocating
	// ToLower/ReplaceAll calls.
	if !needsNormalization(name) {
		return false
	}
	return secretKeys[normalizeKey(name)]
}

// needsNormalization reports whether name contains an upper-case letter or
// a "_"/"-" separator, i.e. whether normalizeKey could change it.
func needsNormalization(name string) bool {
	for i := range len(name) {
		c := name[i]
		if (c >= 'A' && c <= 'Z') || c == '_' || c == '-' {
			return true
		}
	}
	return false
}

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

// RedactAttr is a slog.HandlerOptions.ReplaceAttr function that masks
// every attribute whose key names a secret field (see IsSecretKey),
// regardless of the value's kind — a MAC logged as an int must not leak
// either. Installing it on a binary's root handler structurally enforces
// the redaction contract of docs/03-profile-format.md §6: even an ad-hoc
// slog.String("psk64", v) can no longer leak the value.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindGroup && IsSecretKey(a.Key) {
		a.Value = slog.StringValue(Redact(a.Value.Resolve().String()))
	}
	return a
}
