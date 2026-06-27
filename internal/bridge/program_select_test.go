// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
)

func TestProgLeaf(t *testing.T) {
	if got := progLeaf("Dishcare.Dishwasher.Program.Eco50"); got != "Eco50" {
		t.Errorf("progLeaf full = %q, want Eco50", got)
	}
	if got := progLeaf("Eco50"); got != "Eco50" {
		t.Errorf("progLeaf bare = %q, want Eco50", got)
	}
}

func TestProgNorm(t *testing.T) {
	cases := map[string]string{"Eco 50 °C": "eco50c", "Eco50": "eco50", "Auto 2": "auto2"}
	for in, want := range cases {
		if got := progNorm(in); got != want {
			t.Errorf("progNorm(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProgramLabelResolves pins the resolveProgramUID invariant: a localized
// select label, de-localized and normalized, equals the normalized program leaf
// — which is how a Home Assistant select option maps back to its program uid.
func TestProgramLabelResolves(t *testing.T) {
	cases := []struct{ label, programFeature string }{
		{"Eco 50 °C", "Dishcare.Dishwasher.Program.Eco50"},
		{"Auto 2", "Dishcare.Dishwasher.Program.Auto2"},
	}
	for _, c := range cases {
		key := progNorm(i18n.EnumValue(c.label, "de"))
		leaf := progNorm(progLeaf(c.programFeature))
		if key != leaf {
			t.Errorf("label %q -> %q, but program %q leaf -> %q", c.label, key, c.programFeature, leaf)
		}
	}
}
