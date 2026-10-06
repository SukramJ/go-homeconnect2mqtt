// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"slices"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// shortSweepWindow shrinks the migration sweep's window for one test. The
// stub broker replays its retained tree inline on subscribe, so the window
// only has to be long enough to be entered.
func shortSweepWindow(t *testing.T) {
	t.Helper()
	prev := legacySweepWindow.Get()
	legacySweepWindow.Set(20 * time.Millisecond)
	t.Cleanup(func() { legacySweepWindow.Set(prev) })
}

// TestTheMigrationSweepClearsExactlyTheOldLayoutItOwns drives the sweep
// against a broker holding this instance's pre-0.15.0 tree AND everything
// a careless rule would have taken with it: a sibling instance under
// another root, a sibling NESTED under ours (the shape ADR 0070's review of
// this bridge measured a prefix rule deleting 510 live components of), an
// appliance this instance is not configured with, a feature path this
// appliance does not have, a retained command somebody left, and this
// instance's own new tree.
//
// Every one of those must survive, and every old topic of ours must go.
func TestTheMigrationSweepClearsExactlyTheOldLayoutItOwns(t *testing.T) {
	shortSweepWindow(t)
	b, dev, _, rec := pinBridge(t)
	const old = pinRoot + "/" + pinDevice
	status := dev.topics.StatusPrefix()
	op := dev.topics.State("BSH.Common.Status.OperationState", 0)

	cleared := []string{
		pinRoot + "/status", // the old daemon status, by exact match
		old + "/availability",
		old + "/connection_state",
		old + "/BSH/Common/Status/OperationState/state",
		old + "/BSH/Common/Setting/PowerState/state",
		status + "Gone/Feature", // §3.2: an item the appliance no longer has
	}
	survives := []string{
		// A sibling instance under another root, same appliance name.
		"homeconnect2/" + pinDevice + "/BSH/Common/Status/OperationState/state",
		"homeconnect2/status",
		// A sibling nested under our root: its device segment is "kitchen".
		pinRoot + "/kitchen/" + pinDevice + "/BSH/Common/Status/OperationState/state",
		pinRoot + "/kitchen/status",
		// An appliance name this instance is not configured with.
		pinRoot + "/Kühlschrank/BSH/Common/Status/OperationState/state",
		pinRoot + "/Kühlschrank/availability",
		// Our appliance, but a path the old layout never published for it.
		old + "/Not/A/Feature/state",
		old + "/BSH/Common/Status/OperationState",
		// A retained command, which the old daemon never published.
		old + "/BSH/Common/Setting/PowerState/set",
		// Our own new tree, and another appliance's.
		op,
		dev.topics.Online(),
		dev.topics.ConnectionState(),
		pinRoot + "/status/OTHER-HAID/BSH/Common/Status/OperationState",
		pinRoot + "/connected",
		pinRoot + "/info",
	}
	tree := map[string]string{}
	for _, topic := range append(slices.Clone(cleared), survives...) {
		tree[topic] = "x"
	}
	seedRetained(rec, tree)

	if n := b.sweepLegacy(t.Context()); n != len(cleared) {
		t.Errorf("the sweep cleared %d topics, want %d", n, len(cleared))
	}
	got := retractedTopics(rec)
	for _, topic := range cleared {
		if !slices.Contains(got, topic) {
			t.Errorf("%s is the old layout's and was not cleared", topic)
		}
	}
	for _, topic := range survives {
		if slices.Contains(got, topic) {
			t.Errorf("%s was cleared — it is not this instance's old layout", topic)
		}
	}
	rec.mu.Lock()
	for _, p := range rec.pubs {
		if p.retraction && (p.qos != 0 || !p.retain) {
			t.Errorf("%s cleared at qos %v retain %v, want a retained QoS 0 empty payload", p.topic, p.qos, p.retain)
		}
	}
	rec.mu.Unlock()

	// Idempotent: once the broker has applied the clears, a second start
	// finds nothing.
	rec.mu.Lock()
	for _, topic := range cleared {
		delete(rec.retained, topic)
	}
	rec.mu.Unlock()
	if n := b.sweepLegacy(t.Context()); n != 0 {
		t.Errorf("a second sweep cleared %d topics, want 0", n)
	}
}

// TestTheMigrationSweepLeavesADeviceNamedLikeAFunction is rule 2: an
// appliance an earlier release published under the name `status` left
// `<root>/status/…`, which shares its second level with the new grammar
// and cannot be told from a new topic by its shape. It is left retained,
// stale but never wrongly deleted — while the bare `<root>/status`, two
// levels exactly, is still cleared.
func TestTheMigrationSweepLeavesADeviceNamedLikeAFunction(t *testing.T) {
	shortSweepWindow(t)
	b, rec := smallBridgeWith(t, nil, "status", "dw")
	seedRetained(rec, map[string]string{
		pinRoot + "/status":                       "offline",
		pinRoot + "/status/availability":          "offline",
		pinRoot + "/status/connection_state":      "offline",
		pinRoot + "/dw/availability":              "offline",
		pinRoot + "/status/BSH/Common/Status/x/y": "x",
	})
	b.sweepLegacy(t.Context())
	got := retractedTopics(rec)
	want := []string{pinRoot + "/dw/availability", pinRoot + "/status"}
	if !slices.Equal(got, want) {
		t.Errorf("cleared %v, want %v", got, want)
	}
}

// TestTheMigrationSweepStaysOutOfTheCommandTree: a broker sends one copy
// per matching subscription, so a sweep window that matched `<name>/set/…`
// would deliver a command arriving inside it to its handler twice. No
// window may match a command or a maintenance topic, whatever the device
// names.
func TestTheMigrationSweepStaysOutOfTheCommandTree(t *testing.T) {
	b, _ := smallBridgeWith(t, nil, "set", "maintenance", "dw", "Wasch / Trockner")
	plan := b.legacySweepPlan()
	if len(plan.filters) == 0 {
		t.Fatal("no windows")
	}
	commands := make([]string, 0, 2+2*len(b.devices))
	commands = append(commands, pinRoot+"/maintenance/set/loglevel", pinRoot+"/maintenance/set/restart")
	for _, d := range b.devices {
		commands = append(commands, d.topics.Command("BSH.Common.Setting.PowerState", 0),
			d.topics.ControlCommand("start_program"))
	}
	for _, f := range plan.filters {
		for _, c := range commands {
			if publisher.MatchFilter(f, c) {
				t.Errorf("the sweep window %s matches the command topic %s", f, c)
			}
		}
	}
}
