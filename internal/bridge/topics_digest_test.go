// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// Moved twice since #40: by F4, the device sub-tree subscription gained
// "options": ["WithNoLocal"]; by F1, "homeconnect/status" left
// "availability_topics_no_entity_reads", which is now the single
// connection_state topic (F7, deliberately left).
//
// topicsGoldenDigest is the SHA-256 of internal/bridge/testdata/topics.json,
// held outside the file so a regeneration cannot pass unnoticed. See the
// long note in internal/hass/golden_digest_test.go — same reasoning, same
// standard: updating this literal is a declaration that names a finding.
const topicsGoldenDigest = "f9048546ab20f45822329e7a1e2ea78d2c17b7af883d3a146c0ba3830e3db9f7"

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
