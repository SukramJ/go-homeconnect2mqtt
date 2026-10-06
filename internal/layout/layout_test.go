// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package layout

import "testing"

const testHaID = "SIEMENS-SN658X06TE-68A40E0F1234"

func testDevice(t *testing.T) Device {
	t.Helper()
	inst, err := New("homeconnect")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return inst.Device(testHaID)
}

// TestDeviceLayout pins every string the layout produces, as literals.
// These are the topics an operator's automations reference from 0.15.0 on;
// the whole reason this package exists is that they must not move when the
// composition is consolidated.
func TestDeviceLayout(t *testing.T) {
	t.Parallel()
	d := testDevice(t)
	inst, _ := New("homeconnect")
	const p = "homeconnect/status/" + testHaID
	const s = "homeconnect/set/" + testHaID

	cases := []struct{ name, got, want string }{
		{"online", d.Online(), p + "/online"},
		{"connection_state", d.ConnectionState(), p + "/connection_state"},
		{"state", d.State("BSH.Common.Status.OperationState", 0), p + "/BSH/Common/Status/OperationState"},
		{"command", d.Command("BSH.Common.Setting.PowerState", 0), s + "/BSH/Common/Setting/PowerState"},
		{"state unnamed", d.State("", 4660), p + "/_uid/4660"},
		{"control start", d.ControlCommand(ControlStartProgram), s + "/_control/start_program"},
		{"control stop", d.ControlCommand(ControlStopProgram), s + "/_control/stop_program"},
		{"filter", d.CommandFilter(), s + "/#"},
		{"set filter", inst.SetFilter(), "homeconnect/set/#"},
		{"connected", inst.Connected(), "homeconnect/connected"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestNewRefusesANameTheConventionForbids pins the §3 rule: the instance
// name is one topic level, so a root that worked before 0.15.0 with a `/`
// in it is refused at start rather than published outside the grammar.
func TestNewRefusesANameTheConventionForbids(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "home/connect", "homeconnect/", "a+b", "a#"} {
		if _, err := New(name); err == nil {
			t.Errorf("New(%q) accepted a name mqtt-smarthome 2.0 §3 forbids", name)
		}
	}
}

// TestFeatureSegmentsAreMadeTopicSafe: a feature path stays verbatim, but
// a character MQTT forbids inside a level cannot change the topic's shape.
func TestFeatureSegmentsAreMadeTopicSafe(t *testing.T) {
	t.Parallel()
	d := testDevice(t)
	if got, want := d.State("A b.C+d", 0), "homeconnect/status/"+testHaID+"/A_b/C_d"; got != want {
		t.Errorf("State = %q, want %q", got, want)
	}
	if got, want := FeaturePath("A b.C+d", 0), "A_b/C_d"; got != want {
		t.Errorf("FeaturePath = %q, want %q", got, want)
	}
}

// TestRelativeIsCommandOnly pins that Relative accepts exactly the
// daemon's own command topics of this appliance.
func TestRelativeIsCommandOnly(t *testing.T) {
	t.Parallel()
	d := testDevice(t)
	const s = "homeconnect/set/" + testHaID
	accept := map[string]string{
		s + "/BSH/Common/Setting/PowerState": "BSH/Common/Setting/PowerState",
		s + "/_control/start_program":        "_control/start_program",
		s + "/_uid/4660":                     "_uid/4660",
	}
	for in, want := range accept {
		got, ok := d.Relative(in)
		if !ok || got != want {
			t.Errorf("Relative(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	reject := []string{
		"homeconnect/status/" + testHaID + "/BSH/Common/Status/OperationState",
		"homeconnect/status/" + testHaID + "/online",
		"homeconnect/connected",
		"homeconnect/set/OTHER-HAID/BSH/Common/Setting/PowerState",
		s + "/", // the base itself
		s,
	}
	for _, in := range reject {
		if got, ok := d.Relative(in); ok {
			t.Errorf("Relative(%q) = (%q, true), want ok=false", in, got)
		}
	}
}

// TestFeaturePathRoundTrip pins that FeatureName inverts FeaturePath for
// both the named and the unnamed form. The command handler depends on it:
// a path it cannot invert resolves to no entity and the write is dropped
// with a warning that names a feature nobody is looking for.
func TestFeaturePathRoundTrip(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"BSH.Common.Status.OperationState",
		"Dishcare.Dishwasher.Option.IntensivZone",
		"Single",
	} {
		got, _, byUID := FeatureName(FeaturePath(name, 0))
		if byUID || got != name {
			t.Errorf("round trip %q: got (%q, byUID=%v)", name, got, byUID)
		}
	}
	for _, uid := range []int{0, 1, 4660, 65535} {
		_, got, byUID := FeatureName(FeaturePath("", uid))
		if !byUID || got != uid {
			t.Errorf("round trip uid %d: got (%d, byUID=%v)", uid, got, byUID)
		}
	}
	// A malformed uid path resolves to neither, rather than to the
	// feature literally named "_uid.x".
	if name, _, byUID := FeatureName("_uid/x"); byUID || name != "" {
		t.Errorf(`FeatureName("_uid/x") = (%q, byUID=%v), want ("", false)`, name, byUID)
	}
}

// TestLegacyLayout pins the pre-0.15.0 strings, as literals. They are what
// the migration sweep clears and what attribution still recognises, so they
// must be exactly what the old release published — not what it would
// publish if it were written today.
func TestLegacyLayout(t *testing.T) {
	t.Parallel()
	d := NewLegacyDevice("homeconnect", "Geschirrspüler")

	cases := []struct{ name, got, want string }{
		{"base", d.Base(), "homeconnect/Geschirrspüler"},
		{"availability", d.Availability(), "homeconnect/Geschirrspüler/availability"},
		{"connection_state", d.ConnectionState(), "homeconnect/Geschirrspüler/connection_state"},
		{"state", d.State("BSH.Common.Status.OperationState", 0), "homeconnect/Geschirrspüler/BSH/Common/Status/OperationState/state"},
		{"command", d.Command("BSH.Common.Setting.PowerState", 0), "homeconnect/Geschirrspüler/BSH/Common/Setting/PowerState/set"},
		{"state unnamed", d.State("", 4660), "homeconnect/Geschirrspüler/_uid/4660/state"},
		{"control start", d.ControlCommand(ControlStartProgram), "homeconnect/Geschirrspüler/_control/start_program/set"},
		{"bridge", LegacyBridge("homeconnect"), "homeconnect/status"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestLegacyTrailingSlashIsTrimmed pins that an old root written with a
// trailing slash produced the same tree as one without.
func TestLegacyTrailingSlashIsTrimmed(t *testing.T) {
	t.Parallel()
	if a, b := NewLegacyDevice("homeconnect/", "d").Base(), NewLegacyDevice("homeconnect", "d").Base(); a != b {
		t.Errorf("trailing slash: %q != %q", a, b)
	}
	if a, b := LegacyBridge("homeconnect/"), LegacyBridge("homeconnect"); a != b {
		t.Errorf("trailing slash: %q != %q", a, b)
	}
}
