// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/mqtt"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"BSH.Common.Status.OperationState": "bsh_common_status_operationstate",
		"Geschirrspüler":                   "geschirrspuler", // umlaut transliterated
		"a..b":                             "a_b",            // runs collapsed
		"_x_":                              "x",              // trimmed
		"Zone 1 / Süß":                     "zone_1_suss",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanize(t *testing.T) {
	app, _ := buildEntities(t)
	cases := map[string]string{
		"BSH.Common.Status.OperationState": "Operation State",
		"BSH.Common.Status.DoorState":      "Door State",
	}
	for name, want := range cases {
		e, ok := app.EntityByName(name)
		if !ok {
			t.Fatalf("entity %q not found", name)
		}
		if got := humanize(e); got != want {
			t.Errorf("humanize(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestCurationHeuristics(t *testing.T) {
	app, _ := buildEntities(t)
	get := func(name string) (cat string, enabled bool) {
		e, ok := app.EntityByName(name)
		if !ok {
			t.Fatalf("entity %q not found", name)
		}
		return entityCategoryFor(e), enabledByDefault(e)
	}

	// Primary read-only status: enabled, uncategorized.
	if cat, en := get("BSH.Common.Status.OperationState"); cat != "" || !en {
		t.Errorf("OperationState cat=%q enabled=%v, want primary+enabled", cat, en)
	}
	// Non-primary writable setting: disabled by default, config section.
	if cat, en := get("BSH.Common.Setting.ChildLock"); cat != categoryConfig || en {
		t.Errorf("ChildLock cat=%q enabled=%v, want config+disabled", cat, en)
	}
	// Writable option is a control too -> config, not diagnostic.
	if cat, _ := get("BSH.Common.Option.Duration"); cat != categoryConfig {
		t.Errorf("Duration (writable option) cat=%q, want config", cat)
	}
	// Non-primary read-only status: diagnostic.
	if cat, en := get("BSH.Common.Status.Temp"); cat != categoryDiagnostic || en {
		t.Errorf("Temp cat=%q enabled=%v, want diagnostic+disabled", cat, en)
	}
	// State class for the numeric read-only sensor.
	tp, _ := app.EntityByName("BSH.Common.Status.Temp")
	if sc := stateClassFor(tp, platformSensor); sc != "measurement" {
		t.Errorf("Temp state_class = %q, want measurement", sc)
	}
}

func TestDefaultEntityIDSlug(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", mqtt.QoS(1), "en", false, nil)
	d.PublishDevice(context.Background(), "Geschirrspüler", app.Info(), entities)

	var payload map[string]any
	for topic, raw := range pub.pubs {
		if strings.Contains(topic, "operationstate") {
			_ = json.Unmarshal([]byte(raw), &payload)
			break
		}
	}
	if payload == nil {
		t.Fatal("no operationstate config published")
	}
	// entity id is English + umlaut-transliterated, language-independent.
	if got := payload["default_entity_id"]; got != "sensor.geschirrspuler_bsh_common_status_operationstate" {
		t.Errorf("default_entity_id = %v", got)
	}
	// the heuristic English name (no enricher).
	if got := payload["name"]; got != "Operation State" {
		t.Errorf("name = %v, want Operation State", got)
	}
}

type langEnricher struct{ fakeEnricher }

func (langEnricher) LocalizedName(feature, lang string) (string, bool) {
	if feature == "BSH.Common.Status.OperationState" {
		if lang == "de" {
			return "Betriebszustand", true
		}
		return "Operation state", true
	}
	return "", false
}

func TestLocalizedName(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{{"de", "Betriebszustand"}, {"en", "Operation state"}} {
		app, entities := buildEntities(t)
		pub := newStubPub()
		d := New(pub, "homeassistant", "homeconnect", mqtt.QoS(1), tc.lang, false, nil)
		d.SetEnricher(langEnricher{})
		d.PublishDevice(context.Background(), "dw", app.Info(), entities)
		raw := pub.pubs["homeassistant/sensor/dw/bsh_common_status_operationstate/config"]
		var p map[string]any
		_ = json.Unmarshal([]byte(raw), &p)
		if p["name"] != tc.want {
			t.Errorf("lang=%s name = %v, want %q", tc.lang, p["name"], tc.want)
		}
		// the id stays English regardless of language.
		if p["default_entity_id"] != "sensor.dw_bsh_common_status_operationstate" {
			t.Errorf("lang=%s default_entity_id = %v (must stay English)", tc.lang, p["default_entity_id"])
		}
	}
}

func TestCuratedModeSkipsDisabled(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", mqtt.QoS(1), "en", true /* curated */, nil)
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)
	// ChildLock is disabled-by-default -> skipped in curated mode.
	if _, ok := pub.pubs["homeassistant/switch/dw/bsh_common_setting_childlock/config"]; ok {
		t.Error("curated mode should not publish the disabled ChildLock entity")
	}
	// OperationState is primary -> still published.
	if _, ok := pub.pubs["homeassistant/sensor/dw/bsh_common_status_operationstate/config"]; !ok {
		t.Error("curated mode should still publish primary OperationState")
	}
}
