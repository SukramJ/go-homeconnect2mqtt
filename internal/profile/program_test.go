// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import "testing"

func TestResolveProgramNames(t *testing.T) {
	d := newDescription()
	d.add(&Entry{UID: 8195, Name: "Dishcare.Dishwasher.Program.Auto2", Kind: KindProgram})
	d.add(&Entry{UID: 8200, Name: "Dishcare.Dishwasher.Program.Eco50", Kind: KindProgram})
	active := &Entry{UID: 1, Name: "BSH.Common.Root.ActiveProgram", Kind: KindActiveProgram}
	selected := &Entry{UID: 2, Name: "BSH.Common.Root.SelectedProgram", Kind: KindSelectedProgram}
	d.add(active)
	d.add(selected)

	resolveProgramNames(d)

	// The program uid resolves to the short leaf name (i18n localizes downstream).
	if !active.IsEnum() || active.Enumeration[8195] != "Auto2" {
		t.Errorf("active program not resolved: %v", active.Enumeration)
	}
	if selected.Enumeration[8200] != "Eco50" {
		t.Errorf("selected program not resolved: %v", selected.Enumeration)
	}
}

func TestResolveProgramNamesNoPrograms(t *testing.T) {
	d := newDescription()
	active := &Entry{UID: 1, Name: "BSH.Common.Root.ActiveProgram", Kind: KindActiveProgram}
	d.add(active)
	resolveProgramNames(d)
	if active.IsEnum() {
		t.Error("active program should stay non-enum without a program list")
	}
}

func TestResolveProgramNamesKeepsExisting(t *testing.T) {
	d := newDescription()
	d.add(&Entry{UID: 8195, Name: "Dishcare.Dishwasher.Program.Auto2", Kind: KindProgram})
	active := &Entry{
		UID:         1,
		Name:        "BSH.Common.Root.ActiveProgram",
		Kind:        KindActiveProgram,
		Enumeration: map[int]string{42: "Preset"}, // already enumerated -> untouched
	}
	d.add(active)
	resolveProgramNames(d)
	if active.Enumeration[42] != "Preset" || len(active.Enumeration) != 1 {
		t.Errorf("existing enumeration was overwritten: %v", active.Enumeration)
	}
}
