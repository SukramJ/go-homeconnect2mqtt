// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/mapping"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/pincatalog"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// This file pins the other half of the wire: the state, command and
// availability tree this daemon writes to and subscribes to, and — the
// point of the exercise — that the TWO INDEPENDENT state-topic builders
// agree.
//
// This bridge composes an entity's state topic twice, in two packages,
// from two separate featurePath implementations:
//
//	internal/hass/discovery.go:87   featurePath(e *homeconnect.Entity)
//	internal/bridge/publish.go:48   featurePath(name string, uid int)
//
// The first goes into the retained discovery config as `state_topic`; the
// second is where the value is actually published. Nothing compared them
// until this file. A divergence leaves every entity pointing at a topic
// nobody writes — permanently `unknown`, with nothing in the log and
// nothing in Home Assistant's registry to notice it. See
// notes/adr0070-phase7-measurement.md, finding F3.
//
// Regenerate with:
//
//	go test ./internal/bridge -run TestTopicGolden -update-topics-golden
//
// # Pinned defects
//
//   - F3 — two state-topic builders that can drift. Pinned by
//     TestStateTopicBuildersAgree, which compares BUILDER against
//     BUILDER and never consults the golden file, so it fails even
//     immediately after a regeneration.
//   - F4 — the command subscription is still the whole device sub-tree,
//     <root>/<device>/#, because the feature path is variable-depth and no
//     fixed-arity filter covers it. It is no longer unguarded: MQTT 5.0
//     No Local plus a Device.Relative check before dispatch. Asserted by
//     TestCommandFilterIsGuardedAgainstTheDaemonsOwnTree.
//   - F1 — FIXED. <root>/status carries the Last Will and is now declared
//     by every payload, alongside the device availability topic, under
//     mode "all". `availability_topics_no_entity_reads` is down to the
//     single connection_state topic (F7, deliberately left).

var updateTopicsGolden = flag.Bool("update-topics-golden", false,
	"rewrite internal/bridge/testdata/topics.json from the current builders")

// mapping.yaml is 92 KB and both catalogues are parsed from it on every
// call; the pins call them a few dozen times. Parse once, read-only after.
var (
	pinEntries = sync.OnceValues(func() ([]*profile.Entry, error) {
		return pincatalog.Build("../../mapping.yaml")
	})
	pinCatalog = sync.OnceValues(func() (*mapping.Catalog, error) {
		return mapping.Load("../../mapping.yaml")
	})
)

const (
	pinQoS    = mqtt.QoS1
	pinDevice = "Geschirrspüler"
	pinRoot   = "homeconnect"
	pinPrefix = "homeassistant"
)

// topicGolden is the pinned topic tree. Every list is sorted.
type topicGolden struct {
	// BridgeStatusTopic is the daemon's own status topic — the one the
	// Last Will writes.
	BridgeStatusTopic string `json:"bridge_status_topic"`
	// DeviceAvailabilityTopics is what the device workers write.
	DeviceAvailabilityTopics []string `json:"device_availability_topics"`
	// AvailabilityTopicsNoEntityReads is published, referenced by no
	// discovery payload. Since F1 that is connection_state alone (F7,
	// deliberately left); the bridge status topic and the device
	// availability topic are both declared by every payload.
	AvailabilityTopicsNoEntityReads []string `json:"availability_topics_no_entity_reads"`
	// StateTopics is every topic the device worker publishes a value to.
	StateTopics []string `json:"state_topics"`
	// AdvertisedStateTopics is what the discovery payloads tell Home
	// Assistant to read.
	AdvertisedStateTopics []string `json:"advertised_state_topics"`
	// AdvertisedCommandTopics is what they tell it to write to.
	AdvertisedCommandTopics []string `json:"advertised_command_topics"`
	// StateTopicsWithoutEntity is published, but no entity reads it.
	StateTopicsWithoutEntity []string `json:"state_topics_without_entity"`
	// SubscribeFilters is what the daemon asks the broker for, with the
	// QoS it asks at.
	SubscribeFilters []filterQoS `json:"subscribe_filters"`
	// PublishQoSRetain is the delivery guarantee of each publish plane.
	PublishQoSRetain map[string]string `json:"publish_qos_retain"`
}

type filterQoS struct {
	Filter string `json:"filter"`
	QoS    int    `json:"qos"`
	// Options names the mqtt.SubscribeOption constructors the daemon
	// passed. mqtt.SubscribeOption is a closure over an unexported struct,
	// so a recorder cannot apply one and read the result back; the
	// constructor's own name is the only thing observable from outside the
	// library, and it is what distinguishes a No-Local subscription from a
	// plain one. Nil, not empty, for a subscription with no options, so an
	// added option is visible in the golden diff as an added key.
	Options []string `json:"options,omitempty"`
}

// optionNames maps subscribe options back to the exported constructors
// that produced them, via the closure's own symbol name
// ("github.com/SukramJ/go-mqtt.WithNoLocal.func1").
func optionNames(opts []mqtt.SubscribeOption) []string {
	if len(opts) == 0 {
		return nil
	}
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		name := runtime.FuncForPC(reflect.ValueOf(o).Pointer()).Name()
		if i := strings.LastIndex(name, "."); i >= 0 && strings.HasPrefix(name[i+1:], "func") {
			name = name[:i]
		}
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		out = append(out, name)
	}
	return out
}

// subRecorder records subscriptions with their QoS — the argument every
// other stub in this repository discards — and doubles as a minimal
// broker: it holds a retained tree and flushes the matching part of it to
// a fresh subscriber inline, which is what a real broker does and what
// publisher.Runtime's snapshot window depends on. A window that opens
// before its subscription exists sees none of the messages it was opened
// for.
type subRecorder struct {
	mu       sync.Mutex
	filters  []filterQoS
	pubs     []pubCall
	handlers map[string]mqtt.MessageHandler
	// retained is the broker-side tree the sweep reads. Seeded by a test;
	// this stub deliberately does NOT add this daemon's own publishes to
	// it, so a sweep pin says exactly what it was given.
	retained map[string][]byte
	// fail names one topic this stub refuses, so a test can drive the
	// difference between a document that was BUILT and one that was
	// PUBLISHED. A refused publish is recorded nowhere: the broker did not
	// take it.
	fail string
}

// setFail makes the stub refuse one topic. Empty clears the refusal.
func (s *subRecorder) setFail(topic string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = topic
}

// publishedTopics is every non-retraction publish the stub accepted.
func (s *subRecorder) publishedTopics() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.pubs {
		if !p.retraction {
			out = append(out, p.topic)
		}
	}
	return out
}

// discoveryWindows is every snapshot subscription the sweep opened. The
// SUBSCRIBE list is read rather than the live handler map, because the
// window unsubscribes on the way out: a pin that read the map could not
// tell a sweep that ran from one that never started.
func (s *subRecorder) discoveryWindows(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, f := range s.filters {
		if strings.HasPrefix(f.Filter, prefix) {
			out = append(out, f.Filter)
		}
	}
	return out
}

type pubCall struct {
	topic  string
	qos    mqtt.QoS
	retain bool
	// retraction records an empty retained payload, which is MQTT's
	// deletion of a retained message. It is a separate field rather than
	// len(payload)==0 at the read site because the sweep pins care about
	// nothing else, and a recorder that kept every payload would make
	// them compare 687 discovery bodies to count one deletion.
	retraction bool
}

func (s *subRecorder) Publish(_ context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != "" && topic == s.fail {
		return errors.New("subRecorder: refused " + topic)
	}
	s.pubs = append(s.pubs, pubCall{topic, qos, retain, len(payload) == 0 && retain})
	return nil
}

func (s *subRecorder) Subscribe(_ context.Context, filter string, qos mqtt.QoS, h mqtt.MessageHandler, opts ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append(s.filters, filterQoS{filter, int(qos), optionNames(opts)})
	if s.handlers == nil {
		s.handlers = map[string]mqtt.MessageHandler{}
	}
	s.handlers[filter] = h
	replay := make([]*mqtt.Message, 0, len(s.retained))
	for topic, payload := range s.retained {
		if matchFilter(filter, topic) {
			replay = append(replay, &mqtt.Message{Topic: topic, Payload: payload, Retain: true})
		}
	}
	s.mu.Unlock()
	for _, msg := range replay {
		h(msg)
	}
	s.mu.Lock()
	return mqtt.SubscribeResult{}, nil
}

func (s *subRecorder) Unsubscribe(context.Context, string) error { return nil }

// planeFor builds the REAL go-hamqtt publish plane over the recorder, at
// a chosen MQTT_QOS and MQTT_RETAIN. It is the same construction
// cmd/homeconnect2mqtt performs, so every assertion made against it is an
// assertion about the shipped composition root.
func planeFor(t *testing.T, rec *subRecorder, qos int, retain bool) *haplane.Plane {
	t.Helper()
	return haplane.New(hagomqtt.Transport(rec), haplane.Config{
		Prefix:      pinPrefix,
		StatusTopic: layout.Bridge(pinRoot),
		Layout:      hass.NewLayout(pinRoot),
		QoS:         haplane.QoS(qos),
		Retain:      retain,
		Logger:      slog.New(slog.DiscardHandler),
	})
}

// pinBridge wires a REAL Bridge and a REAL hass.Discovery over the pin
// catalogue and the shipped defaults, with the publish/subscribe calls
// recorded rather than sent.
func pinBridge(t *testing.T) (*Bridge, *Device, *hass.Discovery, *subRecorder) {
	t.Helper()
	return pinBridgeQoS(t, int(pinQoS))
}

// pinBridgeQoS is pinBridge with MQTT_QOS chosen by the caller, so the
// operator-facing promise of MQTT_QOS: 0 can be asserted off the recorded
// transport calls rather than off the constant that produced them (F9).
func pinBridgeQoS(t *testing.T, qos int) (*Bridge, *Device, *hass.Discovery, *subRecorder) {
	t.Helper()
	entries, err := pinEntries()
	if err != nil {
		t.Fatalf("pincatalog: %v", err)
	}
	cat, err := pinCatalog()
	if err != nil {
		t.Fatalf("mapping.Load: %v", err)
	}
	desc := &profile.Description{Info: pincatalog.Info, Entries: entries}

	cfg := testCfg()
	cfg.MQTTQoS = qos
	cfg.MQTTTopic = pinRoot
	cfg.Language = "de"
	cfg.HASSEnable = true
	cfg.HASSBaseTopic = pinPrefix

	rec := &subRecorder{}
	logger := slog.New(slog.DiscardHandler)
	// The REAL publish plane over the recorder, so every assertion below
	// reads the QoS byte and the retain flag the transport was handed
	// rather than the constant that fed them. That is what lets the F9
	// pin survive a whole plane moving: a step that re-routes these calls
	// through another library still has to hand the transport a 0.
	plane := planeFor(t, rec, qos, cfg.RetainEnabled())
	// HASS_DISCOVERY defaults to "curated"; the pin uses the full set so
	// the topic tree is the widest one this daemon can produce.
	disc := hass.New(plane, pinPrefix, pinRoot, cfg.Language, false, logger)
	disc.SetEnricher(cat)

	b, err := New(Deps{
		Config: cfg,
		MQTT:   rec,
		Plane:  plane,
		Logger: logger,
		HASS:   disc,
		Devices: []DeviceSpec{{
			Config: profile.DeviceConfig{
				Name: pinDevice, Host: "192.168.1.50",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: desc,
		}},
	})
	if err != nil {
		t.Fatalf("bridge.New: %v", err)
	}
	return b, b.devices[0], disc, rec
}

// realStateTopics asks the production expression from device.go:250 —
// d.topics.state(e) — for every entity, which is exactly the topic the
// publish drain receives.
func realStateTopics(d *Device) []string {
	entities := d.app.Entities()
	out := make([]string, 0, len(entities))
	for _, e := range entities {
		out = append(out, d.topics.state(e))
	}
	sort.Strings(out)
	return out
}

// advertised reads state_topic and command_topic out of the real
// discovery payloads, produced by the real builder.
func advertised(t *testing.T, dev *Device) (states, commands map[string]string) {
	t.Helper()
	states, commands = map[string]string{}, map[string]string{}
	for cfgTopic, p := range renderPayloads(t, dev) {
		if st, ok := p["state_topic"].(string); ok {
			states[cfgTopic] = st
		}
		if ct, ok := p["command_topic"].(string); ok {
			commands[cfgTopic] = ct
		}
	}
	return states, commands
}

// renderPayloads drives the real hass.Discovery over the device's real
// entities and returns config topic -> decoded payload.
func renderPayloads(t *testing.T, dev *Device) map[string]map[string]any {
	t.Helper()
	cat, err := pinCatalog()
	if err != nil {
		t.Fatalf("mapping.Load: %v", err)
	}
	pr := &payloadRecorder{payloads: map[string]map[string]any{}}
	d := hass.New(pr, pinPrefix, pinRoot, "de", false, slog.New(slog.DiscardHandler))
	d.SetEnricher(cat)
	d.PublishDevice(context.Background(), dev.name, dev.app.Info(), dev.app.Entities())
	if len(pr.payloads) == 0 {
		t.Fatal("the discovery builder published nothing")
	}
	return pr.payloads
}

type payloadRecorder struct {
	mu       sync.Mutex
	payloads map[string]map[string]any
}

// PublishBundle satisfies hass.ConfigWriter. The topic pin drives the
// per-entity builder, which is the form the pinned tree records.
func (p *payloadRecorder) PublishBundle(context.Context, *discovery.Bundle) (bool, error) {
	return false, errors.New("payloadRecorder: the device document path is not driven here")
}

func (p *payloadRecorder) Publish(_ context.Context, topic string, payload []byte) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err == nil {
		p.payloads[topic] = m
	}
	return true, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedValues(m map[string]string) []string {
	set := map[string]bool{}
	for _, v := range m {
		set[v] = true
	}
	return sortedKeys(set)
}

// TestTopicGolden pins the whole non-discovery topic tree.
func TestTopicGolden(t *testing.T) {
	b, dev, _, rec := pinBridge(t)

	if err := b.subscribeCommands(context.Background()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	rec.mu.Unlock()
	// The orphan sweep's snapshot window. It is transient — installed for
	// publisher.Config.SweepWindow and taken down again — so it is not in
	// the recorder after subscribeCommands; it is measured off the
	// transport by TestSweepWindowIsTheOnlyDiscoverySubscription and
	// stated here, once, in the same shape.
	//
	// It replaced the two narrower filters the hand-rolled reconcile
	// installed (homeassistant/+/+/+/config and
	// homeassistant/+/<slug>/+/config, both hard-wired to QoS 0). One
	// window at MQTT_QOS is what publisher.Runtime.Sweep opens, because it
	// parses all three discovery topic forms out of one subscription
	// rather than encoding one of them in a filter.
	filters = append(filters, filterQoS{Filter: pinPrefix + "/#", QoS: int(pinQoS)})
	sort.Slice(filters, func(i, j int) bool { return filters[i].Filter < filters[j].Filter })

	states, commands := advertised(t, dev)
	advertisedState := map[string]bool{}
	for _, st := range states {
		advertisedState[st] = true
	}
	orphanState := []string{}
	for _, st := range realStateTopics(dev) {
		if !advertisedState[st] {
			orphanState = append(orphanState, st)
		}
	}

	availTopics := []string{dev.topics.Availability(), dev.topics.ConnectionState()}
	sort.Strings(availTopics)
	// Read the availability LIST, which is what the payloads carry since
	// F1; the flat availability_topic key is gone.
	referenced := map[string]bool{}
	for _, p := range mustPayloads(t, dev) {
		list, _ := p["availability"].([]any)
		for _, src := range list {
			if m, ok := src.(map[string]any); ok {
				if at, ok := m["topic"].(string); ok {
					referenced[at] = true
				}
			}
		}
	}
	unreferenced := []string{}
	for _, at := range append([]string{layout.Bridge(pinRoot)}, availTopics...) {
		if !referenced[at] {
			unreferenced = append(unreferenced, at)
		}
	}
	sort.Strings(unreferenced)

	got := topicGolden{
		BridgeStatusTopic:               layout.Bridge(pinRoot),
		DeviceAvailabilityTopics:        availTopics,
		AvailabilityTopicsNoEntityReads: unreferenced,
		StateTopics:                     realStateTopics(dev),
		AdvertisedStateTopics:           sortedValues(states),
		AdvertisedCommandTopics:         sortedValues(commands),
		StateTopicsWithoutEntity:        orphanState,
		SubscribeFilters:                filters,
		PublishQoSRetain: map[string]string{
			"discovery_config":    "qos=MQTT_QOS retain=true",
			"discovery_retract":   "qos=MQTT_QOS retain=true (empty payload)",
			"entity_state":        "qos=MQTT_QOS retain=MQTT_RETAIN",
			"device_availability": "qos=MQTT_QOS retain=MQTT_RETAIN",
			"bridge_status":       "qos=MQTT_QOS retain=true",
			"bridge_will":         "qos=MQTT_QOS retain=true",
		},
	}

	path := filepath.Join("testdata", "topics.json")
	pretty, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')
	if *updateTopicsGolden {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(pretty))
		return
	}
	raw, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("read %s (regenerate with -update-topics-golden): %v", path, err)
	}
	var want topicGolden
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("golden topics.json is not valid json: %v", err)
	}
	if want.BridgeStatusTopic != got.BridgeStatusTopic {
		t.Errorf("bridge status topic changed: %q -> %q", want.BridgeStatusTopic, got.BridgeStatusTopic)
	}
	diffTopics(t, "device_availability_topics", want.DeviceAvailabilityTopics, got.DeviceAvailabilityTopics)
	diffTopics(t, "availability_topics_no_entity_reads", want.AvailabilityTopicsNoEntityReads, got.AvailabilityTopicsNoEntityReads)
	diffTopics(t, "state_topics", want.StateTopics, got.StateTopics)
	diffTopics(t, "advertised_state_topics", want.AdvertisedStateTopics, got.AdvertisedStateTopics)
	diffTopics(t, "advertised_command_topics", want.AdvertisedCommandTopics, got.AdvertisedCommandTopics)
	diffTopics(t, "state_topics_without_entity", want.StateTopicsWithoutEntity, got.StateTopicsWithoutEntity)
	diffFilters(t, want.SubscribeFilters, got.SubscribeFilters)
	for k, v := range want.PublishQoSRetain {
		if got.PublishQoSRetain[k] != v {
			t.Errorf("publish_qos_retain[%s]: %q -> %q", k, v, got.PublishQoSRetain[k])
		}
	}
}

func mustPayloads(t *testing.T, dev *Device) []map[string]any {
	t.Helper()
	rendered := renderPayloads(t, dev)
	out := make([]map[string]any, 0, len(rendered))
	for _, p := range rendered {
		out = append(out, p)
	}
	return out
}

func diffTopics(t *testing.T, name string, want, got []string) {
	t.Helper()
	inWant, inGot := map[string]bool{}, map[string]bool{}
	for _, s := range want {
		inWant[s] = true
	}
	for _, s := range got {
		inGot[s] = true
	}
	for _, s := range want {
		if !inGot[s] {
			t.Errorf("%s: topic no longer produced: %s", name, s)
		}
	}
	for _, s := range got {
		if !inWant[s] {
			t.Errorf("%s: new topic not in golden: %s", name, s)
		}
	}
}

func diffFilters(t *testing.T, want, got []filterQoS) {
	t.Helper()
	w, _ := json.Marshal(want)
	g, _ := json.Marshal(got)
	if !bytes.Equal(w, g) {
		t.Errorf("subscribe filters changed:\n golden: %s\n  built: %s", w, g)
	}
}

// TestStateTopicBuildersAgree is F3's pin. Every state_topic a discovery
// payload advertises must be one the device worker actually publishes to
// — checked BUILDER against BUILDER, never against the golden file, so a
// change to either one alone fails here even after a regeneration.
func TestStateTopicBuildersAgree(t *testing.T) {
	_, dev, _, _ := pinBridge(t)

	published := map[string]bool{}
	for _, st := range realStateTopics(dev) {
		published[st] = true
	}
	states, _ := advertised(t, dev)
	if len(states) == 0 {
		t.Fatal("no state topics advertised")
	}
	for configTopic, st := range states {
		if !published[st] {
			t.Errorf("%s advertises %s, which the device worker never publishes to — "+
				"internal/hass/discovery.go and internal/bridge/publish.go have diverged (F3)",
				configTopic, st)
		}
	}
	t.Logf("F3: %d advertised state topics, all produced by the other builder too", len(states))
}

// TestCommandTopicsAreSubscribed is the same class of divergence on the
// inbound half: every command_topic advertised must fall under the filter
// the daemon actually subscribes to.
func TestCommandTopicsAreSubscribed(t *testing.T) {
	b, dev, _, rec := pinBridge(t)
	if err := b.subscribeCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	rec.mu.Unlock()

	_, commands := advertised(t, dev)
	if len(commands) == 0 {
		t.Fatal("no command topics advertised")
	}
	for configTopic, ct := range commands {
		covered := false
		for _, f := range filters {
			if matchFilter(f.Filter, ct) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s advertises command topic %s, which no subscription covers: %v", configTopic, ct, filters)
		}
	}
	t.Logf("%d advertised command topics, all covered by %v", len(commands), filters)
}

// TestAdvertisedCommandTopicsResolveToSomethingThatHandlesThem closes the
// half of F3 that TestStateTopicBuildersAgree never looked at: the
// OUTBOUND-to-INBOUND round trip.
//
// Being covered by the subscribe filter (TestCommandTopicsAreSubscribed)
// only proves the message arrives. It does not prove anything acts on it.
// The device sub-tree filter is "<root>/<device>/#", so a command topic
// built by a path the handler does not recognise is delivered, matched,
// dispatched — and dropped, with a log line at most. That is exactly the
// shape the two synthetic program buttons had: internal/hass composed
// "_control/<key>/set" inline, internal/bridge compared against its own
// "_control/start_program" constants, and nothing compared the two
// spellings. A rename on either side turns both buttons into no-ops that
// look, from Home Assistant, exactly like working buttons.
//
// Every advertised command_topic must therefore be either a synthetic
// control the handler recognises, or a feature the resolver can find.
func TestAdvertisedCommandTopicsResolveToSomethingThatHandlesThem(t *testing.T) {
	b, dev, _, _ := pinBridge(t)
	_, commands := advertised(t, dev)
	if len(commands) == 0 {
		t.Fatal("no command topics advertised")
	}

	controls, features := 0, 0
	for cfgTopic, ct := range commands {
		rel, ok := dev.topics.Relative(ct)
		if !ok {
			t.Errorf("%s advertises %s, which is not a command topic of %s",
				cfgTopic, ct, dev.topics.Base())
			continue
		}
		switch rel {
		case layout.ControlPath(layout.ControlStartProgram), layout.ControlPath(layout.ControlStopProgram):
			controls++
			continue
		}
		if _, ok := b.resolveEntity(dev, rel); !ok {
			t.Errorf("%s advertises %s: the handler neither recognises it as a "+
				"synthetic control nor resolves %q to a feature — the discovery "+
				"builder and the command handler have diverged (F3)", cfgTopic, ct, rel)
			continue
		}
		features++
	}
	if controls != 2 {
		t.Errorf("synthetic control command topics = %d, want 2 (start + stop)", controls)
	}
	t.Logf("F3: %d advertised command topics — %d features, %d synthetic controls, all handled",
		len(commands), features, controls)
}

// TestCommandFilterIsGuardedAgainstTheDaemonsOwnTree is F4, after the fix.
//
// The filter itself is unchanged and cannot change: the feature path is
// variable-depth, so no fixed-arity filter covers the command tree. What
// changed is that the sub-tree is no longer unguarded. Two guards, because
// the two halves are different mechanisms:
//
//   - MQTT 5.0 No Local, so the broker does not forward this daemon's own
//     publishes back to it at all. Recorded in the golden as an option on
//     the filter, which is the only place it is observable from outside.
//   - Device.Relative, checked before anything is dispatched. No Local is
//     a no-op on a 3.1.1 link and does not cover the retained replay a
//     broker delivers on (re)subscribe, and under MQTT_RETAIN: false the
//     retained check does not fire either. Before the fix, every one of
//     the 689 own publishes below spawned a goroutine that returned.
func TestCommandFilterIsGuardedAgainstTheDaemonsOwnTree(t *testing.T) {
	b, dev, _, rec := pinBridge(t)
	if err := b.subscribeCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	rec.mu.Unlock()

	var deviceFilter string
	for _, f := range filters {
		if strings.HasSuffix(f.Filter, "/#") {
			deviceFilter = f.Filter
		}
	}
	if deviceFilter == "" {
		t.Fatal("no device sub-tree subscription — F4 is fixed; update this test and the golden together")
	}
	var noLocal bool
	for _, f := range filters {
		if f.Filter == deviceFilter {
			for _, o := range f.Options {
				if o == "WithNoLocal" {
					noLocal = true
				}
			}
		}
	}
	if !noLocal {
		t.Errorf("%s is subscribed without mqtt.WithNoLocal — the broker will "+
			"forward this daemon's own publishes straight back to it (F4)", deviceFilter)
	}

	ownTopics := append(realStateTopics(dev), dev.topics.Availability(), dev.topics.ConnectionState())
	for _, own := range ownTopics {
		if !matchFilter(deviceFilter, own) {
			t.Errorf("%s is published but not matched by %s — the filter narrowed; "+
				"update this test and the golden together", own, deviceFilter)
		}
		if shouldDispatch(dev, own, false) {
			t.Errorf("%s is one of this daemon's own publishes, but the handler "+
				"would dispatch it (F4)", own)
		}
	}

	// The guard must not be so eager that it drops real commands, and it
	// must still drop the broker's retained replay of one.
	_, commands := advertised(t, dev)
	for cfgTopic, ct := range commands {
		if !shouldDispatch(dev, ct, false) {
			t.Errorf("%s advertises %s, which the handler would not dispatch", cfgTopic, ct)
		}
		if shouldDispatch(dev, ct, true) {
			t.Errorf("%s: a RETAINED replay of %s would be dispatched — a stale "+
				"command re-fires its write on every reconnect", cfgTopic, ct)
		}
	}
	t.Logf("F4: %s still matches %d of this daemon's own publishes; No Local stops the "+
		"broker sending them and Relative rejects all %d before dispatch",
		deviceFilter, len(ownTopics), len(ownTopics))
}

// matchFilter is a minimal MQTT topic-filter matcher, sufficient for the
// filters this daemon uses (+ and a trailing #).
func matchFilter(filter, topic string) bool {
	f := strings.Split(filter, "/")
	tp := strings.Split(topic, "/")
	for i, seg := range f {
		if seg == "#" {
			return i <= len(tp)
		}
		if i >= len(tp) {
			return false
		}
		if seg != "+" && seg != tp[i] {
			return false
		}
	}
	return len(f) == len(tp)
}

// TestQoSZeroReachesTheTransportAsQoSZero is F9, pinned where it can be
// seen to break.
//
// MQTT_QOS is validated to 0..1 and an operator who sets 0 is asking for
// at-most-once. Today mqtt.QoS(0) means exactly that. In the go-hamqtt
// publisher vocabulary this migration moves onto, QoS(0) means *unset* and
// resolves to QoS 1, so the deliberate choice would be silently upgraded
// the moment the plane moves — with nothing in the config, the log or the
// payloads to show it.
//
// The assertion is deliberately made on the QoS the TRANSPORT was handed,
// on every publish and every subscribe the daemon makes, not on the
// constant that fed them. That is what lets the pin survive the whole
// plane being replaced: a step that re-routes these calls through another
// library still has to hand the transport a 0.
func TestQoSZeroReachesTheTransportAsQoSZero(t *testing.T) {
	b, dev, _, rec := pinBridgeQoS(t, 0)

	if err := b.subscribeCommands(context.Background()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	b.publishDiscovery(context.Background(), dev)
	b.publish(dev.topics.Availability(), []byte("online"))

	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	pubs := append([]pubCall(nil), rec.pubs...)
	rec.mu.Unlock()

	if len(pubs) == 0 || len(filters) == 0 {
		t.Fatalf("nothing recorded: %d publishes, %d subscribes", len(pubs), len(filters))
	}
	for _, p := range pubs {
		if p.qos != mqtt.QoS0 {
			t.Errorf("publish %s: qos = %v, want QoS0 — MQTT_QOS: 0 was silently upgraded", p.topic, p.qos)
		}
	}
	for _, f := range filters {
		// The two transient reconcile filters are hard-wired to QoS 0 and
		// are not recorded here; these are the command and birth ones,
		// which follow MQTT_QOS.
		if f.QoS != int(mqtt.QoS0) {
			t.Errorf("subscribe %s: qos = %d, want 0", f.Filter, f.QoS)
		}
	}
	t.Logf("F9: MQTT_QOS: 0 reached the transport as QoS 0 on %d publishes and %d subscribes",
		len(pubs), len(filters))
}
