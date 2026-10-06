// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// The template contract: every template and availability entry of the
// device documents this daemon publishes, evaluated against the bytes this
// daemon's own publish path writes on the topic it reads, must produce a
// value the entity's platform accepts.
//
// The pins in internal/hass/testdata are the per-entity payloads from
// before 0.15.0, and the convention keys (state topics, templates,
// availability) are compared there with expected STRINGS. Nothing evaluated
// a template against a real payload: a template that renders `True` where
// the platform wants `true`, or a label map keyed on the wrong token, would
// pass every pin and leave the entity without a state. This closes that.
//
// The documents are the production render (hass.Discovery.BundleFor, the
// call PublishDeviceBundle makes) of the pin catalogue, full and curated, in
// English and German. The payloads are what Bridge.safePublish — the device
// drain's publish — and Bridge.onState write through the real go-hamqtt
// state plane, read back off the transport. The acceptance rules are the
// MQTT platforms' state handlers in home-assistant/core 2026.10:
//
//   - binary_sensor: payload_on, payload_off or "None"
//     (components/mqtt/binary_sensor.py:184-210);
//   - switch: payload_on, payload_off or "None" (switch.py:111-124);
//   - select: "none" in any case, or one of `options` (select.py:120-135);
//   - number: payload_reset ("None"), or a number within min..max after the
//     0/100 defaults (number.py:175-198);
//   - sensor: "None"; one of `options` when it has them; a number when a
//     unit, a state class or a numeric device class makes it numeric
//     (sensor.py:317-336, sensor/__init__.py:126-145); a datetime for a
//     date/timestamp class; otherwise a string of at most 255 characters
//     (sensor.py:338-344);
//   - an availability entry: exactly its payload_available or
//     payload_not_available (mqtt/entity.py, MqttAvailabilityMixin).
//
// A select's command_template is evaluated too, for every option, and must
// produce a token the appliance's enumeration names — the value the command
// path writes.

func contractBridge(t *testing.T, lang string, curated bool) (*Bridge, *Device, *subRecorder) {
	t.Helper()
	entries, err := pinEntries()
	if err != nil {
		t.Fatalf("pincatalog: %v", err)
	}
	cat, err := pinCatalog()
	if err != nil {
		t.Fatalf("mapping.Load: %v", err)
	}
	cfg := testCfg()
	cfg.MQTTTopic = pinRoot
	cfg.Language = lang
	cfg.HASSEnable = true
	cfg.HASSBaseTopic = pinPrefix
	rec := &subRecorder{}
	logger := slog.New(slog.DiscardHandler)
	plane := planeFor(t, rec, int(pinQoS))
	disc := hass.New(plane, pinPrefix, pinRoot, lang, curated, logger)
	disc.SetEnricher(cat)
	b, err := New(Deps{
		Config: cfg, MQTT: rec, Plane: plane, Logger: logger, HASS: disc,
		Devices: []DeviceSpec{{
			Config: profile.DeviceConfig{
				Name: pinDevice, HaID: haIDFor(pinDevice), Host: "192.168.1.50",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: &profile.Description{Info: pincatalog.Info, Entries: entries},
		}},
	})
	if err != nil {
		t.Fatalf("bridge.New: %v", err)
	}
	// What is under test is the state plane; a discovery pass on onState
	// would only add a migration to the recorder.
	b.StopDiscovery()
	return b, b.devices[0], rec
}

// contractComponent is the part of a component the contract reads.
type contractComponent struct {
	Platform        string   `json:"platform"`
	StateTopic      string   `json:"state_topic"`
	ValueTemplate   string   `json:"value_template"`
	CommandTemplate string   `json:"command_template"`
	PayloadOn       string   `json:"payload_on"`
	PayloadOff      string   `json:"payload_off"`
	Options         []string `json:"options"`
	DeviceClass     string   `json:"device_class"`
	Unit            string   `json:"unit_of_measurement"`
	StateClass      string   `json:"state_class"`
	Min             *float64 `json:"min"`
	Max             *float64 `json:"max"`
	Availability    []struct {
		Topic         string `json:"topic"`
		ValueTemplate string `json:"value_template"`
		Available     string `json:"payload_available"`
		NotAvailable  string `json:"payload_not_available"`
	} `json:"availability"`
}

func TestEveryTemplateRendersAStateItsPlatformAccepts(t *testing.T) {
	for _, tc := range []struct {
		lang    string
		curated bool
	}{{"en", false}, {"de", false}, {"en", true}, {"de", true}} {
		name := fmt.Sprintf("%s/curated=%v", tc.lang, tc.curated)
		t.Run(name, func(t *testing.T) {
			b, dev, rec := contractBridge(t, tc.lang, tc.curated)
			doc, err := b.hass.BundleFor(dev.name, dev.haID, dev.app.Info(), dev.app.Entities())
			if err != nil {
				t.Fatalf("BundleFor: %v", err)
			}
			var ve *discovery.ValidationError
			if err := discovery.Validate(doc); err != nil && (!errors.As(err, &ve) || ve.Blocking()) {
				t.Fatalf("the document is refused: %v", err)
			}
			byTopic := map[string]*homeconnect.Entity{}
			for _, e := range dev.app.Entities() {
				byTopic[dev.topics.state(e)] = e
			}
			availability := map[string]bool{}
			evaluated := 0
			for _, key := range doc.Keys() {
				raw, err := json.Marshal(doc.Components[key])
				if err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				var c contractComponent
				if err := json.Unmarshal(raw, &c); err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				for _, a := range c.Availability {
					sig := a.Topic + "\x00" + a.ValueTemplate + "\x00" + a.Available + "\x00" + a.NotAvailable
					if !availability[sig] {
						availability[sig] = true
						checkAvailability(t, b, dev, rec, key, a.Topic, a.ValueTemplate, a.Available, a.NotAvailable)
					}
				}
				if c.Platform == "button" {
					if c.StateTopic != "" || c.ValueTemplate != "" {
						t.Errorf("%s: a button with a state", key)
					}
					continue
				}
				if c.StateTopic == "" {
					t.Errorf("%s: a %s without a state topic", key, c.Platform)
					continue
				}
				e, ok := byTopic[c.StateTopic]
				if !ok {
					t.Errorf("%s: state_topic %s is not a topic this daemon publishes", key, c.StateTopic)
					continue
				}
				for _, v := range contractValues(e, c) {
					dev.app.ApplyValues([]map[string]any{{"uid": e.UID(), "value": v}})
					b.safePublish(c.StateTopic, statusValue(e))
					payload, ok := rec.lastPayload(c.StateTopic)
					if !ok {
						t.Errorf("%s: nothing was published for value %v", key, v)
						continue
					}
					state := string(payload)
					if c.ValueTemplate != "" {
						if state, err = renderTemplate(c.ValueTemplate, payload, nil); err != nil {
							t.Errorf("%s: %s on %s: %v", key, c.ValueTemplate, payload, err)
							continue
						}
					}
					if why := refusedState(c, state); why != "" {
						t.Errorf("%s (%s): wire value %v, payload %s, rendered %q: %s", key, c.Platform, v, payload, state, why)
					}
					evaluated++
				}
				if c.Platform == "select" {
					checkSelectCommands(t, key, e, c)
				}
			}
			if evaluated == 0 || len(availability) == 0 {
				t.Fatalf("evaluated %d states and %d availability entries", evaluated, len(availability))
			}
			t.Logf("%d components, %d states and %d availability entries evaluated", len(doc.Components), evaluated, len(availability))
		})
	}
}

// contractValues is the wire values an entity is driven through: every
// member of an enumeration, a raw program uid the profile does not name
// (published as "None"), an event's Off and Present, both booleans, the
// bounds of a number, and a representative of every other type.
func contractValues(e *homeconnect.Entity, c contractComponent) []any {
	keys := make([]int, 0, len(e.Desc.Enumeration))
	for k := range e.Desc.Enumeration {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	switch {
	case e.Desc.Kind == profile.KindEvent:
		var out []any
		for _, k := range keys {
			if n := e.Desc.Enumeration[k]; n == "Off" || n == "Present" {
				out = append(out, k)
			}
		}
		return out
	case e.Desc.Kind == profile.KindActiveProgram || e.Desc.Kind == profile.KindSelectedProgram:
		out := make([]any, 0, len(keys)+1)
		unnamed := 1
		for _, k := range keys {
			out = append(out, k)
			if k >= unnamed {
				unnamed = k + 1
			}
		}
		return append(out, unnamed)
	case e.Desc.IsEnum():
		out := make([]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, k)
		}
		return out
	}
	switch e.Desc.ProtocolType {
	case profile.ProtocolBoolean:
		return []any{true, false}
	case profile.ProtocolInteger, profile.ProtocolFloat:
		if c.Platform == "number" {
			lo, hi := 0.0, 100.0
			if c.Min != nil {
				lo = *c.Min
			}
			if c.Max != nil {
				hi = *c.Max
			}
			return []any{lo, hi}
		}
		if e.Desc.ProtocolType == profile.ProtocolFloat {
			return []any{0, 2.5, 1234.75}
		}
		return []any{0, 42}
	case profile.ProtocolObject:
		return []any{map[string]any{"a": 1, "b": "x"}}
	default:
		return []any{"abc", "Some text with 'quotes'"}
	}
}

// refusedState is "" when the entity's platform accepts state, and the
// reason it does not otherwise.
func refusedState(c contractComponent, state string) string {
	const none = "None"
	switch c.Platform {
	case "binary_sensor", "switch":
		if state == c.PayloadOn || state == c.PayloadOff || state == none {
			return ""
		}
		return fmt.Sprintf("neither payload_on %q nor payload_off %q", c.PayloadOn, c.PayloadOff)
	case "select":
		if strings.EqualFold(state, none) || slices.Contains(c.Options, state) {
			return ""
		}
		return "not one of the options"
	case "number":
		if state == none {
			return ""
		}
		f, err := strconv.ParseFloat(state, 64)
		if err != nil {
			return "not a number"
		}
		lo, hi := 0.0, 100.0
		if c.Min != nil {
			lo = *c.Min
		}
		if c.Max != nil {
			hi = *c.Max
		}
		if f < lo || f > hi {
			return fmt.Sprintf("outside %v..%v", lo, hi)
		}
		return ""
	case "sensor":
		if state == none {
			return ""
		}
		if len(c.Options) > 0 {
			if slices.Contains(c.Options, state) {
				return ""
			}
			return "not one of the options"
		}
		switch c.DeviceClass {
		case "date", "timestamp", "uptime":
			return "a date/timestamp sensor; this contract has no datetime payload to offer it"
		case "", "enum":
			if c.Unit == "" && c.StateClass == "" {
				if state == "" || len(state) > 255 {
					return "empty, or longer than the 255 characters a state may have"
				}
				return ""
			}
		}
		if _, err := strconv.ParseFloat(state, 64); err != nil {
			return "not a number, on a sensor Home Assistant expects to be numeric"
		}
		return ""
	}
	return "a platform this contract has no rule for"
}

// checkAvailability drives one availability source through the states this
// daemon publishes on it and evaluates the entry against each payload.
func checkAvailability(t *testing.T, b *Bridge, dev *Device, rec *subRecorder, key, topic, tmpl, avail, notAvail string) {
	t.Helper()
	type step struct {
		what    string
		drive   func() []byte
		wantsUp bool
	}
	last := func() []byte {
		p, ok := rec.lastPayload(topic)
		if !ok {
			t.Fatalf("%s: nothing was published on availability topic %s", key, topic)
		}
		return p
	}
	var steps []step
	switch topic {
	case b.layout.Connected():
		steps = []step{
			{"the Last Will", func() []byte {
				w, err := b.plane.Will()
				if err != nil {
					t.Fatalf("Will: %v", err)
				}
				return w.Payload
			}, false},
			{"an appliance connected", func() []byte { b.onState(dev, homeconnect.StateConnected); return last() }, true},
			{"no appliance connected", func() []byte { b.onState(dev, homeconnect.StateOffline); return last() }, false},
		}
	case dev.topics.Online():
		steps = []step{
			{"the appliance connected", func() []byte { b.onState(dev, homeconnect.StateConnected); return last() }, true},
			{"the appliance offline", func() []byte { b.onState(dev, homeconnect.StateOffline); return last() }, false},
			{"the appliance reconnecting", func() []byte { b.onState(dev, homeconnect.StateReconnecting); return last() }, false},
		}
	default:
		t.Errorf("%s: availability topic %s is not one this daemon publishes", key, topic)
		return
	}
	for _, s := range steps {
		payload := s.drive()
		state := string(payload)
		if tmpl != "" {
			var err error
			if state, err = renderTemplate(tmpl, payload, nil); err != nil {
				t.Errorf("%s: availability %s, %s: %s on %s: %v", key, topic, s.what, tmpl, payload, err)
				continue
			}
		}
		want := notAvail
		if s.wantsUp {
			want = avail
		}
		if state != want {
			t.Errorf("%s: availability %s, %s: payload %s renders %q, want %q", key, topic, s.what, payload, state, want)
		}
	}
}

// checkSelectCommands evaluates a select's command side: the option Home
// Assistant sends, through command_template when there is one, must reach
// the command topic as a member of the appliance's enumeration.
func checkSelectCommands(t *testing.T, key string, e *homeconnect.Entity, c contractComponent) {
	t.Helper()
	members := map[string]bool{}
	for _, n := range e.Desc.Enumeration {
		members[n] = true
	}
	for _, opt := range c.Options {
		sent := opt
		if c.CommandTemplate != "" {
			var err error
			if sent, err = renderTemplate(c.CommandTemplate, nil, map[string]any{"value": opt}); err != nil {
				t.Errorf("%s: command_template on %q: %v", key, opt, err)
				continue
			}
		}
		if !members[sent] {
			t.Errorf("%s: option %q is sent as %q, which the appliance's enumeration does not name", key, opt, sent)
		}
	}
}
