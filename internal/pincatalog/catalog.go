// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package pincatalog builds the fixed appliance catalogue the discovery and
// topic pins (ADR 0070 phase 7, notes/adr0070-phase7-measurement.md) are
// measured against.
//
// The catalogue is NOT a stub of the publish path: it is an input fixture.
// Every feature NAME is real — read from the shipped mapping.yaml, which is
// generated to mirror the official Home Assistant `home_connect` integration —
// and the entries are fed through the production
// profile.Entry -> homeconnect.Appliance -> hass.Discovery chain unchanged.
//
// What is synthesised is the per-feature wire DESCRIPTOR (kind, access,
// protocol type, content type, enumeration), because an appliance's
// DeviceDescription XML is device-bound and not shipped with this repository.
// The synthesis is deterministic and derived only from the feature name and
// the catalogue's own hints, so the pin is stable across runs and machines:
//
//   - kind comes from the third dotted segment of the feature name
//     (Status/Setting/Option/Event/Command/Appliance/Root), which is how a
//     real DeviceDescription groups the same features into its
//     statusList/settingList/optionList/eventList/commandList elements;
//   - access follows the kind (a setting/option is readWrite, a status is
//     read, a command is writeOnly), matching a real profile's common case;
//   - the protocol/content type is taken from the catalogue's unit and
//     device_class hints where it has them, and otherwise from the leaf
//     name's shape.
//
// Nine explicitly-constructed edge entries are appended to cover the branches
// the catalogue itself cannot reach (unnamed feature, raw program node,
// protection port, read-only selected program, empty enumeration, ...).
package pincatalog

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// Info is the device header the pins publish under.
var Info = profile.DeviceInfo{
	Type:     "Dishwasher",
	Brand:    "BOSCH",
	Model:    "SMV6ZCX49E",
	Version:  2,
	Revision: 1,
}

// eventEnum is the enumeration a real BSH event element carries.
var eventEnum = map[int]string{0: "Off", 1: "Present", 2: "Confirmed"}

// programEnum is the enumeration a real activeProgram/selectedProgram element
// carries once the parser has folded the programGroup into it.
var programEnum = map[int]string{
	0:    "Dishcare.Dishwasher.Program.Auto2",
	8192: "Dishcare.Dishwasher.Program.Eco50",
	8193: "Dishcare.Dishwasher.Program.Quick45",
}

// onOffEnum is the generic two-member enumeration used for the *State/*Mode
// leaves that a real profile models as an enumerationType.
var onOffEnum = map[int]string{0: "Off", 1: "On", 2: "Auto"}

type catalogFile struct {
	Features map[string]struct {
		Unit        string `yaml:"unit"`
		DeviceClass string `yaml:"device_class"`
	} `yaml:"features"`
}

// Build reads the shipped mapping.yaml at mappingPath and returns the pin
// catalogue: one profile entry per catalogued feature, in feature-name order,
// followed by the edge entries. UIDs are assigned densely from 4096 in that
// same order, so they are stable as long as mapping.yaml is.
func Build(mappingPath string) ([]*profile.Entry, error) {
	data, err := os.ReadFile(mappingPath) //nolint:gosec // test fixture path, caller-supplied
	if err != nil {
		return nil, fmt.Errorf("pincatalog: read %s: %w", mappingPath, err)
	}
	var cf catalogFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("pincatalog: parse %s: %w", mappingPath, err)
	}
	names := make([]string, 0, len(cf.Features))
	for name := range cf.Features {
		if strings.Count(name, ".") < 2 {
			continue // not a dotted Home Connect feature key
		}
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]*profile.Entry, 0, len(names)+len(edges))
	uid := 4096
	for _, name := range names {
		h := cf.Features[name]
		e := entryFor(name, h.Unit, h.DeviceClass)
		e.UID = uid
		uid++
		out = append(out, e)
	}
	for _, mk := range edges {
		e := mk()
		e.UID = uid
		uid++
		out = append(out, e)
	}
	return out, nil
}

// segment returns the i-th dotted segment of a feature name, or "".
func segment(name string, i int) string {
	parts := strings.Split(name, ".")
	if i >= len(parts) {
		return ""
	}
	return parts[i]
}

func leaf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// entryFor synthesises one entry from a real feature name plus the
// catalogue's own hints. See the package comment for the rules.
func entryFor(name, unit, deviceClass string) *profile.Entry {
	e := &profile.Entry{Name: name, Available: true, Access: "read"}
	switch segment(name, 2) {
	case "Setting":
		e.Kind, e.Access = profile.KindSetting, "readwrite"
	case "Option":
		e.Kind, e.Access = profile.KindOption, "readwrite"
	case "Command":
		e.Kind, e.Access = profile.KindCommand, "writeonly"
	case "Event", "Appliance":
		e.Kind, e.Enumeration = profile.KindEvent, eventEnum
	case "Root":
		switch leaf(name) {
		case "ActiveProgram":
			e.Kind, e.Access, e.Enumeration = profile.KindActiveProgram, "readwrite", programEnum
		case "SelectedProgram":
			e.Kind, e.Access, e.Enumeration = profile.KindSelectedProgram, "readwrite", programEnum
		default:
			e.Kind = profile.KindStatus
		}
	default:
		e.Kind = profile.KindStatus
	}
	if e.Kind == profile.KindEvent || e.Kind == profile.KindActiveProgram || e.Kind == profile.KindSelectedProgram {
		e.ProtocolType = profile.ProtocolString
		return e
	}
	applyType(e, unit, deviceClass)
	return e
}

// unitContent maps a catalogue unit to the fine content type the real parser
// would have resolved from the element's refDID.
var unitContent = map[string]struct {
	content string
	cid     profile.ProtocolType
}{
	"°C":  {"temperatureCelsius", profile.ProtocolFloat},
	"°F":  {"temperatureFahrenheit", profile.ProtocolFloat},
	"%":   {"percent", profile.ProtocolInteger},
	"s":   {"timeSpan", profile.ProtocolInteger},
	"dBm": {"dbm", profile.ProtocolInteger},
	"rpm": {"rpm", profile.ProtocolInteger},
	"W":   {"power", profile.ProtocolFloat},
	"Wh":  {"energy", profile.ProtocolFloat},
	"g":   {"weight", profile.ProtocolInteger},
}

// classContent maps a catalogue device_class to the same, for the features
// that carry a class but no unit.
var classContent = map[string]struct {
	content string
	cid     profile.ProtocolType
}{
	"temperature":     {"temperatureCelsius", profile.ProtocolFloat},
	"power":           {"power", profile.ProtocolFloat},
	"energy":          {"energy", profile.ProtocolFloat},
	"duration":        {"timeSpan", profile.ProtocolInteger},
	"signal_strength": {"dbm", profile.ProtocolInteger},
	"weight":          {"weight", profile.ProtocolInteger},
}

// enumLeaves are the leaf-name suffixes a real profile models as an
// enumerationType rather than a scalar.
var enumLeaves = []string{"State", "Mode", "Type", "Level", "Status"}

// boolLeaves are the leaf-name suffixes a real profile models as a Boolean.
var boolLeaves = []string{"Active", "Allowed", "Enabled", "Connected", "Locked", "Confirmed", "Lock", "Supported"}

func applyType(e *profile.Entry, unit, deviceClass string) {
	if c, ok := unitContent[unit]; ok {
		e.ContentType, e.ProtocolType = c.content, c.cid
		e.HasMin, e.Min, e.HasMax, e.Max, e.HasStep, e.StepSize = true, 0, true, 100, true, 1
		return
	}
	if c, ok := classContent[deviceClass]; ok {
		e.ContentType, e.ProtocolType = c.content, c.cid
		e.HasMin, e.Min, e.HasMax, e.Max, e.HasStep, e.StepSize = true, 0, true, 100, true, 1
		return
	}
	l := leaf(e.Name)
	for _, s := range enumLeaves {
		if strings.HasSuffix(l, s) {
			e.Enumeration, e.ProtocolType = onOffEnum, profile.ProtocolString
			return
		}
	}
	for _, s := range boolLeaves {
		if strings.HasSuffix(l, s) {
			e.ProtocolType = profile.ProtocolBoolean
			return
		}
	}
	if strings.Contains(l, "Count") {
		e.ProtocolType = profile.ProtocolInteger
		return
	}
	e.ProtocolType = profile.ProtocolString
}

// edges are the branches the catalogue cannot reach on its own.
var edges = []func() *profile.Entry{
	// An unmapped element: no clear name at all (featurePath/_uid fallback).
	func() *profile.Entry {
		return &profile.Entry{Kind: profile.KindStatus, Access: "read", Available: true, ProtocolType: profile.ProtocolInteger}
	},
	// A raw program node and a protection port: both classify as not-exposed.
	func() *profile.Entry {
		return &profile.Entry{Name: "Dishcare.Dishwasher.Program.Eco50", Kind: profile.KindProgram, Access: "readwrite", Available: true, ProtocolType: profile.ProtocolString}
	},
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Root.ProtectionPort", Kind: profile.KindProtectionPort, Access: "read", Available: true, ProtocolType: profile.ProtocolString}
	},
	// A read-only selected program: sensor, not select.
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Root.SelectedProgramReadOnly", Kind: profile.KindSelectedProgram, Access: "read", Available: true, Enumeration: programEnum, ProtocolType: profile.ProtocolString}
	},
	// A writable selected-program element whose programGroup was empty, so
	// the parser had no enumeration to fold in: a select with no options.
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Root.SelectedProgramNoPrograms", Kind: profile.KindSelectedProgram, Access: "readwrite", Available: true, ProtocolType: profile.ProtocolString}
	},
	// A writable boolean and a writable number: switch and number.
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Setting.PinTheSwitch", Kind: profile.KindSetting, Access: "readwrite", Available: true, ProtocolType: profile.ProtocolBoolean}
	},
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Setting.PinTheNumber", Kind: profile.KindSetting, Access: "readwrite", Available: true, ProtocolType: profile.ProtocolInteger, ContentType: "temperatureCelsius", HasMin: true, Min: 30, HasMax: true, Max: 90, HasStep: true, StepSize: 5}
	},
	// A counter: total_increasing state class.
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Status.ProgramCount", Kind: profile.KindStatus, Access: "read", Available: true, ProtocolType: profile.ProtocolInteger}
	},
	// An entity whose element is present but unavailable: not writable.
	func() *profile.Entry {
		return &profile.Entry{Name: "BSH.Common.Setting.Unavailable", Kind: profile.KindSetting, Access: "readwrite", Available: false, ProtocolType: profile.ProtocolBoolean}
	},
}
