// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/model"

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
// resulting document to pass discovery.Validate AND the rules Home
// Assistant applies that the validator does not (see haRefuses). One bad
// mapping line can no longer cost an entity — or, through this daemon's own
// validation gate, an appliance — without failing here first.
//
// 0.15.2 added the entity_category rule. 187 of the pin's 687 components
// were sensors filed under `config`, which Home Assistant refuses on a
// sensor and a binary_sensor: the entity is never created. The renderer now
// maps such a category to `diagnostic`, so the RENDERED document cannot
// carry it whatever mapping.yaml says — which is why the sweep also fails on
// the refusal itself: a mapping line asking for a category one of the shapes
// cannot carry is a line that does not mean what it says.

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
//   - every feature the catalogue gives a device_class, unit, state_class or
//     entity_category, which is where an override can contradict the
//     heuristic, and is the only way the 0.15.0 regression could arise;
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
		_, ec := cat.EntityCategory(e.Name)
		if e.Name != "" && (dc || unit || sc || ec) {
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
// discovery.Validate (go-hamqtt v0.36.0) does not check. Each costs that ONE
// entity: Home Assistant validates a device document as a whole only for its
// device, origin, availability and each component's platform and unique_id
// (components/mqtt/schemas.py:199-227, discovery.py:304-313); everything
// else is validated per component (mqtt/entity.py:324-347).
//
//   - an entity_category outside {config, diagnostic}
//     (helpers/entity.py:212), or `config` on a sensor or binary_sensor,
//     which both refuse to be added (sensor/__init__.py:309-313,
//     binary_sensor/__init__.py:79-83);
//   - a sensor unit its device class does not declare — the MQTT sensor
//     schema raises "The unit of measurement ... is not valid together with
//     device class ...", which drops the entity;
//   - a unit or a state class on a non-numeric device class (timestamp,
//     date): the entity is created, and raises on its first state.
func haRefuses(t *testing.T, body map[string]any) string {
	t.Helper()
	sensor, err := hacatalog.LoadSensor()
	if err != nil {
		t.Fatalf("LoadSensor: %v", err)
	}
	platform, _ := body["platform"].(string)
	if cat, ok := body["entity_category"].(string); ok {
		switch {
		case cat != categoryConfig && cat != categoryDiagnostic:
			return fmt.Sprintf("entity_category %q is not one of Home Assistant's", cat)
		case cat == categoryConfig && (platform == platformSensor || platform == platformBinarySensor):
			return fmt.Sprintf("entity_category %q on a %s, which Home Assistant refuses to add", cat, platform)
		}
	}
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
			refusals := &refusalLog{}
			d := New(nil, goldenPrefix, goldenRoot, lang, false, slog.New(refusals))
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
			for _, r := range refusals.categories() {
				t.Errorf("%s/%s: mapping.yaml asks for a category this shape's platform refuses: %s", s.name, lang, r)
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

// refusalLog is a slog.Handler that keeps every hass.entity_category_refused
// record, so the sweep can fail on the catalogue line rather than only on
// the rendered document the sanitizer has already repaired.
type refusalLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *refusalLog) Enabled(context.Context, slog.Level) bool { return true }

func (l *refusalLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "hass.entity_category_refused" {
		return nil
	}
	var b strings.Builder
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, "%s=%s ", a.Key, a.Value)
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, strings.TrimSpace(b.String()))
	return nil
}

func (l *refusalLog) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *refusalLog) WithGroup(string) slog.Handler      { return l }

func (l *refusalLog) categories() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// TestTheSweepCatchesAConfigCategoryOnAReadOnlyPlatform proves the sweep's
// category rule can fail: the line 0.15.1 shipped eight times —
// `entity_category: config` on a setting — on a setting the appliance
// describes read-only, which lands on a binary_sensor.
func TestTheSweepCatchesAConfigCategoryOnAReadOnlyPlatform(t *testing.T) {
	entries := []*profile.Entry{{
		UID: 9001, Name: "Dishcare.Dishwasher.Setting.ExtraDry", Kind: profile.KindSetting,
		Access: "read", Available: true, ProtocolType: profile.ProtocolBoolean,
	}}
	app := homeconnect.NewAppliance(nil, &profile.Description{Info: pincatalog.Info, Entries: entries}, nil)
	refusals := &refusalLog{}
	d := New(nil, goldenPrefix, goldenRoot, "en", false, slog.New(refusals))
	d.SetEnricher(categoryEnricher{categoryConfig})
	b, err := d.BundleFor(goldenDeviceDE, goldenHaID, pincatalog.Info, app.Entities())
	if err != nil {
		t.Fatalf("BundleFor: %v", err)
	}
	if got := refusals.categories(); len(got) != 1 {
		t.Fatalf("refusals = %v, want exactly one", got)
	}
	body := decodeComponent(t, b.Components["dishcare_dishwasher_setting_extradry"])
	if body["platform"] != platformBinarySensor || body["entity_category"] != categoryDiagnostic {
		t.Errorf("rendered %v, want a diagnostic binary_sensor", body)
	}
	if why := haRefuses(t, map[string]any{"platform": platformBinarySensor, "entity_category": categoryConfig}); why == "" {
		t.Error("haRefuses accepts config on a binary_sensor")
	}
	if why := haRefuses(t, map[string]any{"platform": platformSwitch, "entity_category": "configuration"}); why == "" {
		t.Error("haRefuses accepts a category Home Assistant does not define")
	}
}

// categoryEnricher configures one entity_category for every feature, and
// nothing else.
type categoryEnricher struct{ cat string }

func (categoryEnricher) LocalizedName(string, string) (string, bool) { return "", false }
func (categoryEnricher) DeviceClass(string) (string, bool)           { return "", false }
func (categoryEnricher) Unit(string) (string, bool)                  { return "", false }
func (categoryEnricher) StateClass(string) (string, bool)            { return "", false }
func (c categoryEnricher) EntityCategory(string) (string, bool)      { return c.cat, true }
func (categoryEnricher) EnabledByDefault(string) (val, ok bool)      { return false, false }
func (categoryEnricher) Excluded(string) bool                        { return false }

// TestEntityCategoryIsRenderedOnlyWhereThePlatformAcceptsIt pins the rule
// for every platform this daemon emits, in both render paths.
func TestEntityCategoryIsRenderedOnlyWhereThePlatformAcceptsIt(t *testing.T) {
	platforms := []string{platformSensor, platformBinarySensor, platformSwitch, platformSelect, platformNumber, platformButton}
	for _, platform := range platforms {
		configBecomes := categoryConfig
		if platform == platformSensor || platform == platformBinarySensor {
			configBecomes = categoryDiagnostic
		}
		for _, tc := range []struct{ in, want string }{
			{"", ""},
			{categoryDiagnostic, categoryDiagnostic},
			{categoryConfig, configBecomes},
			{"Config", ""},
			{"configuration", ""},
		} {
			if got := renderableCategory(platform, tc.in); got != tc.want {
				t.Errorf("%s: renderableCategory(%q) = %q, want %q", platform, tc.in, got, tc.want)
			}
			p := map[string]any{"entity_category": tc.in}
			sanitizeForPlatform(p, platform)
			if got, _ := p["entity_category"].(string); got != tc.want {
				t.Errorf("%s: sanitizeForPlatform(%q) = %q, want %q", platform, tc.in, got, tc.want)
			}
			desc := &model.Description{Category: hacatalog.EntityCategory(tc.in)}
			sanitizeDescriptionForPlatform(desc, platform)
			if got := string(desc.Category); got != tc.want {
				t.Errorf("%s: sanitizeDescriptionForPlatform(%q) = %q, want %q", platform, tc.in, got, tc.want)
			}
		}
	}
}

// TestNumberBoundsAreOnlyThoseHomeAssistantAccepts pins numberBounds against
// the MQTT number schema: step >= 1e-3 (mqtt/number.py:98-100), min <= max
// after the 0/100 defaults (mqtt/number.py:79-80, number/const.py:93-94),
// and nothing JSON cannot carry.
func TestNumberBoundsAreOnlyThoseHomeAssistantAccepts(t *testing.T) {
	type want struct {
		minV, maxV, step string // "-" = absent
	}
	show := func(p *float64) string {
		if p == nil {
			return "-"
		}
		return fmt.Sprint(*p)
	}
	inf, nan := math.Inf(1), math.NaN()
	cases := []struct {
		name string
		b    homeconnect.Bounds
		want want
	}{
		{"ordinary", homeconnect.Bounds{Min: 0, HasMin: true, Max: 10, HasMax: true, Step: 0.5, HasStep: true}, want{"0", "10", "0.5"}},
		{"zero step", homeconnect.Bounds{Min: 0, HasMin: true, Max: 10, HasMax: true, Step: 0, HasStep: true}, want{"0", "10", "-"}},
		{"negative step", homeconnect.Bounds{Step: -1, HasStep: true}, want{"-", "-", "-"}},
		{"step below the floor", homeconnect.Bounds{Step: 0.0005, HasStep: true}, want{"-", "-", "-"}},
		{"step at the floor", homeconnect.Bounds{Step: 0.001, HasStep: true}, want{"-", "-", "0.001"}},
		{"min above max", homeconnect.Bounds{Min: 10, HasMin: true, Max: 1, HasMax: true}, want{"-", "-", "-"}},
		{"min equals max", homeconnect.Bounds{Min: 5, HasMin: true, Max: 5, HasMax: true}, want{"5", "5", "-"}},
		{"lone min above the default max", homeconnect.Bounds{Min: 200, HasMin: true}, want{"-", "-", "-"}},
		{"lone max below the default min", homeconnect.Bounds{Max: -5, HasMax: true}, want{"-", "-", "-"}},
		{"lone min inside the default range", homeconnect.Bounds{Min: 30, HasMin: true}, want{"30", "-", "-"}},
		{"infinite max", homeconnect.Bounds{Min: 0, HasMin: true, Max: inf, HasMax: true}, want{"0", "-", "-"}},
		{"NaN step", homeconnect.Bounds{Step: nan, HasStep: true}, want{"-", "-", "-"}},
	}
	for _, c := range cases {
		minV, maxV, step := numberBounds(c.b)
		got := want{show(minV), show(maxV), show(step)}
		if got != c.want {
			t.Errorf("%s: numberBounds = %+v, want %+v", c.name, got, c.want)
		}
	}
}
