// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

const (
	rawPSK = "c2VjcmV0UFNLNjR2YWx1ZQ=="
	rawIV  = "c2VjcmV0SVY2NHZhbHVl"
)

// newBufLogger returns a text logger writing to buf, optionally with the
// RedactAttr guard installed.
func newBufLogger(buf *bytes.Buffer, redact bool) *slog.Logger {
	opts := &slog.HandlerOptions{}
	if redact {
		opts.ReplaceAttr = RedactAttr
	}
	return slog.New(slog.NewTextHandler(buf, opts))
}

// TestLogValueMasksSecrets checks that logging the secret-bearing structs
// via slog never emits the raw psk64/iv64 while the identifying fields
// stay readable (docs/03-profile-format.md §6).
func TestLogValueMasksSecrets(t *testing.T) {
	cases := []struct {
		name  string
		value any
		clear []string // identifying fields expected in the output
	}{
		{
			name: "DeviceConfig value",
			value: DeviceConfig{
				Name: "dishwasher", Host: "192.0.2.10",
				ConnectionType: ConnectionAES, PSK64: rawPSK, IV64: rawIV,
			},
			clear: []string{"dishwasher", "192.0.2.10", "AES"},
		},
		{
			name: "DeviceProfile pointer",
			value: &DeviceProfile{
				HaID: "BOSCH-SMV4HCX48E-68A40E123456", ConnectionType: ConnectionTLS,
				Brand: "BOSCH", Type: "Dishwasher", PSK64: rawPSK, IV64: rawIV,
			},
			clear: []string{"BOSCH-SMV4HCX48E-68A40E123456", "Dishwasher", "TLS"},
		},
		{
			name: "InventoryEntry value",
			value: InventoryEntry{
				HaID: "SIEMENS-WM14XYZ-0123", ConnectionType: "AES",
				Type: "Washer", DefaultHost: "siemens-washer.local",
				PSK64: rawPSK, IV64: rawIV,
			},
			clear: []string{"SIEMENS-WM14XYZ-0123", "Washer", "siemens-washer.local"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			// No ReplaceAttr guard: the LogValuer alone must mask.
			newBufLogger(&buf, false).Info("device", slog.Any("device", tc.value))
			out := buf.String()
			for _, raw := range []string{rawPSK, rawIV} {
				if strings.Contains(out, raw) {
					t.Errorf("output leaks raw secret %q: %s", raw, out)
				}
			}
			for _, masked := range []string{Redact(rawPSK), Redact(rawIV)} {
				if !strings.Contains(out, masked) {
					t.Errorf("output missing masked form %q: %s", masked, out)
				}
			}
			for _, want := range tc.clear {
				if !strings.Contains(out, want) {
					t.Errorf("output missing identifying field %q: %s", want, out)
				}
			}
		})
	}
}

// TestRedactAttrMasksSecretKeys checks the handler-level guard: ad-hoc
// string attrs with secret-looking keys are masked, everything else is
// passed through untouched.
func TestRedactAttrMasksSecretKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := newBufLogger(&buf, true)
	logger.Info(
		"connect",
		slog.String("device", "dishwasher"),
		slog.String("psk64", rawPSK),
		slog.String("serialNumber", "010203040506000123"),
		slog.String("host", "192.0.2.10"),
		slog.Int("mac", 421234), // non-string secret key: masked too
	)
	out := buf.String()

	for _, raw := range []string{rawPSK, "010203040506000123", "421234"} {
		if strings.Contains(out, raw) {
			t.Errorf("output leaks raw secret %q: %s", raw, out)
		}
	}
	for _, want := range []string{Redact(rawPSK), Redact("010203040506000123"), Redact("421234"), "dishwasher", "192.0.2.10"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}

// TestIsSecretKeyCasings checks that all common casings of the contract
// keys (psk/iv/serialNumber/mac/shipSki/deviceID) are recognized.
func TestIsSecretKeyCasings(t *testing.T) {
	secret := []string{
		"psk", "PSK", "psk64", "PSK64", "key",
		"iv", "IV", "iv64", "IV64", "aes_iv", "aesIv",
		"deviceID", "deviceId", "device_id", "DeviceID",
		"serialNumber", "serialnumber", "serial_number", "SerialNumber",
		"shipSki", "ship_ski", "ShipSki",
		"mac", "MAC", "Mac",
	}
	for _, k := range secret {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false, want true", k)
		}
	}
	public := []string{"haId", "name", "host", "brand", "type", "vib", "model", "connectionType"}
	for _, k := range public {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true, want false", k)
		}
	}
}
