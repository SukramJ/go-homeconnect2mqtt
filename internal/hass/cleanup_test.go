// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"
)

func newDisc() *Discovery {
	return New(newStubPub(), "homeassistant", "homeconnect", "en", false, nil)
}

func TestIsOwnConfig(t *testing.T) {
	d := newDisc()
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"ours", `{"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state"}`, true},
		{"ours no state (button)", `{"unique_id":"homeconnect_dw_btn"}`, true},
		{"foreign unique_id", `{"unique_id":"zigbee2mqtt_x","state_topic":"zigbee2mqtt/x"}`, false},
		{"foreign state root", `{"unique_id":"homeconnect_dw_op","state_topic":"other/dw/X/state"}`, false},
		{"not json", `not-json`, false},
	}
	for _, c := range cases {
		if got := d.IsOwnConfig([]byte(c.payload)); got != c.want {
			t.Errorf("%s: IsOwnConfig = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestOwnsConfigTopic is the sweep's topic-namespace half. Each refusal
// below is a class of retained config this daemon does not publish and
// must therefore never judge, and every row is parsed by the library's own
// publisher.ParseConfigTopic rather than hand-built, so a topic form the
// library starts accepting is a row this test starts seeing.
func TestOwnsConfigTopic(t *testing.T) {
	t.Parallel()
	d := newDisc()
	owns := d.OwnsConfigTopic("Geschirrspüler", "Kühlschrank")

	cases := []struct {
		name  string
		topic string
		want  bool
		why   string
	}{
		{"ours", "homeassistant/sensor/geschirrspuler/bsh_common_status_operationstate/config", true, ""},
		{"ours, second device", "homeassistant/button/kuhlschrank/start_program/config", true, ""},
		{"another appliance of ours we were not asked about", "homeassistant/sensor/backofen/x/config", false, "out of scope"},
		{"device document", "homeassistant/device/geschirrspuler/config", false, "this daemon publishes no bundle yet"},
		{"four-segment form", "homeassistant/sensor/homeconnect_geschirrspuler_x/config", false, "no node id: a sibling bridge's or Tasmota's shape"},
		{"platform we never emit", "homeassistant/climate/geschirrspuler/x/config", false, "26 of HA's 32 platforms are not ours"},
		{"foreign node id", "homeassistant/sensor/zigbee2mqtt_bridge/x/config", false, "not a configured device"},
		{"not a config topic", "homeassistant/sensor/geschirrspuler/x/config.other", false, "does not parse"},
		{"the birth topic", "homeassistant/status", false, "Home Assistant's own, and deliberately not a config topic"},
	}
	for _, c := range cases {
		parsed, ok := publisher.ParseConfigTopic("homeassistant", c.topic)
		got := ok && owns(parsed)
		if got != c.want {
			t.Errorf("%s: Owns(%q) = %v, want %v (%s)", c.name, c.topic, got, c.want, c.why)
		}
	}
}

// TestConfigTopicForRebuildsTheTopicItParsed closes the round trip: the
// sweep hands Inspect a PARSED topic and the caller has to name a string
// to retract, so a mismatch here retracts a topic nobody published — or,
// worse, none at all, leaving the orphan standing with a cleared counter.
func TestConfigTopicForRebuildsTheTopicItParsed(t *testing.T) {
	t.Parallel()
	d := newDisc()
	for _, want := range []string{
		"homeassistant/sensor/geschirrspuler/bsh_common_status_operationstate/config",
		"homeassistant/button/geschirrspuler/start_program/config",
		"homeassistant/binary_sensor/geschirrspuler/bsh_common_status_doorstate/config",
	} {
		parsed, ok := publisher.ParseConfigTopic("homeassistant", want)
		if !ok {
			t.Fatalf("%q does not parse", want)
		}
		if got := d.ConfigTopicFor(parsed); got != want {
			t.Errorf("ConfigTopicFor = %q, want %q", got, want)
		}
	}
}

func TestPublishDeviceReturnsTopics(t *testing.T) {
	app, entities := buildEntities(t)
	published := newDisc().PublishDevice(context.Background(), "dw", app.Info(), entities)
	if !published["homeassistant/sensor/dw/bsh_common_status_operationstate/config"] {
		t.Errorf("published set missing OperationState (%d topics)", len(published))
	}
}
