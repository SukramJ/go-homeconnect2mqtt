// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// The 0.15.1 regression net: every feature mapping.yaml names, in every wire
// shape a real appliance can give it, rendered into a device document and
// validated.
//
// 0.13.0 to 0.15.0 published NO device document for any appliance exposing
// BSH.Common.Option.RemainingProgramTime read-only, because its catalogue
// class (`timestamp`) met the heuristic's unit and state class and Home
// Assistant admits neither on a timestamp. Nothing caught it: the pin
// fixture synthesises one shape per feature from the catalogue's own hints,
// and for this feature that shape was a STRING sensor, which carries no
// state class. The real appliance sends an Integer with content type
// timeSpan.
//
// So this does not trust a synthesised shape. For the catalogue's features
// (see sweepFeatures for which) it tries every shape (read-only and writable, x boolean, string,
// enumeration, and a number under every content type the classifier derives
// a class or a unit from, plus none), and requires every
// resulting document to pass discovery.Validate AND the two rules Home
// Assistant applies that the validator does not (see haRefuses). One bad
// mapping line can no longer silence a whole appliance without failing
// here first.

// sweepContentTypes is every content type deviceClassAndUnit maps, plus
// none, which stands for every content type it does not map.
//
// Integer and Float are not both swept: classify, stateClassFor and
// deviceClassAndUnit treat them identically, so the second would only
// double the run time. Neither is the language: it changes names and enum
// labels, which no validity rule reads, and the shapes below cover both
// enum platforms.
var sweepContentTypes = []string{
	"", "temperatureCelsius", "temperatureFahrenheit", "percent", "timeSpan",
	"dbm", "rpm", "power", "energy", "weight",
}

type sweepShape struct {
	name     string
	access   string
	protocol profile.ProtocolType
	content  string
	enum     bool
}

func sweepShapes() []sweepShape {
	out := make([]sweepShape, 0, 2*(3+len(sweepContentTypes)))
	for _, access := range []string{"read", "readwrite"} {
		out = append(out,
			sweepShape{name: access + "/boolean", access: access, protocol: profile.ProtocolBoolean},
			sweepShape{name: access + "/string", access: access, protocol: profile.ProtocolString},
			sweepShape{name: access + "/enum", access: access, protocol: profile.ProtocolString, enum: true},
		)
		for _, ct := range sweepContentTypes {
			out = append(out, sweepShape{
				name:   fmt.Sprintf("%s/integer/%s", access, ct),
				access: access, protocol: profile.ProtocolInteger, content: ct,
			})
		}
	}
	return out
}

// sweepFeatures is the part of the pin catalogue's feature set — every
// dotted feature mapping.yaml names, with the kind its name implies — whose
// rendering can differ from feature to feature within one shape:
//
//   - every feature the catalogue gives a device_class, unit or state_class,
//     which is where an override can contradict the heuristic, and is the
//     only way the 0.15.0 regression could arise;
//   - for every other feature, one representative per (kind, "Count" in the
//     name, primary leaf) combination, because the heuristic reads the name
//     through those two tests only (stateClassFor, isPrimary). The rest of
//     them render identically to their representative in every shape, and
//     sweeping all ~690 features in all 26 shapes cost 90 s under -race,
//     which the 120 s package budget of `make test` cannot carry.
func sweepFeatures(t *testing.T) []*profile.Entry {
	t.Helper()
	base, err := pinEntries()
	if err != nil {
		t.Fatalf("pincatalog: %v", err)
	}
	cat := pinEnricher(t)
	type class struct {
		kind           profile.EntryKind
		count, primary bool
	}
	seen := map[class]bool{}
	var out []*profile.Entry
	for _, e := range base {
		_, dc := cat.DeviceClass(e.Name)
		_, unit := cat.Unit(e.Name)
		_, sc := cat.StateClass(e.Name)
		if e.Name != "" && (dc || unit || sc) {
			out = append(out, e)
			continue
		}
		leaf := e.Name
		if _, after, ok := strings.CutLast(e.Name, "."); ok {
			leaf = after
		}
		c := class{e.Kind, strings.Contains(e.Name, "Count"), primarySuffixes[leaf]}
		if !seen[c] {
			seen[c] = true
			out = append(out, e)
		}
	}
	return out
}

// sweepEntities is sweepFeatures recast into one shape. Kinds whose platform
// does not depend on the wire type (commands, events, programs) are kept as
// the pin builds them.
func sweepEntities(features []*profile.Entry, s sweepShape) []*homeconnect.Entity {
	entries := make([]*profile.Entry, 0, len(features))
	for _, b := range features {
		e := *b
		switch e.Kind {
		case profile.KindStatus, profile.KindSetting, profile.KindOption:
			e.Access = s.access
			e.ProtocolType = s.protocol
			e.ContentType = s.content
			e.Enumeration = nil
			if s.enum {
				e.Enumeration = map[int]string{0: "Off", 1: "On", 2: "Auto"}
			}
			e.HasMin, e.Min, e.HasMax, e.Max, e.HasStep, e.StepSize = true, 0, true, 100, true, 1
		default:
		}
		entries = append(entries, &e)
	}
	desc := &profile.Description{Info: pincatalog.Info, Entries: entries}
	return homeconnect.NewAppliance(nil, desc, nil).Entities()
}

// haRefuses is what Home Assistant refuses about a component that
// discovery.Validate (go-hamqtt v0.36.0) does not check:
//
//   - a sensor unit its device class does not declare — the MQTT sensor
//     schema raises "The unit of measurement ... is not valid together with
//     device class ...", which drops the entity and, in a document, the
//     document;
//   - a unit or a state class on a non-numeric device class (timestamp,
//     date): the entity is created, and raises on its first state.
func haRefuses(t *testing.T, body map[string]any) string {
	t.Helper()
	sensor, err := hacatalog.LoadSensor()
	if err != nil {
		t.Fatalf("LoadSensor: %v", err)
	}
	platform, _ := body["platform"].(string)
	dc, _ := body["device_class"].(string)
	unit, _ := body["unit_of_measurement"].(string)
	sc, _ := body["state_class"].(string)
	if platform != platformSensor || dc == "" || dc == deviceClassEnum {
		return ""
	}
	if !slices.Contains(sensor.NumericDeviceClasses, dc) && (unit != "" || sc != "") {
		return fmt.Sprintf("non-numeric device_class %q with unit %q / state_class %q", dc, unit, sc)
	}
	if units, known := sensor.DeviceClassUnits[dc]; known && unit != "" && !slices.Contains(units, unit) {
		return fmt.Sprintf("unit %q is not valid for device_class %q", unit, dc)
	}
	return ""
}

func TestEveryMappedFeatureInEveryShapeRendersAValidDocument(t *testing.T) {
	shapes := sweepShapes()
	features := sweepFeatures(t)
	components := 0
	for _, s := range shapes {
		for _, lang := range []string{"de"} {
			d := New(nil, goldenPrefix, goldenRoot, lang, false, slog.New(slog.DiscardHandler))
			d.SetEnricher(pinEnricher(t))
			b, err := d.BundleFor(goldenDeviceDE, goldenHaID, pincatalog.Info, sweepEntities(features, s))
			if err != nil {
				t.Fatalf("%s/%s: BundleFor: %v", s.name, lang, err)
			}
			if err := discovery.Validate(b); err != nil {
				var ve *discovery.ValidationError
				if !errors.As(err, &ve) || ve.Blocking() {
					t.Errorf("%s/%s: the document is refused: %v", s.name, lang, err)
				}
			}
			for _, key := range b.Keys() {
				raw, err := json.Marshal(b.Components[key])
				if err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				if why := haRefuses(t, body); why != "" {
					t.Errorf("%s/%s: %s: %s", s.name, lang, key, why)
				}
				components++
			}
		}
	}
	t.Logf("%d features x %d shapes, %d components validated", len(features), len(shapes), components)
}

// TestRemainingProgramTimeAsTheAppliancesExposeIt is the 0.15.0 failure as
// the three live appliances reported it, rendered as they expose it: a
// read-only Integer option of content type timeSpan. It was withheld with
// `state_class "measurement" is not allowed for device_class "timestamp"`.
// It is now a duration in seconds, which is what the appliance sends.
func TestRemainingProgramTimeAsTheAppliancesExposeIt(t *testing.T) {
	for _, access := range []string{"read", "readwrite"} {
		for _, tc := range []struct{ lang, name string }{{"en", "Remaining program time"}, {"de", "Restprogrammzeit"}} {
			entries := []*profile.Entry{{
				UID: 544, Name: "BSH.Common.Option.RemainingProgramTime", Kind: profile.KindOption,
				Access: access, Available: true, ProtocolType: profile.ProtocolInteger, ContentType: "timeSpan",
			}}
			app := homeconnect.NewAppliance(nil, &profile.Description{Info: pincatalog.Info, Entries: entries}, nil)
			d := New(nil, goldenPrefix, goldenRoot, tc.lang, true, slog.New(slog.DiscardHandler))
			d.SetEnricher(pinEnricher(t))
			b, err := d.BundleFor("Geschirrspueler", goldenHaID, pincatalog.Info, app.Entities())
			if err != nil {
				t.Fatalf("BundleFor: %v", err)
			}
			if err := discovery.Validate(b); err != nil {
				t.Fatalf("%s/%s: %v", access, tc.lang, err)
			}
			body := decodeComponent(t, b.Components["bsh_common_option_remainingprogramtime"])
			wantPlatform, wantSC := platformSensor, "measurement"
			if access == "readwrite" {
				wantPlatform, wantSC = platformNumber, ""
			}
			sc, _ := body["state_class"].(string)
			if body["platform"] != wantPlatform || body["device_class"] != "duration" ||
				body["unit_of_measurement"] != "s" || sc != wantSC || body["name"] != tc.name {
				t.Errorf("%s/%s: rendered %v", access, tc.lang, body)
			}
		}
	}
}

// TestSensorClassesAreReconciled pins each rule of reconcileSensorClasses
// against Home Assistant's tables, including the fail-closed branch.
func TestSensorClassesAreReconciled(t *testing.T) {
	cases := []struct {
		dc, unit, sc       string
		wantDC, wantU, wSC string
	}{
		{"timestamp", "s", "measurement", "timestamp", "", ""},
		{"date", "", "measurement", "date", "", ""},
		{"duration", "s", "measurement", "duration", "s", "measurement"},
		{"energy", "Wh", "measurement", "energy", "Wh", ""},
		{"energy", "Wh", "total_increasing", "energy", "Wh", "total_increasing"},
		{"volume", "", "measurement", "volume", "", ""},
		{"humidity", "K", "measurement", "", "K", "measurement"},
		{"", "rpm", "measurement", "", "rpm", "measurement"},
	}
	for _, c := range cases {
		dc, u, sc := reconcileSensorClasses(c.dc, c.unit, c.sc)
		if dc != c.wantDC || u != c.wantU || sc != c.wSC {
			t.Errorf("reconcile(%q,%q,%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.dc, c.unit, c.sc, dc, u, sc, c.wantDC, c.wantU, c.wSC)
		}
	}
	if sensorRelations() == nil {
		t.Fatal("go-ha-catalog's sensor relation tables did not load")
	}
	if dc, u, sc := reconcileSensorClassesIn(nil, "duration", "s", "measurement"); dc != "" || u != "s" || sc != "measurement" {
		t.Errorf("without the tables: (%q,%q,%q), want the device class dropped and nothing else", dc, u, sc)
	}
}
