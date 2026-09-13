// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/slug"
)

// TestTwoNamesThatFoldToOneNodeIDAreRejected is the guard for a collision
// that used to be confusing and is now destructive.
//
// LoadDevices has always rejected two devices with the same NAME. The
// string that matters downstream is not the name, though: it is
// slug.Slug(name), the Home Assistant node id, and that fold is
// many-to-one over case, spaces and punctuation. Two appliances that fold
// together address one retained device document, one `unique_id`
// namespace and one tombstone memo, so each pass reads the other's
// document as its own previous state and tombstones every component the
// other declares — on every connection, for ever.
//
// Every row below is asserted to actually COLLIDE before it is asserted to
// be rejected. A pair that folds apart would be rejected by nothing and
// would pin nothing.
func TestTwoNamesThatFoldToOneNodeIDAreRejected(t *testing.T) {
	dir := t.TempDir()
	for _, pair := range [][2]string{
		{"My Oven", "my-oven"},
		{"Dishwasher", "dishwasher"},
		{"Geschirrspüler", "geschirrspuler"},
		{"Wash  Machine", "wash_machine"},
	} {
		t.Run(pair[0]+" vs "+pair[1], func(t *testing.T) {
			if a, b := slug.Slug(pair[0]), slug.Slug(pair[1]); a != b {
				t.Fatalf("%q folds to %q and %q to %q — this row collides with nothing "+
					"and would be rejected by nothing", pair[0], a, pair[1], b)
			}
			path := filepath.Join(dir, "d.yaml")
			content := "devices:\n  - name: \"" + pair[0] + "\"\n    connection_type: TLS\n    psk64: a\n" +
				"  - name: \"" + pair[1] + "\"\n    connection_type: TLS\n    psk64: b\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadDevices(path)
			if err == nil {
				t.Fatalf("%q and %q both become node id %q and were accepted: they would "+
					"delete each other's entities on every pass", pair[0], pair[1], slug.Slug(pair[0]))
			}
			// The message has to name BOTH names and the id they share,
			// because the operator's file contains neither the fold nor
			// the id and the collision is invisible in it.
			for _, want := range []string{pair[0], pair[1], slug.Slug(pair[0])} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

// TestNamesThatFoldApartAreAccepted is the over-correction control: the
// guard must reject a collision, not two appliances that merely look
// similar.
func TestNamesThatFoldApartAreAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.yaml")
	content := "devices:\n  - name: \"Oven\"\n    connection_type: TLS\n    psk64: a\n" +
		"  - name: \"Oven 2\"\n    connection_type: TLS\n    psk64: b\n" +
		"  - name: \"Geschirrspüler\"\n    connection_type: TLS\n    psk64: c\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	devs, err := LoadDevices(path)
	if err != nil {
		t.Fatalf("LoadDevices refused three distinct node ids: %v", err)
	}
	ids := map[string]bool{}
	for _, d := range devs {
		ids[slug.Slug(d.Name)] = true
	}
	if len(ids) != 3 {
		t.Fatalf("%d distinct node ids over %d devices: %v", len(ids), len(devs), ids)
	}
}
