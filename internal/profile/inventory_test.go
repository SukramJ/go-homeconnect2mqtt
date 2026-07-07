// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const profileJSONTLS = `{
  "haId": "0606060606",
  "deviceDescriptionFileName": "0606060606_DeviceDescription.xml",
  "featureMappingFileName": "0606060606_FeatureMapping.xml",
  "connectionType": "TLS",
  "key": "dGxzc2VjcmV0",
  "brand": "NEFF",
  "type": "Hob",
  "vib": "T68",
  "model": "DemoHob"
}`

func TestParseArchiveDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("dishwasher.zip", buildZip(t, map[string]string{
		"0102030405.json":                  profileJSONAES,
		"0102030405_DeviceDescription.xml": deviceDescriptionShort,
		"0102030405_FeatureMapping.xml":    featureMappingShort,
	}))
	write("hob.ZIP", buildZip(t, map[string]string{ // mixed-case extension
		"0606060606.json":                  profileJSONTLS,
		"0606060606_DeviceDescription.xml": deviceDescriptionShort,
		"0606060606_FeatureMapping.xml":    featureMappingShort,
	}))
	write("broken.zip", []byte("not a zip"))               // skipped leniently, not fatal
	write("notes.txt", []byte("ignore me"))                // not a zip
	write("bomb.zip", buildZipStored(t, map[string]string{ // oversized entry: skipped, not fatal
		"x.json":   profileJSONTLS,
		"huge.bin": strings.Repeat("\x00", maxEntrySize+1),
	}))

	profs, err := ParseArchiveDir(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("ParseArchiveDir: %v", err)
	}
	if len(profs) != 2 {
		t.Fatalf("expected 2 profiles (broken skipped), got %d", len(profs))
	}
	seen := map[string]ConnectionType{}
	for _, p := range profs {
		seen[p.HaID] = p.ConnectionType
	}
	if seen["0102030405"] != ConnectionAES || seen["0606060606"] != ConnectionTLS {
		t.Errorf("unexpected parse result: %v", seen)
	}
}

func TestParseArchiveDirEmpty(t *testing.T) {
	if _, err := ParseArchiveDir(t.TempDir(), nil); err == nil {
		t.Error("expected an error for a directory with no *.zip")
	}
}

func TestWriteInventory(t *testing.T) {
	profs := []*DeviceProfile{
		{HaID: "0102030405", ConnectionType: ConnectionTLS, PSK64: "secret-psk", Brand: "NEFF", Type: "Dishwasher", Vib: "S15"},
		{HaID: "0606060606", ConnectionType: ConnectionAES, PSK64: "psk2", IV64: "iv2", Brand: "BOSCH", Type: "Washer"},
	}
	path := filepath.Join(t.TempDir(), "inventory.json")
	if err := WriteInventory(path, profs); err != nil {
		t.Fatalf("WriteInventory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not honour Unix permission bits, so only assert on Unix.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("inventory perms = %o, want 600 (holds secrets)", perm)
	}
	var got []InventoryEntry
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].HaID != "0102030405" || got[0].PSK64 != "secret-psk" || got[0].ConnectionType != "TLS" {
		t.Errorf("entry[0] = %+v", got[0])
	}
	if got[0].DefaultHost != "NEFF-Dishwasher-0102030405" {
		t.Errorf("defaultHost = %q", got[0].DefaultHost)
	}
}

func TestWriteInventoryTightensExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not honoured on Windows")
	}
	path := filepath.Join(t.TempDir(), "inventory.json")
	// A pre-existing lax-mode file must not keep its mode once secrets are
	// rewritten into it.
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	profs := []*DeviceProfile{{HaID: "0102030405", ConnectionType: ConnectionTLS, PSK64: "secret-psk"}}
	if err := WriteInventory(path, profs); err != nil {
		t.Fatalf("WriteInventory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("inventory perms = %o, want 600 (holds secrets)", perm)
	}
	var got []InventoryEntry
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("stale content not replaced: %v", err)
	}
	if len(got) != 1 || got[0].PSK64 != "secret-psk" {
		t.Errorf("unexpected content: %+v", got)
	}
}
