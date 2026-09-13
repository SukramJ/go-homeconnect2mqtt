// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// Moved once since #40, by F4: the device sub-tree subscription gained
// "options": ["WithNoLocal"].
//
// topicsGoldenDigest is the SHA-256 of internal/bridge/testdata/topics.json,
// held outside the file so a regeneration cannot pass unnoticed. See the
// long note in internal/hass/golden_digest_test.go — same reasoning, same
// standard: updating this literal is a declaration that names a finding.
const topicsGoldenDigest = "e9052c1d2e7bd734e9d7d04f05ece315d04d8a12f7e805ea17dfe162e8b11173"

// TestTopicsGoldenMatchesItsPinnedDigest fails when topics.json was
// regenerated without the accompanying declaration of intent.
func TestTopicsGoldenMatchesItsPinnedDigest(t *testing.T) {
	t.Parallel()
	if *updateTopicsGolden {
		t.Skip("regenerating: update topicsGoldenDigest by hand, naming the finding")
	}
	b, err := os.ReadFile("testdata/topics.json")
	if err != nil {
		t.Fatalf("read topics.json: %v", err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != topicsGoldenDigest {
		t.Errorf("topics.json: digest %s, pinned %s\n"+
			"The file was regenerated. Update topicsGoldenDigest in the same commit, "+
			"and name in the commit message which finding moves these bytes.",
			got, topicsGoldenDigest)
	}
}
