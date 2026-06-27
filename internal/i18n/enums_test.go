// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package i18n

import "testing"

func TestEnumLabel(t *testing.T) {
	cases := []struct{ name, lang, want string }{
		{"Run", "de", "Läuft"},
		{"Closed", "de", "Geschlossen"},
		{"Off", "de", "Aus"},
		{"DelayedStart", "de", "Startvorwahl"},       // CamelCase, normalized
		{"Double_Shot", "de", "Double Shot"},         // separators normalized away
		{"NoSuchMemberXyz", "de", "NoSuchMemberXyz"}, // uncatalogued -> unchanged
		{"Run", "en", "Run"},                         // english display -> unchanged
		{"Run", "fr", "Run"},                         // unsupported lang -> unchanged
	}
	for _, c := range cases {
		if got := EnumLabel(c.name, c.lang); got != c.want {
			t.Errorf("EnumLabel(%q,%q) = %q, want %q", c.name, c.lang, got, c.want)
		}
	}
}

func TestEnumValue(t *testing.T) {
	// German label -> English member key (normalized; matched case-insensitively
	// downstream). Uncatalogued / non-de pass through unchanged.
	cases := []struct{ label, lang, want string }{
		{"Läuft", "de", "run"},
		{"Geschlossen", "de", "closed"},
		{"Startvorwahl", "de", "delayedstart"},
		{"NoSuchLabelXyz", "de", "NoSuchLabelXyz"}, // uncatalogued -> unchanged
		{"Läuft", "en", "Läuft"},                   // en mode -> no reverse mapping
	}
	for _, c := range cases {
		if got := EnumValue(c.label, c.lang); got != c.want {
			t.Errorf("EnumValue(%q,%q) = %q, want %q", c.label, c.lang, got, c.want)
		}
	}
}
