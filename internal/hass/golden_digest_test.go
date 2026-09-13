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
	// Unchanged since the pins were taken (#40). No finding in this PR
	// moves the English full set's topics or identity.
	"discovery_full_en.json":    "ea481a97e07dd77d1c4b115af749afcafbcc385e0e4938d2f11159628a5d5901",
	"discovery_full_de.json":    "472708115548a48ddb9d994e53e5f1c66832f7be043cb8e5ddc2c8f0067e6a06",
	"discovery_curated_de.json": "28b4dd381b3d207c627685dc25c73dfb015da441c09f08592d268908ad336e04",
	"discovery_plain_en.json":   "eaa174d99ebdd8a8b69ec7be543e5bfe94e5184f8eb98a10cd515e76e924e268",
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
