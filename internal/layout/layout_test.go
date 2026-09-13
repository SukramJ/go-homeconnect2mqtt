// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package layout

import "testing"

// TestDeviceLayout pins every string the layout produces, as literals.
// These are the topics an installed base already subscribes to and an
// operator's automations already reference; the whole reason this package
// exists is that they must not move when the composition is consolidated.
func TestDeviceLayout(t *testing.T) {
	t.Parallel()
	d := NewDevice("homeconnect", "Geschirrspüler")

	cases := []struct{ name, got, want string }{
		{"base", d.Base(), "homeconnect/Geschirrspüler"},
		{"availability", d.Availability(), "homeconnect/Geschirrspüler/availability"},
		{"connection_state", d.ConnectionState(), "homeconnect/Geschirrspüler/connection_state"},
		{"state", d.State("BSH.Common.Status.OperationState", 0), "homeconnect/Geschirrspüler/BSH/Common/Status/OperationState/state"},
		{"command", d.Command("BSH.Common.Setting.PowerState", 0), "homeconnect/Geschirrspüler/BSH/Common/Setting/PowerState/set"},
		{"state unnamed", d.State("", 4660), "homeconnect/Geschirrspüler/_uid/4660/state"},
		{"control start", d.ControlCommand(ControlStartProgram), "homeconnect/Geschirrspüler/_control/start_program/set"},
		{"control stop", d.ControlCommand(ControlStopProgram), "homeconnect/Geschirrspüler/_control/stop_program/set"},
		{"filter", d.CommandFilter(), "homeconnect/Geschirrspüler/#"},
		{"bridge", Bridge("homeconnect"), "homeconnect/status"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestTrailingSlashIsTrimmed pins that a root written with a trailing
// slash produces the same tree as one without, which both former builders
// did independently.
func TestTrailingSlashIsTrimmed(t *testing.T) {
	t.Parallel()
	if a, b := NewDevice("homeconnect/", "d").Base(), NewDevice("homeconnect", "d").Base(); a != b {
		t.Errorf("trailing slash: %q != %q", a, b)
	}
	if a, b := Bridge("homeconnect/"), Bridge("homeconnect"); a != b {
		t.Errorf("trailing slash: %q != %q", a, b)
	}
}

// TestRelativeIsCommandOnly pins that Relative accepts exactly the
// daemon's own command topics. It is what makes the "/set" check happen
// before anything is dispatched: a state or availability topic echoed back
// by the device sub-tree subscription must not be mistaken for a command.
func TestRelativeIsCommandOnly(t *testing.T) {
	t.Parallel()
	d := NewDevice("homeconnect", "dw")
	accept := map[string]string{
		"homeconnect/dw/BSH/Common/Setting/PowerState/set": "BSH/Common/Setting/PowerState",
		"homeconnect/dw/_control/start_program/set":        "_control/start_program",
		"homeconnect/dw/_uid/4660/set":                     "_uid/4660",
	}
	for in, want := range accept {
		got, ok := d.Relative(in)
		if !ok || got != want {
			t.Errorf("Relative(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	reject := []string{
		"homeconnect/dw/BSH/Common/Status/OperationState/state",
		"homeconnect/dw/availability",
		"homeconnect/dw/connection_state",
		"homeconnect/status",
		"homeconnect/other/BSH/Common/Setting/PowerState/set",
		"homeconnect/dw", // the base itself
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
