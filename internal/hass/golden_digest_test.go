// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
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
	// No other key moved in any row, and identity_en.json is UNCHANGED —
	// F1 touches no string Home Assistant keys a registry on.
	"discovery_full_en.json":    "7dc318cdbf5b558800b5d21c56bafc6654712a3b34ae09b0d4f89b98d8e826ad",
	"discovery_full_de.json":    "79e7488393b05e3219ad80b02c69e0480686137e34dc67cd2a3ac768383099cc",
	"discovery_curated_de.json": "42e51f2f26d3eb3f6f583ae57b252864d9baac8130260fa1ddd5578514ea956b",
	"discovery_plain_en.json":   "094c32e44b50f3827216dc068bbf8c4b8fa729bb8e774adcf4ad1375ff9a75bc",
	"identity_en.json":          "16d00294cc7afb1e75f36408ee6cfe409080ab81de5fb0ddac6d0c56ccaad616",
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
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
