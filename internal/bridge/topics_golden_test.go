// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
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
//   - F4 — the command subscription is the whole device sub-tree,
//     <root>/<device>/#, so the broker echoes every state publish this
//     daemon makes straight back to it. Pinned by
//     TestCommandFilterSwallowsTheDaemonsOwnStateTree.
//   - F7 — the bridge status topic <root>/status carries the Last Will,
//     and no entity references it. Pinned here as
//     `availability_topics_no_entity_reads`.

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
	// AvailabilityTopicsNoEntityReads is F7: published, referenced by no
	// discovery payload.
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
}

// subRecorder records subscriptions with their QoS — the argument every
// other stub in this repository discards.
type subRecorder struct {
	mu      sync.Mutex
	filters []filterQoS
	pubs    []pubCall
}

type pubCall struct {
	topic  string
	qos    mqtt.QoS
	retain bool
}

func (s *subRecorder) Publish(_ context.Context, topic string, _ []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pubs = append(s.pubs, pubCall{topic, qos, retain})
	return nil
}

func (s *subRecorder) Subscribe(_ context.Context, filter string, qos mqtt.QoS, _ mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append(s.filters, filterQoS{filter, int(qos)})
	return mqtt.SubscribeResult{}, nil
}

func (s *subRecorder) Unsubscribe(context.Context, string) error { return nil }

// pinBridge wires a REAL Bridge and a REAL hass.Discovery over the pin
// catalogue and the shipped defaults, with the publish/subscribe calls
// recorded rather than sent.
func pinBridge(t *testing.T) (*Bridge, *Device, *hass.Discovery, *subRecorder) {
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
	cfg.MQTTTopic = pinRoot
	cfg.Language = "de"
	cfg.HASSEnable = true
	cfg.HASSBaseTopic = pinPrefix

	rec := &subRecorder{}
	logger := slog.New(slog.DiscardHandler)
	// HASS_DISCOVERY defaults to "curated"; the pin uses the full set so
	// the topic tree is the widest one this daemon can produce.
	disc := hass.New(rec, pinPrefix, pinRoot, mqtt.QoS(cfg.MQTTQoS), cfg.Language, false, logger) //nolint:gosec // fixed test value
	disc.SetEnricher(cat)

	b, err := New(Deps{
		Config: cfg,
		MQTT:   rec,
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
	for topic, p := range renderPayloads(t, dev) {
		if st, ok := p["state_topic"].(string); ok {
			states[topic] = st
		}
		if ct, ok := p["command_topic"].(string); ok {
			commands[topic] = ct
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
	d := hass.New(pr, pinPrefix, pinRoot, pinQoS, "de", false, slog.New(slog.DiscardHandler))
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

func (p *payloadRecorder) Publish(_ context.Context, topic string, payload []byte, _ mqtt.QoS, _ bool, _ ...mqtt.PublishOption) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err == nil {
		p.payloads[topic] = m
	}
	return nil
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
	b, dev, disc, rec := pinBridge(t)

	if err := b.subscribeCommands(context.Background()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	rec.mu.Unlock()
	// The two transient reconcile filters are built by the production
	// code too; ask it rather than re-deriving the strings here.
	filters = append(filters,
		filterQoS{disc.ConfigFilter(), int(mqtt.QoS0)},
		filterQoS{disc.DeviceConfigFilter(dev.name), int(mqtt.QoS0)})
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

	availTopics := []string{dev.topics.availability(), dev.topics.connectionState()}
	sort.Strings(availTopics)
	referenced := map[string]bool{}
	for _, p := range mustPayloads(t, dev) {
		if at, ok := p["availability_topic"].(string); ok {
			referenced[at] = true
		}
	}
	unreferenced := []string{}
	for _, at := range append([]string{pinRoot + "/status"}, availTopics...) {
		if !referenced[at] {
			unreferenced = append(unreferenced, at)
		}
	}
	sort.Strings(unreferenced)

	got := topicGolden{
		BridgeStatusTopic:               pinRoot + "/status",
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
			"bridge_will":         "qos=0 retain=true",
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

// TestCommandFilterSwallowsTheDaemonsOwnStateTree is F4. The command
// subscription is the whole device sub-tree, so the broker echoes every
// state, availability and connection-state publish this daemon makes
// straight back to it. It is dropped, but only after the broker has sent
// it — and only because the retained bit is checked first. With
// MQTT_RETAIN: false the drop no longer applies and every state publish
// spawns a goroutine that immediately returns.
//
// Pinned as current behaviour.
func TestCommandFilterSwallowsTheDaemonsOwnStateTree(t *testing.T) {
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
	own := append(realStateTopics(dev), dev.topics.availability(), dev.topics.connectionState())
	for _, topic := range own {
		if !matchFilter(deviceFilter, topic) {
			t.Errorf("%s is published but not matched by %s", topic, deviceFilter)
		}
	}
	t.Logf("F4: %s echoes %d of this daemon's own publishes back to it", deviceFilter, len(own))
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
