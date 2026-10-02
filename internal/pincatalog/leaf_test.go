// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package pincatalog

import "testing"

func TestLeafEdges(t *testing.T) {
	cases := []struct{ in, want string }{
		{"A.B.Eco50", "Eco50"},
		{"Eco50", "Eco50"}, // no separator: the whole name
		{"", ""},
		{"A.", ""},          // empty last segment
		{".Eco50", "Eco50"}, // empty first segment
		{"A..B", "B"},
	}
	for _, c := range cases {
		if got := leaf(c.in); got != c.want {
			t.Errorf("leaf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
