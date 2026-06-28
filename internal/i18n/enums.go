// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package i18n localizes Home Connect enum member values for Home Assistant
// dropdowns. Because an MQTT-discovery entity cannot use HA's native enum
// translations (those ship with a native integration), the localized label is
// baked into the discovery `options`, the published state, and the reverse
// command mapping — the same approach as the sister projects.
//
// The German catalogue (catalog_gen.go) is generated from the official Home
// Connect integration strings and covers all appliance domains. Lookups are
// normalized (lower-cased, non-alphanumerics stripped) so a member name matches
// regardless of its casing/separators; an uncatalogued value falls back to the
// original (English) name, keeping options and state consistent.
package i18n

import "strings"

// norm reduces an enum member name / label to its lookup key: lower-cased with
// every non-alphanumeric rune removed (so "Double_Shot", "DoubleShot" and
// "double shot" all collapse to "doubleshot").
func norm(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// enumENGen reverses enumDEGen: normalized German label -> normalized English
// member key (which the device's enumeration matches case-insensitively).
var enumENGen = func() map[string]string {
	m := make(map[string]string, len(enumDEGen))
	for en, de := range enumDEGen {
		m[norm(de)] = en
	}
	return m
}()

// isNumeric reports whether s is a bare number ("60", "90", "001"). Such values
// are power levels / indices / durations and must never be "translated": a flat
// catalogue cannot tell a hob power level "60" from a program leaf "60".
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// EnumLabel returns the localized label for an enum member name, or the name
// unchanged when the language is not localized, the value is numeric, or it is
// uncatalogued.
func EnumLabel(name, lang string) string {
	if lang == "de" && !isNumeric(name) {
		if de, ok := enumDEGen[norm(name)]; ok {
			return de
		}
	}
	return name
}

// EnumValue maps a (possibly localized) label back to the English enum member
// name for the write path, or returns the label unchanged when it is already
// English / numeric / uncatalogued. The result is matched case-insensitively
// against the device's enumeration downstream, so the casing is not significant.
func EnumValue(label, lang string) string {
	if lang == "de" && !isNumeric(label) {
		if en, ok := enumENGen[norm(label)]; ok {
			return en
		}
	}
	return label
}
