// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

func TestLeafNameEdges(t *testing.T) {
	cases := []struct{ in, want string }{
		{"A.B.Eco50", "Eco50"},
		{"Eco50", "Eco50"}, // no separator: the whole name
		{"", ""},
		{"A.", ""},          // empty last segment
		{".Eco50", "Eco50"}, // empty first segment
		{"A..B", "B"},
	}
	for _, c := range cases {
		e := &homeconnect.Entity{Desc: &profile.Entry{Name: c.in}}
		if got := leafName(e); got != c.want {
			t.Errorf("leafName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
