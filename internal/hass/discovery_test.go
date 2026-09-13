// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

type stubPub struct {
	mu   sync.Mutex
	pubs map[string]string
}

func newStubPub() *stubPub { return &stubPub{pubs: map[string]string{}} }

func (s *stubPub) Publish(_ context.Context, topic string, payload []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pubs[topic] = string(payload)
	return true, nil
}

// buildEntities parses a rich description covering every platform and
// returns the live entities (no connection needed for classification).
func buildEntities(t *testing.T) (*homeconnect.Appliance, []*homeconnect.Entity) {
	t.Helper()
	dd := `<?xml version="1.0"?><device>
      <description><type>Dishwasher</type><brand>BOSCH</brand><model>SMV6</model><version>2</version></description>
      <statusList uid="0001">
        <status access="read" available="true" enumerationType="3000" refCID="03" uid="1002"/>
        <status access="read" available="true" refCID="07" uid="1003"/>
        <status access="read" available="true" refCID="01" uid="1004"/>
      </statusList>
      <settingList uid="0003">
        <setting access="readWrite" available="true" enumerationType="3000" refCID="03" uid="1005"/>
        <setting access="readWrite" available="true" refCID="01" uid="1006"/>
        <setting access="readWrite" available="true" refCID="04" uid="1007" min="0" max="90" stepSize="1"/>
      </settingList>
      <eventList uid="0005"><event enumerationType="3000" refCID="03" uid="1009"/></eventList>
      <commandList uid="0007"><command access="writeOnly" available="true" refCID="01" uid="100D"/></commandList>
      <programGroup uid="000B"><program available="true" uid="1015"/></programGroup>
      <activeProgram access="readWrite" uid="1019"/>
      <enumerationTypeList><enumerationType enid="3000"><enumeration value="0"/><enumeration value="1"/></enumerationType></enumerationTypeList>
    </device>`
	fm := `<featureMappingFile><featureDescription>
        <feature refUID="1002">BSH.Common.Status.OperationState</feature>
        <feature refUID="1003">BSH.Common.Status.Temp</feature>
        <feature refUID="1004">BSH.Common.Status.DoorState</feature>
        <feature refUID="1005">BSH.Common.Setting.Program</feature>
        <feature refUID="1006">BSH.Common.Setting.ChildLock</feature>
        <feature refUID="1007">BSH.Common.Option.Duration</feature>
        <feature refUID="1009">BSH.Common.Event.Problem</feature>
        <feature refUID="100D">BSH.Common.Command.AbortProgram</feature>
        <feature refUID="1015">Dishcare.Dishwasher.Program.Eco50</feature>
        <feature refUID="1019">BSH.Common.Root.ActiveProgram</feature>
      </featureDescription>
      <enumDescriptionList><enumDescription refENID="3000">
        <enumMember refValue="0">Off</enumMember><enumMember refValue="1">On</enumMember>
      </enumDescription></enumDescriptionList></featureMappingFile>`
	d, err := profile.ParseDescription([]byte(dd), []byte(fm), nil)
	if err != nil {
		t.Fatalf("ParseDescription: %v", err)
	}
	sock, _ := homeconnect.NewAESSocket("h", make([]byte, 32), make([]byte, 16))
	sess := homeconnect.NewSession(sock, homeconnect.SessionConfig{})
	app := homeconnect.NewAppliance(sess, d, nil)
	entities := app.Entities()
	return app, entities
}

func classifyByName(t *testing.T, app *homeconnect.Appliance, name string) (string, bool) {
	t.Helper()
	e, ok := app.EntityByName(name)
	if !ok {
		t.Fatalf("entity %q not found", name)
	}
	return classify(e)
}

func TestClassify(t *testing.T) {
	app, _ := buildEntities(t)
	cases := map[string]string{
		"BSH.Common.Status.OperationState": platformSensor,       // read enum
		"BSH.Common.Status.Temp":           platformSensor,       // read float
		"BSH.Common.Status.DoorState":      platformBinarySensor, // read bool
		"BSH.Common.Setting.Program":       platformSelect,       // writable enum
		"BSH.Common.Setting.ChildLock":     platformSwitch,       // writable bool
		"BSH.Common.Option.Duration":       platformNumber,       // writable float
		"BSH.Common.Event.Problem":         platformBinarySensor, // event
		"BSH.Common.Command.AbortProgram":  platformButton,       // command
		"BSH.Common.Root.ActiveProgram":    platformSensor,       // read-only (start via control)
	}
	for name, want := range cases {
		got, ok := classifyByName(t, app, name)
		if !ok || got != want {
			t.Errorf("classify(%s) = %q (%v), want %q", name, got, ok, want)
		}
	}
	// A raw program node is not exposed.
	if _, ok := classifyByName(t, app, "Dishcare.Dishwasher.Program.Eco50"); ok {
		t.Error("program node should not be exposed via discovery")
	}
}

func TestPublishDevice(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", "en", false, nil)
	d.PublishDevice(context.Background(), "dishwasher", app.Info(), entities)

	// Switch config for ChildLock.
	swTopic := "homeassistant/switch/dishwasher/bsh_common_setting_childlock/config"
	raw, ok := pub.pubs[swTopic]
	if !ok {
		t.Fatalf("missing switch config %q; got topics %v", swTopic, keys(pub.pubs))
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p["command_topic"] != "homeconnect/dishwasher/BSH/Common/Setting/ChildLock/set" {
		t.Errorf("switch command_topic = %v", p["command_topic"])
	}
	if p["state_topic"] != "homeconnect/dishwasher/BSH/Common/Setting/ChildLock/state" {
		t.Errorf("switch state_topic = %v", p["state_topic"])
	}
	if _, hasDev := p["device"]; !hasDev {
		t.Error("payload missing device block")
	}

	// Select config has options.
	selTopic := "homeassistant/select/dishwasher/bsh_common_setting_program/config"
	if raw, ok := pub.pubs[selTopic]; ok {
		_ = json.Unmarshal([]byte(raw), &p)
		opts, _ := p["options"].([]any)
		if len(opts) != 2 {
			t.Errorf("select options = %v, want 2", p["options"])
		}
	} else {
		t.Errorf("missing select config %q", selTopic)
	}

	// Temperature sensor has device_class + unit.
	tempTopic := "homeassistant/sensor/dishwasher/bsh_common_status_temp/config"
	if raw, ok := pub.pubs[tempTopic]; ok {
		_ = json.Unmarshal([]byte(raw), &p)
		if p["device_class"] != "temperature" || p["unit_of_measurement"] != "°C" {
			t.Errorf("temp sensor class/unit = %v/%v", p["device_class"], p["unit_of_measurement"])
		}
	} else {
		t.Errorf("missing temp sensor config %q", tempTopic)
	}

	// Number has min/max/step and no payload_on.
	numTopic := "homeassistant/number/dishwasher/bsh_common_option_duration/config"
	if raw, ok := pub.pubs[numTopic]; ok {
		_ = json.Unmarshal([]byte(raw), &p)
		if p["max"].(float64) != 90 || p["command_topic"] == nil {
			t.Errorf("number payload wrong: %v", p)
		}
	} else {
		t.Errorf("missing number config %q", numTopic)
	}

	// Program node must NOT produce a config.
	for topic := range pub.pubs {
		if containsSub(topic, "eco50") {
			t.Errorf("program node should not be published: %q", topic)
		}
	}

	// Program controls (buttons) seed the entity id via default_entity_id only;
	// object_id is dropped by HA's discovery schemas and must not be published.
	btnTopic := "homeassistant/button/dishwasher/start_program/config"
	if raw, ok := pub.pubs[btnTopic]; ok {
		_ = json.Unmarshal([]byte(raw), &p)
		if _, has := p["object_id"]; has {
			t.Errorf("button object_id must not be published, got %v", p["object_id"])
		}
		if p["default_entity_id"] != "button.dishwasher_start_program" {
			t.Errorf("button default_entity_id = %v, want button.dishwasher_start_program", p["default_entity_id"])
		}
	} else {
		t.Errorf("missing start_program button config %q", btnTopic)
	}
}

type fakeEnricher struct{}

// DeviceClass returns a class Home Assistant's sensor platform actually
// declares. It used to return "custom_class", which it does not: sensor was
// treated as an open vocabulary (F13) and any string was published, so the
// entity was dropped by Home Assistant during schema validation, silently.
// The refusal path is covered by TestEnrichmentOverrideIsRefusedWhenThe
// PlatformDoesNotDeclareIt below.
func (fakeEnricher) DeviceClass(feature string) (string, bool) {
	switch feature {
	case "BSH.Common.Status.Temp":
		return "humidity", true
	case "BSH.Common.Status.OperationState":
		return "door", true // a binary_sensor class; the sensor platform refuses it
	}
	return "", false
}

func (fakeEnricher) Unit(feature string) (string, bool) {
	if feature == "BSH.Common.Status.Temp" {
		return "K", true
	}
	return "", false
}

func (fakeEnricher) LocalizedName(_, _ string) (string, bool) { return "", false }
func (fakeEnricher) StateClass(string) (string, bool)         { return "", false }
func (fakeEnricher) EntityCategory(string) (string, bool)     { return "", false }
func (fakeEnricher) EnabledByDefault(string) (val, ok bool)   { return false, false }
func (fakeEnricher) Excluded(string) bool                     { return false }

func TestEnrichmentOverride(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", "en", false, nil)
	d.SetEnricher(fakeEnricher{})
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)
	raw := pub.pubs["homeassistant/sensor/dw/bsh_common_status_temp/config"]
	if raw == "" {
		t.Fatal("missing temp sensor config")
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["device_class"] != "humidity" || p["unit_of_measurement"] != "K" {
		t.Errorf("enrichment override not applied: class=%v unit=%v", p["device_class"], p["unit_of_measurement"])
	}
}

// TestEnrichmentOverrideIsRefusedWhenThePlatformDoesNotDeclareIt is F13 as a
// unit test, independent of the shipped catalogue and of the pin fixture:
// an operator override naming a class the target platform does not declare
// must not reach the payload, because Home Assistant would then drop the
// whole entity without a word.
func TestEnrichmentOverrideIsRefusedWhenThePlatformDoesNotDeclareIt(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", "en", false, nil)
	d.SetEnricher(fakeEnricher{})
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)

	raw := pub.pubs["homeassistant/sensor/dw/bsh_common_status_operationstate/config"]
	if raw == "" {
		t.Fatal("missing operation-state sensor config")
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if got, has := p["device_class"]; has && got == "door" {
		t.Error(`the sensor carries device_class "door", which the sensor platform does not ` +
			`declare; Home Assistant drops such an entity during schema validation`)
	}
	if _, has := p["unique_id"]; !has {
		t.Error("the entity itself was dropped; the refusal must drop the KEY, not the entity")
	}
	// The refusal falls back to the HEURISTIC class rather than clearing the
	// key, which is what keeps an enum sensor's options list (F13's rider).
	if p["device_class"] != deviceClassEnum {
		t.Errorf("device_class = %v, want %q: a refused override leaves the heuristic in place",
			p["device_class"], deviceClassEnum)
	}
	if opts, _ := p["options"].([]any); len(opts) == 0 {
		t.Error("the enum sensor lost its options list along with the refused override")
	}
}

// TestBirthTopicSurvivesATrailingSlashPrefix names the second of the two
// locks on the birth topic, because only the first one is load-bearing
// today and a mutation therefore cannot tell them apart.
//
// [Discovery.BirthTopic] calls publisher.BirthTopic rather than
// concatenating, and New already trims a trailing slash off the prefix —
// so replacing the library call with `d.baseTopic + "/status"` changes no
// byte and fails no test. It is an EQUIVALENT mutation, not an untested
// one, and the defect it guards against is real and was shipped by a
// sibling bridge: `prefix + "/status"` against an operator prefix of
// "homeassistant/" subscribes "homeassistant//status", which is a legal
// and DIFFERENT topic from the one Home Assistant announces on. After
// every Home Assistant restart the entities were gone until the daemon
// restarted, silent in both logs.
//
// This asserts the property through the raw operator value, so removing
// EITHER lock while adding the other keeps it green and removing both
// turns it red.
func TestBirthTopicSurvivesATrailingSlashPrefix(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"homeassistant", "homeassistant/", "homeassistant//"} {
		d := New(newStubPub(), prefix, "homeconnect", "en", false, nil)
		if got := d.BirthTopic(); got != "homeassistant/status" {
			t.Errorf("HASS_BASE_TOPIC %q -> BirthTopic %q, want homeassistant/status — "+
				"an empty MQTT level is legal and is a different topic", prefix, got)
		}
	}
}

func TestBirthTopic(t *testing.T) {
	d := New(newStubPub(), "homeassistant", "homeconnect", "en", false, nil)
	if d.BirthTopic() != "homeassistant/status" {
		t.Errorf("BirthTopic = %q", d.BirthTopic())
	}
}

func TestBinarySensorPayload(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", "en", false, nil)
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)
	raw := pub.pubs["homeassistant/binary_sensor/dw/bsh_common_event_problem/config"]
	if raw == "" {
		t.Fatal("missing event binary_sensor config")
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["payload_on"] != "Present" || p["payload_off"] != "Off" {
		t.Errorf("event payload_on/off = %v/%v", p["payload_on"], p["payload_off"])
	}
	// An event is an enum feature, but `enum` is a sensor-only device class:
	// carrying it would make HA reject the whole binary_sensor config.
	if dc, ok := p["device_class"]; ok {
		t.Errorf("binary_sensor device_class = %v, want none", dc)
	}
}

// TestButtonPayload pins the two things HA requires of a command button: a
// command_topic (it rejects the config without one) and a press payload the
// bridge can write to the boolean command feature.
func TestButtonPayload(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", "en", false, nil)
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)
	raw := pub.pubs["homeassistant/button/dw/bsh_common_command_abortprogram/config"]
	if raw == "" {
		t.Fatalf("missing command button config; got topics %v", keys(pub.pubs))
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["command_topic"] != "homeconnect/dw/BSH/Common/Command/AbortProgram/set" {
		t.Errorf("button command_topic = %v", p["command_topic"])
	}
	if p["payload_press"] != commandPressPayload {
		t.Errorf("button payload_press = %v, want %q", p["payload_press"], commandPressPayload)
	}
	if st, ok := p["state_topic"]; ok {
		t.Errorf("button state_topic = %v, want none (a button has no state)", st)
	}
}

// enumEnricher mimics an operator catalogue that attaches device_class `enum`
// to an event — the shipped mapping.yaml used to, and HA rejects it.
type enumEnricher struct{ fakeEnricher }

func (enumEnricher) DeviceClass(feature string) (string, bool) {
	if feature == "BSH.Common.Event.Problem" {
		return deviceClassEnum, true
	}
	return "", false
}

func TestEnrichmentDeviceClassFilteredPerPlatform(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", "en", false, nil)
	d.SetEnricher(enumEnricher{})
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)
	var p map[string]any
	_ = json.Unmarshal([]byte(pub.pubs["homeassistant/binary_sensor/dw/bsh_common_event_problem/config"]), &p)
	if dc, ok := p["device_class"]; ok {
		t.Errorf("catalogue enum class survived onto a binary_sensor: %v", dc)
	}
}

func TestDeviceClassAllowed(t *testing.T) {
	cases := []struct {
		platform, class string
		want            bool
	}{
		{platformSensor, deviceClassEnum, true},
		{platformSensor, "temperature", true},
		{platformBinarySensor, deviceClassEnum, false},
		{platformBinarySensor, "door", true},
		{platformSwitch, "outlet", true},
		{platformSwitch, "temperature", false},
		{platformButton, "restart", true},
		{platformNumber, "temperature", true},
		{platformNumber, deviceClassEnum, false},
		{platformSelect, deviceClassEnum, false},
	}
	for _, c := range cases {
		if got := deviceClassAllowed(c.platform, c.class); got != c.want {
			t.Errorf("deviceClassAllowed(%s, %s) = %v, want %v", c.platform, c.class, got, c.want)
		}
	}
}

// TestSanitizeSensorEnumPairing pins HA's mutual implication on a sensor:
// device_class `enum` needs options, and options need `enum`.
func TestSanitizeSensorEnumPairing(t *testing.T) {
	p := map[string]any{"device_class": deviceClassEnum}
	sanitizeForPlatform(p, platformSensor)
	if _, ok := p["device_class"]; ok {
		t.Error("enum without options should drop device_class")
	}

	p = map[string]any{"device_class": "temperature", "options": []string{"a"}, "unit_of_measurement": "°C"}
	sanitizeForPlatform(p, platformSensor)
	if _, ok := p["options"]; ok {
		t.Error("options without the enum class should be dropped")
	}
	if p["unit_of_measurement"] != "°C" {
		t.Errorf("unit dropped from a value sensor: %v", p)
	}

	p = map[string]any{"device_class": deviceClassEnum, "options": []string{"a"}, "state_class": "measurement", "unit_of_measurement": "°C"}
	sanitizeForPlatform(p, platformSensor)
	if _, ok := p["state_class"]; ok {
		t.Error("an enum sensor must not carry a state_class")
	}
	if _, ok := p["unit_of_measurement"]; ok {
		t.Error("an enum sensor must not carry a unit")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
