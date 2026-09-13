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
// Moved a third time by ADR 0070 phase 7 step 5, in exactly two rows,
// both hand-edited and neither regenerated:
//
//   - `subscribe_filters` loses "homeassistant/+/+/+/config" and
//     "homeassistant/+/geschirrspuler/+/config", both hard-wired to
//     QoS 0, and gains "homeassistant/#" at MQTT_QOS. That is
//     publisher.Runtime.Sweep's snapshot window replacing the two
//     hand-rolled reconcile subscriptions: the library parses all three
//     Home Assistant discovery topic forms out of ONE window rather than
//     encoding one of them in a filter, so the narrower pair cannot be
//     expressed. Everything the window then does is narrower than
//     before, not wider — see internal/bridge/reconcile.go. This is the
//     same single pinned value go-mtec2mqtt's equivalent step moved, for
//     the same reason.
//   - `publish_qos_retain.bridge_will` said "qos=0 retain=true" and the
//     will has gone out at MQTT_QOS since #41 fixed F1
//     (cmd/homeconnect2mqtt/main.go, asserted by
//     TestWillIsTheAvailabilitySourceEveryEntityReads). The row was
//     prose that nothing measured, so it went stale in the commit that
//     changed the thing it describes. It is corrected here and
//     TestWillRowMatchesTheWillTheRuntimeStates now measures it.
//
// Moved a fourth time by F5 of the tombstone review, in exactly one row and
// in the ADDITIVE direction: `subscribe_filters` gains
// "homeassistant/device/+/config" at MQTT_QOS, the tombstone read-back's
// snapshot window. The subscription itself shipped with the read-back; what
// was missing was its line in this artefact, because the builder drives
// subscribeCommands alone and only the sweep's transient window had ever
// been stated by hand. "No golden moved" was therefore true of a file that
// under-reported the wire. Nothing on the wire changes with this commit;
// the file starts reporting what was already there.
//
// No published topic, no QoS of any PUBLISH, and no retain flag moved.
//
// topicsGoldenDigest is the SHA-256 of internal/bridge/testdata/topics.json,
// held outside the file so a regeneration cannot pass unnoticed. See the
// long note in internal/hass/golden_digest_test.go — same reasoning, same
// standard: updating this literal is a declaration that names a finding.
const topicsGoldenDigest = "f99630e4c65625625aa9753eb89dc49011663ffaaf613177d7fcb778169b72f5"

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
