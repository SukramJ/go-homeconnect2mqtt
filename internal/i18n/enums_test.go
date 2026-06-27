// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package i18n

import "testing"

func TestEnumLabel(t *testing.T) {
	cases := []struct{ name, lang, want string }{
		{"Run", "de", "Läuft"},
		{"Closed", "de", "Geschlossen"},
		{"Off", "de", "Aus"},
		{"DelayedStart", "de", "Startvorwahl"}, // CamelCase, case-insensitive
		{"R01", "de", "R01"},                   // uncatalogued -> unchanged
		{"Eco50", "de", "Eco50"},               // program name -> unchanged
		{"Run", "en", "Run"},                   // english display -> unchanged
		{"Run", "fr", "Run"},                   // unsupported lang -> unchanged
	}
	for _, c := range cases {
		if got := EnumLabel(c.name, c.lang); got != c.want {
			t.Errorf("EnumLabel(%q,%q) = %q, want %q", c.name, c.lang, got, c.want)
		}
	}
}

func TestEnumValue(t *testing.T) {
	// German label -> English member name (lowercased; matched case-insensitively
	// downstream). Already-English / uncatalogued / non-de pass through unchanged.
	cases := []struct{ label, lang, want string }{
		{"Läuft", "de", "run"},
		{"Aus", "de", "off"},
		{"Geschlossen", "de", "closed"},
		{"Off", "de", "Off"},     // already english -> unchanged
		{"R01", "de", "R01"},     // uncatalogued -> unchanged
		{"Läuft", "en", "Läuft"}, // en mode -> no reverse mapping
	}
	for _, c := range cases {
		if got := EnumValue(c.label, c.lang); got != c.want {
			t.Errorf("EnumValue(%q,%q) = %q, want %q", c.label, c.lang, got, c.want)
		}
	}
}
