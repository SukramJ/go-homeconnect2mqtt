// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildZipStored is buildZip with method Store (no deflate). The zip-bomb
// tests move tens of MiB; skipping compression keeps them fast under -race.
func buildZipStored(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

const profileJSONAES = `{
  "haId": "0102030405",
  "deviceDescriptionFileName": "0102030405_DeviceDescription.xml",
  "featureMappingFileName": "0102030405_FeatureMapping.xml",
  "connectionType": "AES",
  "key": "c2VjcmV0a2V5",
  "iv": "aXZpdml2aXY",
  "brand": "BOSCH",
  "type": "Dishwasher",
  "vib": "SMV6ZCX01G",
  "model": "DemoModel"
}`

func TestParseArchiveBytes(t *testing.T) {
	zipData := buildZip(t, map[string]string{
		"0102030405.json":                  profileJSONAES,
		"0102030405_DeviceDescription.xml": deviceDescriptionShort,
		"0102030405_FeatureMapping.xml":    featureMappingShort,
	})
	profiles, err := ParseArchiveBytes(zipData, nil)
	if err != nil {
		t.Fatalf("ParseArchiveBytes: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	p := profiles[0]
	if p.HaID != "0102030405" || p.ConnectionType != ConnectionAES {
		t.Errorf("profile header = %+v", p)
	}
	if p.PSK64 != "c2VjcmV0a2V5" || p.IV64 != "aXZpdml2aXY" {
		t.Error("keys not mapped")
	}
	if p.DefaultHost() != "0102030405" {
		t.Errorf("AES default host = %q", p.DefaultHost())
	}
	if _, ok := p.Description.ByName("Demo.Setting.Power"); !ok {
		t.Error("description not parsed from archive")
	}
}

func TestDefaultHostTLS(t *testing.T) {
	p := &DeviceProfile{HaID: "0102030405", ConnectionType: ConnectionTLS, Brand: "BOSCH", Type: "Dishwasher"}
	if got := p.DefaultHost(); got != "BOSCH-Dishwasher-0102030405" {
		t.Errorf("TLS default host = %q", got)
	}
}

func TestParseArchiveMissingXML(t *testing.T) {
	zipData := buildZip(t, map[string]string{"0102030405.json": profileJSONAES})
	_, err := ParseArchiveBytes(zipData, nil)
	if !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("expected ErrInvalidProfile, got %v", err)
	}
}

func TestParseArchiveNoJSON(t *testing.T) {
	zipData := buildZip(t, map[string]string{"readme.txt": "hi"})
	_, err := ParseArchiveBytes(zipData, nil)
	if !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("expected ErrInvalidProfile, got %v", err)
	}
}

func TestParseArchiveNotAZip(t *testing.T) {
	_, err := ParseArchiveBytes([]byte("not a zip"), nil)
	if !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("expected ErrInvalidProfile, got %v", err)
	}
}

// indexJSON builds a minimal device index referencing the shared XML fixtures.
func indexJSON(haID string) string {
	return fmt.Sprintf(`{"haId":%q,"deviceDescriptionFileName":"dd.xml",`+
		`"featureMappingFileName":"fm.xml","connectionType":"AES","key":"a2V5","iv":"aXY"}`, haID)
}

func TestParseArchiveRejectsUnsafeHaID(t *testing.T) {
	cases := []struct {
		name string
		haID string
	}{
		{"parent traversal", "../../../pwn"},
		{"dot", "."},
		{"dotdot", ".."},
		{"forward slash", "a/b"},
		{"backslash", `a\b`},
		{"absolute", "/etc/pwn"},
		{"empty", ""},
		{"space", "ha id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zipData := buildZip(t, map[string]string{
				"bad.json": indexJSON(tc.haID),
				"dd.xml":   deviceDescriptionShort,
				"fm.xml":   featureMappingShort,
			})
			// The unsafe profile is skipped (lenient loading); with nothing
			// left, the archive as a whole is invalid.
			_, err := ParseArchiveBytes(zipData, slog.New(slog.DiscardHandler))
			if !errors.Is(err, ErrInvalidProfile) {
				t.Errorf("haId %q: expected ErrInvalidProfile, got %v", tc.haID, err)
			}
		})
	}
}

func TestParseArchiveSkipsUnsafeHaIDKeepsValid(t *testing.T) {
	zipData := buildZip(t, map[string]string{
		"bad.json":  indexJSON("../evil"),
		"good.json": indexJSON("GOODDEVICE-01"),
		"dd.xml":    deviceDescriptionShort,
		"fm.xml":    featureMappingShort,
	})
	profiles, err := ParseArchiveBytes(zipData, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("ParseArchiveBytes: %v", err)
	}
	if len(profiles) != 1 || profiles[0].HaID != "GOODDEVICE-01" {
		t.Fatalf("expected only the valid profile, got %+v", profiles)
	}
}

func TestParseArchiveZipBombEntry(t *testing.T) {
	zipData := buildZipStored(t, map[string]string{
		"0102030405.json":                  profileJSONAES,
		"0102030405_DeviceDescription.xml": deviceDescriptionShort,
		"0102030405_FeatureMapping.xml":    featureMappingShort,
		"huge.bin":                         strings.Repeat("\x00", maxEntrySize+1),
	})
	_, err := ParseArchiveBytes(zipData, slog.New(slog.DiscardHandler))
	if !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for oversized entry, got %v", err)
	}
	if !strings.Contains(err.Error(), "huge.bin") {
		t.Errorf("error should name the offending entry: %v", err)
	}
}

func TestParseArchiveZipBombTotal(t *testing.T) {
	// Five entries at exactly the per-entry limit pass individually but blow
	// the whole-archive cap (5 * 16 MiB > 64 MiB).
	files := map[string]string{}
	chunk := strings.Repeat("\x00", maxEntrySize)
	for i := range 5 {
		files[fmt.Sprintf("part%d.bin", i)] = chunk
	}
	_, err := ParseArchiveBytes(buildZipStored(t, files), slog.New(slog.DiscardHandler))
	if !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for oversized archive, got %v", err)
	}
	if !strings.Contains(err.Error(), "archive declares") {
		t.Errorf("expected the total-size guard to trip: %v", err)
	}
}

func TestLoadDevices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.yaml")
	content := `devices:
  - name: dishwasher
    host: 192.168.1.50
    connection_type: aes
    psk64: abc
    iv64: def
    description: ./d.json
  - name: hob
    connection_type: TLS
    psk64: xyz
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	devs, err := LoadDevices(path)
	if err != nil {
		t.Fatalf("LoadDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devs))
	}
	if devs[0].ConnectionType != ConnectionAES {
		t.Errorf("connection type not upper-cased: %q", devs[0].ConnectionType)
	}
}

func TestLoadDevicesValidation(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no name":   "devices:\n  - connection_type: AES\n    psk64: a\n    iv64: b\n",
		"bad type":  "devices:\n  - name: x\n    connection_type: FOO\n    psk64: a\n",
		"no psk":    "devices:\n  - name: x\n    connection_type: AES\n",
		"aes no iv": "devices:\n  - name: x\n    connection_type: AES\n    psk64: a\n",
		"duplicate": "devices:\n  - name: x\n    connection_type: TLS\n    psk64: a\n  - name: x\n    connection_type: TLS\n    psk64: b\n",
		"empty":     "devices: []\n",
	}
	for label, content := range cases {
		path := filepath.Join(dir, "d.yaml")
		_ = os.WriteFile(path, []byte(content), 0o600)
		if _, err := LoadDevices(path); err == nil {
			t.Errorf("%s: expected validation error", label)
		}
	}
}

func TestDescriptionJSONRoundTrip(t *testing.T) {
	d := mustParse(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.json")
	if err := SaveDescriptionJSON(path, d); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadDescriptionJSON(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Index must be rebuilt so lookups work after load.
	e, ok := loaded.ByName("Demo.Setting.Power")
	if !ok || e.UID != 0x1005 {
		t.Errorf("round-trip lost entry Demo.Setting.Power")
	}
	if len(loaded.Entries) != len(d.Entries) {
		t.Errorf("entry count changed: %d -> %d", len(d.Entries), len(loaded.Entries))
	}
}

func TestRedact(t *testing.T) {
	if !IsSecretKey("psk") || !IsSecretKey("iv") || IsSecretKey("haId") {
		t.Error("secret key classification wrong")
	}
	if Redact("") != "" {
		t.Error("empty redaction")
	}
	if Redact("ab") != "****" {
		t.Error("short redaction")
	}
	if got := Redact("supersecret"); got == "supersecret" {
		t.Errorf("value not redacted: %q", got)
	}
}
