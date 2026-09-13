// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// The golden files are produced by the very code they guard, so a
// regeneration is invisible in the test run that produced it: `go test
// -update-discovery-golden` rewrites the file and the next `go test`
// passes against the rewritten file. The only thing that makes a
// regeneration visible is a value held OUTSIDE the file.
//
// That is what this is. Each digest below is the SHA-256 of a golden
// file, held as a Go literal. Regenerating a golden therefore fails this
// test until the literal is updated too — in the same commit, whose
// message must name the finding that moves the bytes.
//
// This is the pin the ADR 0070 phase 7 sequencing asks for at step 1
// ("the goldens must not move") and the standard it holds every later
// step to: steps 4, 5 and 6 must regenerate nothing, so these six
// constants must be untouched by them.
//
// Updating a digest is a declaration, not a chore. If you are here
// because the test failed and you do not know which finding moved the
// bytes, the answer is not to paste the new digest.
var goldenDigests = map[string]string{
	// Moved once since the pins were taken (#40), by F1: in all four
	// payload files the flat "availability_topic" was replaced by the
	// two-source "availability" list plus "availability_mode": "all".
	// No other key moved in any row, and identity_en.json was UNCHANGED by
	// F1 — it touches no string Home Assistant keys a registry on.
	//
	// Moved a second time by F5: in all five files, exactly one row moves
	// from the select platform to the sensor platform
	// (bsh_common_root_selectedprogramnoprograms). Its config topic and
	// default_entity_id change with the platform; its unique_id does not.
	//
	// Moved a third time by F11, in the two GERMAN files only: "options"
	// reorders on 91 rows of the full set and 19 of the curated one. The
	// two English files are byte-identical across that change, and no key
	// other than "options" moves in any row.
	"discovery_full_en.json":    "07ccb6722c98fde08fd0cdec8869957e4587c560b9b614b09a1f375ee460b880",
	"discovery_full_de.json":    "7e2193d4ae54d0841b6c0b57f0c01cb2a43dbceabd81d080859ff066757d8dac",
	"discovery_curated_de.json": "cf6f43d1a291be5948bb5746edb22a5f626dfb8d680a88283164ef31139fcca6",
	"discovery_plain_en.json":   "9120a86322c7b308df91ef82fd4b96ee6e6f99f3d5add77eb431de074f963c78",
	"identity_en.json":          "a725b3b2f3072047d8f2f95bdc7b078ae0f34aadb3b3e74c6cf46d72a9e7eb93",
}

// TestGoldenFilesMatchTheirPinnedDigests fails when a golden file was
// regenerated without the accompanying declaration of intent.
func TestGoldenFilesMatchTheirPinnedDigests(t *testing.T) {
	t.Parallel()
	if *updateDiscoveryGolden {
		t.Skip("regenerating: update the literals in goldenDigests by hand, naming the finding")
	}
	for name, want := range goldenDigests {
		got := fileDigest(t, filepath.Join("testdata", name))
		if got != want {
			t.Errorf("%s: digest %s, pinned %s\n"+
				"The file was regenerated. Update the literal in golden_digest_test.go "+
				"in the same commit, and name in the commit message which finding moves these bytes.",
				name, got, want)
		}
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(normalizeEOL(b))
	return hex.EncodeToString(sum[:])
}

// normalizeEOL strips CR before LF so the digest is a property of the
// fixture's content rather than of the checkout that produced it.
//
// .gitattributes pins these files to LF, which is the real fix; this is
// the belt to its braces, for a clone made before that line existed with
// core.autocrlf on. The goldens are machine-written JSON and contain no
// meaningful lone CR.
func normalizeEOL(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}
