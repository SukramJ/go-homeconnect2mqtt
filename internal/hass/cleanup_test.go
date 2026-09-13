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
	// The rows are real payload shapes, not minimal ones, and that is the
	// whole point: this rule is the ONLY thing separating two instances of
	// this daemon that share a HASS_BASE_TOPIC and an appliance name, and
	// it used to be asked only about payloads carrying a state_topic — the
	// class it already handled. A button carries none, and 20 of every
	// appliance's 687 configs are buttons.
	const avail = `"availability":[{"topic":"homeconnect/status"},{"topic":"homeconnect/dw/availability"}],`
	const sibAvail = `"availability":[{"topic":"other/status"},{"topic":"other/dw/availability"}],`
	// The nested sibling: MQTT_TOPIC "homeconnect/kitchen", which is a
	// legal value (internal/config/validate.go asks only for non-empty)
	// and a natural way to keep one broker tidy.
	const nestedAvail = `"availability":[{"topic":"homeconnect/kitchen/status"},` +
		`{"topic":"homeconnect/kitchen/dw/availability"}],`
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"ours, a sensor", `{` + avail + `"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state"}`, true},
		{"ours, a button (no state_topic)", `{` + avail + `"unique_id":"homeconnect_dw_btn","command_topic":"homeconnect/dw/X/set"}`, true},
		{"ours, a pre-F1 payload with the flat availability topic", `{"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state","availability_topic":"homeconnect/dw/availability"}`, true},
		{"a SIBLING pre-F1 button, keyed only on the flat availability topic", `{"unique_id":"homeconnect_dw_btn","command_topic":"other/dw/X/set","availability_topic":"other/dw/availability"}`, false},
		{"a SIBLING instance's sensor", `{` + sibAvail + `"unique_id":"homeconnect_dw_op","state_topic":"other/dw/X/state"}`, false},
		{"a SIBLING instance's button — the one this rule used to claim", `{` + sibAvail + `"unique_id":"homeconnect_dw_btn","command_topic":"other/dw/X/set"}`, false},
		{"foreign unique_id", `{"unique_id":"zigbee2mqtt_x","state_topic":"zigbee2mqtt/x"}`, false},
		{"foreign state root", `{"unique_id":"homeconnect_dw_op","state_topic":"other/dw/X/state"}`, false},
		{"ours by namespace but naming no topic at all — unprovable, so not claimed", `{"unique_id":"homeconnect_dw_btn"}`, false},
		// One row per topic key, each carrying that key ALONE. Without
		// them the four keys mask one another: a button is claimed by its
		// availability list whether or not the command topic is read, and
		// a sensor by its state topic whether or not either is, so
		// dropping any single key from the rule changes no verdict and
		// the mutation survives. Each pair below is the same payload under
		// two roots, so the key is shown to decide the answer in both
		// directions.
		//
		// The two ANCHOR keys claim on their own; the two that carry a
		// variable-depth path do not, and that is the nested-sibling fix
		// rather than a regression: `homeconnect/dw/X/state` is a topic
		// the instance rooted at `homeconnect/dw` renders just as
		// readily as the one rooted at `homeconnect`, so it proves
		// nothing on its own. No config this daemon has ever published is
		// in that position — every one carries the availability list
		// (since F1) or the flat availability topic (before it).
		{"state_topic alone, unattributable — it is under our root AND under a nested sibling's", `{"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state"}`, false},
		{"state_topic alone, the sibling's", `{"unique_id":"homeconnect_dw_op","state_topic":"other/dw/X/state"}`, false},
		{"state_topic plus our anchor", `{` + avail + `"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state"}`, true},
		{"command_topic alone, unattributable", `{"unique_id":"homeconnect_dw_btn","command_topic":"homeconnect/dw/X/set"}`, false},
		{"command_topic alone, the sibling's", `{"unique_id":"homeconnect_dw_btn","command_topic":"other/dw/X/set"}`, false},
		{"command_topic plus our anchor", `{` + avail + `"unique_id":"homeconnect_dw_btn","command_topic":"homeconnect/dw/X/set"}`, true},
		{"availability_topic alone, ours", `{"unique_id":"homeconnect_dw_op","availability_topic":"homeconnect/dw/availability"}`, true},
		{"availability_topic alone, the sibling's", `{"unique_id":"homeconnect_dw_op","availability_topic":"other/dw/availability"}`, false},
		{"the availability list alone, ours", `{` + avail + `"unique_id":"homeconnect_dw_op"}`, true},
		{"the availability list alone, the sibling's", `{` + sibAvail + `"unique_id":"homeconnect_dw_op"}`, false},
		// A sibling instance rooted a topic level UNDER ours. Every topic
		// it names begins with `homeconnect/`, so the topic-PREFIX rule
		// claimed all 687 of its components and tombstoned the 510 that
		// were live. The anchors are exact strings: its status topic is
		// `homeconnect/kitchen/status`, and its device availability topic
		// carries one level more than ours can.
		{"a NESTED sibling's sensor", `{` + nestedAvail + `"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/kitchen/dw/X/state"}`, false},
		{"a NESTED sibling's button", `{` + nestedAvail + `"unique_id":"homeconnect_dw_btn","command_topic":"homeconnect/kitchen/dw/X/set"}`, false},
		{"a NESTED sibling's pre-F1 payload, keyed on its flat availability topic", `{"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/kitchen/dw/X/state","availability_topic":"homeconnect/kitchen/dw/availability"}`, false},
		{"a NESTED sibling's status topic is not ours, though it starts with our root", `{"unique_id":"homeconnect_dw_op","availability_topic":"homeconnect/kitchen/status"}`, false},
		// The other direction of the same nesting, asserted separately
		// because the break was ASYMMETRIC: the inner instance always
		// declined the outer one's, and must go on doing so.
		{"our own payload, judged by the nested sibling", `{` + avail + `"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state"}`, true},
		// A payload whose topics do not agree about their root was
		// published by nobody, and "all of them, or not ours" is the safe
		// reading: a rule that took the first match it liked would be a
		// rule an attacker-shaped payload could satisfy.
		//
		// The first row below is the one that keeps the two halves of the
		// rule from masking each other. It carries OUR anchor and a
		// foreign state topic, so the anchor alone would claim it and only
		// the "every topic under our root" half refuses it; the second
		// carries no anchor either, and would be refused by the anchor
		// half whatever the prefix half did. Without the first, deleting
		// the prefix half changes no verdict in this table and the
		// mutation survives — which it did.
		{"our anchor, somebody else's state topic", `{` + avail + `"unique_id":"homeconnect_dw_op","state_topic":"other/dw/X/state"}`, false},
		// Deliberately NOT a row: our anchor plus a topic in a nested
		// sibling's sub-tree. No instance publishes that — a sibling names
		// its own status topic, not ours — and the anchor is the thing
		// that decides, so the rule claims it. Asserting otherwise would
		// be asserting a promise the rule does not make.
		{"topics that disagree about their root", `{"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state","command_topic":"other/dw/X/set"}`, false},
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

// TestOwnsGuardsAreSubsumedByTheNodeScope names two guards in
// [Discovery.OwnsConfigTopic] that cannot change a verdict today, because
// an inert guard is a blind spot a mutation pass reports as a survivor
// and a reader then deletes.
//
// Both are redundant by construction rather than by accident:
//
//   - `t.Bundle` is subsumed by `t.Platform == ""`. A device document's
//     topic carries no platform segment — its components' platforms live
//     inside the payload — so publisher.ParseConfigTopic always reports
//     the two together.
//   - `t.NodeID == ""` and `t.ObjectID == ""` are subsumed by the node
//     scope. The node-id-less forms parse with an empty NodeID, and the
//     scope is a set of slugified device names, which profile's
//     validateDeviceName (since F2) refuses to let be empty — so
//     nodes[""] can never be true.
//
// They are kept because each states an intent the next one does not, and
// because `t.Bundle` stops being redundant the day this daemon publishes
// a device document: step 6 does exactly that, and the line has to be
// revisited deliberately rather than found by a failing test.
func TestOwnsGuardsAreSubsumedByTheNodeScope(t *testing.T) {
	t.Parallel()
	bundle, ok := publisher.ParseConfigTopic("homeassistant", "homeassistant/device/geschirrspuler/config")
	if !ok || !bundle.Bundle {
		t.Fatalf("the device-document topic no longer parses as a bundle: %+v", bundle)
	}
	if bundle.Platform != "" {
		t.Errorf("a device document now carries platform %q — `t.Bundle` has become "+
			"load-bearing and this equivalence no longer holds", bundle.Platform)
	}
	short, ok := publisher.ParseConfigTopic("homeassistant", "homeassistant/sensor/homeconnect_geschirrspuler_x/config")
	if !ok {
		t.Fatal("the four-segment per-entity form no longer parses")
	}
	if short.NodeID != "" {
		t.Errorf("the node-id-less form now parses with node id %q — `t.NodeID == \"\"` has "+
			"become load-bearing", short.NodeID)
	}
	// The node scope can never hold the empty string, which is what makes
	// the two guards above unreachable.
	d := newDisc()
	if d.OwnsConfigTopic("")(publisher.ConfigTopic{Platform: "sensor", ObjectID: "x"}) {
		t.Error("a device name that slugifies to empty entered the node scope; " +
			"profile.validateDeviceName is supposed to make that unreachable (F2)")
	}
}
