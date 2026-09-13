// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package slug holds the one fold that turns an operator-chosen appliance
// name into a Home Assistant identifier.
//
// It is its own package for one reason: the fold decides the discovery
// node id — the third segment of the retained device-document topic, the
// prefix of every `unique_id`, and the key the tombstone memo is held
// under — and the only place that can REJECT a name is
// internal/profile, which loads the devices file and which
// internal/hass imports. A copy of the fold in internal/profile would be
// two spellings of one rule, which is the shape this repository's F9
// already paid for once.
package slug

import "strings"

// umlautReplacer transliterates German umlauts to match HA's slugify.
var umlautReplacer = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss")

// Slug lowercases, transliterates umlauts and reduces any run of
// non-alphanumeric characters to a single underscore (HA-compatible).
//
// It is deliberately many-to-one: "My Oven", "my-oven" and "MY  OVEN" are
// one identifier. Two CONFIGURED appliances that fold together share a
// document topic, a `unique_id` namespace and one tombstone memo key, and
// then delete each other's entities on every pass — which is why
// profile.LoadDevices rejects the collision rather than this function
// resolving it.
func Slug(s string) string {
	s = umlautReplacer.Replace(strings.ToLower(s))
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// Fold is Slug's case/umlaut fold on its own, without the identifier
// shaping — the collation key internal/hass orders display labels with.
func Fold(s string) string { return umlautReplacer.Replace(strings.ToLower(s)) }
