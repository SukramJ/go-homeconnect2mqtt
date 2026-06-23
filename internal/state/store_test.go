// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package state

import (
	"testing"
	"time"
)

func fixedClock(start time.Time) (func() time.Time, *time.Time) {
	now := start
	return func() time.Time { return now }, &now
}

func TestUpdateAndSnapshot(t *testing.T) {
	clock, _ := fixedClock(time.Unix(1000, 0))
	s := New(clock)
	s.RegisterDevice("dw", "ha1", "BOSCH", "Dishwasher", "SMV6", nil)
	s.UpdateFeature("dw", Feature{Feature: "BSH.Common.Status.OperationState", UID: 1, Value: "Run"})

	snap := s.Snapshot()
	if len(snap.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(snap.Devices))
	}
	d := snap.Devices[0]
	if d.Name != "dw" || d.Brand != "BOSCH" || d.FeatureCount != 1 {
		t.Errorf("summary wrong: %+v", d)
	}
}

func TestDeviceLookupByNameAndHaID(t *testing.T) {
	s := New(nil)
	s.RegisterDevice("dw", "HA-123", "BOSCH", "Dishwasher", "", nil)
	if _, ok := s.Device("dw"); !ok {
		t.Error("lookup by name failed")
	}
	if _, ok := s.Device("HA-123"); !ok {
		t.Error("lookup by haId failed")
	}
	if _, ok := s.Device("nope"); ok {
		t.Error("unknown device should not be found")
	}
}

func TestAgeAndHealthStale(t *testing.T) {
	clock, now := fixedClock(time.Unix(1000, 0))
	s := New(clock)
	s.RegisterDevice("dw", "", "B", "T", "", nil)
	s.SetConnectionState("dw", "connected", true)
	s.UpdateFeature("dw", Feature{Feature: "x", UID: 1, Value: 1})

	// Advance 5s: fresh.
	*now = time.Unix(1005, 0)
	h := s.Health()
	if h.Status != "ok" || h.Devices[0].Stale {
		t.Errorf("should be fresh: %+v", h)
	}
	if h.Devices[0].AgeSeconds != 5 {
		t.Errorf("age = %d, want 5", h.Devices[0].AgeSeconds)
	}

	// Advance past the stale threshold.
	*now = time.Unix(1000, 0).Add(DefaultStaleThreshold + 5*time.Second)
	h = s.Health()
	if h.Status != "degraded" || !h.Devices[0].Stale {
		t.Errorf("should be stale: %+v", h)
	}
}

func TestHealthOfflineIsStale(t *testing.T) {
	s := New(nil)
	s.RegisterDevice("dw", "", "B", "T", "", nil)
	s.SetConnectionState("dw", "offline", false)
	h := s.Health()
	if h.Status != "degraded" || !h.Devices[0].Stale {
		t.Errorf("offline device should be stale/degraded: %+v", h)
	}
}

func TestSubscribeReceivesEvents(t *testing.T) {
	s := New(nil)
	ch, cancel := s.Subscribe()
	defer cancel()
	s.SetConnectionState("dw", "connected", true)
	select {
	case ev := <-ch:
		if ev.Type != EventConnection {
			t.Errorf("event type = %v, want connection", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
}

func TestSubscribeLatestWins(t *testing.T) {
	s := New(nil)
	ch, cancel := s.Subscribe()
	defer cancel()
	// Publish several without reading; buffer cap 1 keeps the latest.
	for i := 0; i < 5; i++ {
		s.UpdateFeature("dw", Feature{Feature: "x", UID: 1, Value: i})
	}
	ev := <-ch
	m := ev.Data.(map[string]any)
	if m["value"].(int) != 4 {
		t.Errorf("latest-wins failed, got value %v", m["value"])
	}
}

func TestCancelUnsubscribes(t *testing.T) {
	s := New(nil)
	ch, cancel := s.Subscribe()
	cancel()
	if _, open := <-ch; open {
		t.Error("channel should be closed after cancel")
	}
	// Publishing after cancel must not panic.
	s.SetConnectionState("dw", "connected", true)
}

func TestDeviceDetailFeaturesSorted(t *testing.T) {
	s := New(nil)
	s.RegisterDevice("dw", "", "B", "T", "", map[string]any{"brand": "B"})
	s.UpdateFeature("dw", Feature{Feature: "Zeta", UID: 2})
	s.UpdateFeature("dw", Feature{Feature: "Alpha", UID: 1})
	detail, ok := s.Device("dw")
	if !ok {
		t.Fatal("device not found")
	}
	if len(detail.Features) != 2 || detail.Features[0].Feature != "Alpha" {
		t.Errorf("features not sorted: %+v", detail.Features)
	}
}
