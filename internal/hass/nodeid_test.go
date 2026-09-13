// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/slug"
)

// TestTheNodeIDIsTheFoldProfileGuards closes the gap between the two
// packages the node id lives in.
//
// profile.LoadDevices refuses two appliance names that fold to one node id,
// and it applies slug.Slug to decide that. This package DERIVES the node id
// that ends up on the wire. If the two ever stopped being the same fold,
// the guard would be guarding a string nothing publishes and the collision
// it exists for would be reachable again with the suite green — which is
// exactly the shape the guard had before, when it compared raw names.
func TestTheNodeIDIsTheFoldProfileGuards(t *testing.T) {
	t.Parallel()
	d := newDisc()
	for _, name := range []string{
		"Geschirrspüler", "My Oven", "my-oven", "Dishwasher", "dishwasher",
		"Wäschetrockner", "Waschmaschine / Trockner", "Oven 2", goldenDeviceDE, goldenDeviceEN,
	} {
		if got, want := d.BundleNodeID(name), slug.Slug(name); got != want {
			t.Errorf("BundleNodeID(%q) = %q, slug.Slug = %q — profile.LoadDevices guards the "+
				"second string and the broker sees the first", name, got, want)
		}
	}
}
