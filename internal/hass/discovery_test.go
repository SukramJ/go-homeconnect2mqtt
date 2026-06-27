// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/mqtt"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

type stubPub struct {
	mu   sync.Mutex
	pubs map[string]string
}

func newStubPub() *stubPub { return &stubPub{pubs: map[string]string{}} }

func (s *stubPub) Publish(_ context.Context, topic string, payload []byte, _ mqtt.QoS, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pubs[topic] = string(payload)
	return nil
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
		"BSH.Common.Root.ActiveProgram":    platformSensor,       // active program
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
	d := New(pub, "homeassistant", "homeconnect", mqtt.QoS(1), "en", false, nil)
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
}

type fakeEnricher struct{}

func (fakeEnricher) DeviceClass(feature string) (string, bool) {
	if feature == "BSH.Common.Status.Temp" {
		return "custom_class", true
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
	d := New(pub, "homeassistant", "homeconnect", mqtt.QoS(1), "en", false, nil)
	d.SetEnricher(fakeEnricher{})
	d.PublishDevice(context.Background(), "dw", app.Info(), entities)
	raw := pub.pubs["homeassistant/sensor/dw/bsh_common_status_temp/config"]
	if raw == "" {
		t.Fatal("missing temp sensor config")
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["device_class"] != "custom_class" || p["unit_of_measurement"] != "K" {
		t.Errorf("enrichment override not applied: class=%v unit=%v", p["device_class"], p["unit_of_measurement"])
	}
}

func TestBirthTopic(t *testing.T) {
	d := New(newStubPub(), "homeassistant", "homeconnect", mqtt.QoS(1), "en", false, nil)
	if d.BirthTopic() != "homeassistant/status" {
		t.Errorf("BirthTopic = %q", d.BirthTopic())
	}
}

func TestBinarySensorPayload(t *testing.T) {
	app, entities := buildEntities(t)
	pub := newStubPub()
	d := New(pub, "homeassistant", "homeconnect", mqtt.QoS(1), "en", false, nil)
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
