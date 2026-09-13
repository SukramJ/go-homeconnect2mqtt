// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// Moved twice since #40: by F4, the device sub-tree subscription gained
// "options": ["WithNoLocal"]; by F1, "homeconnect/status" left
// "availability_topics_no_entity_reads", which is now the single
// connection_state topic (F7, deliberately left); by F5, the programless
// selected-program entity became a read-only sensor, so it no longer
// advertises a command topic.
//
// topicsGoldenDigest is the SHA-256 of internal/bridge/testdata/topics.json,
// held outside the file so a regeneration cannot pass unnoticed. See the
// long note in internal/hass/golden_digest_test.go — same reasoning, same
// standard: updating this literal is a declaration that names a finding.
const topicsGoldenDigest = "a6806d0b94287efc7c1d4e6b60dc1c544b1c11d96a5a6992a23d80d3bb397719"

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
	// Normalised for line endings: .gitattributes pins these fixtures to
	// LF, and this keeps a clone made before that line existed (with
	// core.autocrlf on, i.e. any Windows default) from failing on a
	// checkout artefact rather than on a content change.
	sum := sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
	if got := hex.EncodeToString(sum[:]); got != topicsGoldenDigest {
		t.Errorf("topics.json: digest %s, pinned %s\n"+
			"The file was regenerated. Update topicsGoldenDigest in the same commit, "+
			"and name in the commit message which finding moves these bytes.",
			got, topicsGoldenDigest)
	}
}
