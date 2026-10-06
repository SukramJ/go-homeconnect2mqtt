// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
)

// The pins for the planes that moved onto go-hamqtt at ADR 0070 phase 7
// step 5. Every delivery-guarantee assertion here reads the QoS byte and
// the retain flag off the TRANSPORT call, never off the constant that
// produced it — that is what let the F9 pin survive this whole plane
// moving, and it is what has to keep being true for the next step.

// logSink captures log records so a handler whose only other effect is a
// network call can still be observed.
type logSink struct {
	mu      sync.Mutex
	records []slog.Record
}

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r.Clone())
	return nil
}

func (s *logSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *logSink) WithGroup(string) slog.Handler      { return s }

func (s *logSink) sawMessage(msg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].Message == msg {
			return true
		}
	}
	return false
}

// TestStatusItemsAreRetainedAtQoSZero drives the real state plane and
// reads both flags off the transport.
//
// The state plane is where entity values, an appliance's `online` item and
// its connection_state go. Since 0.15.0 every one of them is a retained
// status item at QoS 0 (mqtt-smarthome 2.0 §3.2, §4): MQTT_RETAIN is gone
// and MQTT_QOS no longer reaches this plane, so both operator settings are
// driven here to show they change nothing.
func TestStatusItemsAreRetainedAtQoSZero(t *testing.T) {
	for _, mqttQoS := range []int{0, 1} {
		b, dev, _, rec := pinBridgeQoS(t, mqttQoS)

		b.publish(dev.topics.Online(), true)
		b.publish(dev.topics.ConnectionState(), "connected")
		e := dev.app.Entities()[0]
		b.publish(dev.topics.state(e), "value")

		rec.mu.Lock()
		pubs := append([]pubCall(nil), rec.pubs...)
		rec.mu.Unlock()
		if len(pubs) != 3 {
			t.Fatalf("MQTT_QOS %d: %d publishes, want 3", mqttQoS, len(pubs))
		}
		for _, p := range pubs {
			if p.qos != mqtt.QoS0 {
				t.Errorf("MQTT_QOS %d: %s reached the transport at QoS %v, want 0", mqttQoS, p.topic, p.qos)
			}
			if !p.retain {
				t.Errorf("%s reached the transport unretained", p.topic)
			}
			if !strings.HasPrefix(string(p.payload), `{"val":`) {
				t.Errorf("%s carried %q, want a status object", p.topic, p.payload)
			}
		}
	}
}

// TestCommandFilterIsDisjointFromTheStateTree is F4's resolution written as
// an assertion.
//
// Before 0.15.0 this file held the opposite: the command filter was the
// whole device sub-tree, "<root>/<device>/#", which matched every state
// topic by construction, so neither publisher.StateConfig.CommandFilters
// nor a disjointness check could be used — only the "/set" SUFFIX
// separated a command from a state, and no MQTT filter can say that. The
// function now sits at the second level: the filter matches no state
// topic, every command topic, and the state plane is TOLD `<name>/set/#`
// and refuses a publish into it.
func TestCommandFilterIsDisjointFromTheStateTree(t *testing.T) {
	t.Parallel()
	b, dev, _, rec := pinBridge(t)
	filter := dev.topics.CommandFilter()

	for _, st := range append(realStateTopics(dev), dev.topics.Online(), dev.topics.ConnectionState()) {
		if publisher.MatchFilter(filter, st) {
			t.Errorf("the command filter %s matches the status item %s", filter, st)
		}
		if _, ok := dev.topics.Relative(st); ok {
			t.Errorf("%s is a status item and Relative accepted it as a command", st)
		}
	}
	_, commands := advertised(t, dev)
	for cfgTopic, ct := range commands {
		if !publisher.MatchFilter(filter, ct) {
			t.Errorf("%s advertises %s, which %s does not match", cfgTopic, ct, filter)
		}
		if _, ok := dev.topics.Relative(ct); !ok {
			t.Errorf("%s advertises %s, which Relative does not accept as a command", cfgTopic, ct)
		}
	}
	// The guard the old filter made unusable.
	for _, ct := range commands {
		if err := b.plane.PublishStatus(t.Context(), ct, 1); err == nil {
			t.Errorf("the state plane accepted a status publish into the command tree (%s)", ct)
		}
		break
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.pubs) != 0 {
		t.Errorf("%d publishes reached the transport", len(rec.pubs))
	}
}

// TestCommandRouterRoutesEveryAdvertisedCommandTopic proves the router
// claims what the discovery configs advertise — the outbound-to-inbound
// round trip, now across a library boundary.
func TestCommandRouterRoutesEveryAdvertisedCommandTopic(t *testing.T) {
	b, dev, _, _ := pinBridge(t)
	if err := b.subscribeCommands(t.Context()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	t.Cleanup(b.stopCommands)

	_, commands := advertised(t, dev)
	if len(commands) == 0 {
		t.Fatal("no command topics advertised")
	}
	for cfgTopic, ct := range commands {
		if !b.commands.Claims(ct) {
			t.Errorf("%s advertises %s, which no route claims", cfgTopic, ct)
		}
	}
	// And the daemon's own state tree no longer arrives on the route —
	// F4, resolved by the layout — while the guard before dispatch stays.
	own := realStateTopics(dev)[0]
	if b.commands.Claims(own) {
		t.Errorf("%s is claimed by a command route (F4)", own)
	}
	if shouldDispatch(dev, own, false) {
		t.Errorf("%s is one of this daemon's own publishes and the handler would dispatch it (F4)", own)
	}
}

// TestARoutedCommandReachesTheHandlerOffTheReadLoop drives a real
// delivery through the router.
//
// The move off the read loop is the point. go-mqtt delivers inline on the
// goroutine that also decodes PUBACK and PINGRESP, and handleSet makes
// blocking Home Connect calls with retry loops — the old code spawned an
// unbounded goroutine per delivery for exactly that reason, giving up
// ordering to get it. The router's worker pool keeps order per topic.
func TestARoutedCommandReachesTheHandlerOffTheReadLoop(t *testing.T) {
	b, dev, _, rec := pinBridge(t)
	sink := &logSink{}
	b.logger = slog.New(sink)
	if err := b.subscribeCommands(t.Context()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	t.Cleanup(b.stopCommands)

	rec.mu.Lock()
	handler := rec.handlers[dev.topics.CommandFilter()]
	rec.mu.Unlock()
	if handler == nil {
		t.Fatal("the router registered no handler for the device sub-tree")
	}

	// A command topic no feature backs: the handler resolves it, fails to
	// find a feature and logs, which is an observable effect that costs no
	// appliance connection.
	handler(&mqtt.Message{Topic: dev.topics.Set("No", "Such", "Feature"), Payload: []byte("1")})
	b.commands.WaitIdle()
	if !sink.sawMessage("bridge.command_unknown_feature") {
		t.Error("a routed command did not reach handleSet")
	}
}

// TestTheDaemonsOwnStateEchoIsNotDispatched is the F4 guard driven
// end to end rather than asserted on shouldDispatch alone.
func TestTheDaemonsOwnStateEchoIsNotDispatched(t *testing.T) {
	b, dev, _, rec := pinBridge(t)
	sink := &logSink{}
	b.logger = slog.New(sink)
	if err := b.subscribeCommands(t.Context()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	t.Cleanup(b.stopCommands)

	rec.mu.Lock()
	handler := rec.handlers[dev.topics.CommandFilter()]
	rec.mu.Unlock()

	for _, own := range []string{
		realStateTopics(dev)[0],
		dev.topics.Online(),
		dev.topics.ConnectionState(),
	} {
		handler(&mqtt.Message{Topic: own, Payload: []byte("online")})
	}
	// A retained replay of a REAL command topic must be dropped too: the
	// broker replays it on every (re)subscribe, and a stale command would
	// re-fire its write on every reconnect.
	_, commands := advertised(t, dev)
	for _, ct := range commands {
		handler(&mqtt.Message{Topic: ct, Payload: []byte("1"), Retain: true})
		break
	}
	b.commands.WaitIdle()
	for _, msg := range []string{"bridge.command_unknown_feature", "bridge.write_failed", "bridge.write_not_writable"} {
		if sink.sawMessage(msg) {
			t.Errorf("an echo of this daemon's own publish reached handleSet (%s)", msg)
		}
	}
}

// TestCommandRoutesAreUnambiguous is the property that keeps one
// published message from running a handler twice.
//
// A broker sends one PUBLISH copy per matching subscription, and a client
// that re-matches each copy against its whole filter list then calls
// every matching handler per copy — openccu-loom measured exactly that
// against Mosquitto and needed a separate connection to fix it. #41
// established that this daemon's two transient discovery-tree filters
// overlap but are never installed together; the sweep has since collapsed
// them into one window, so the question now is only about the command
// routes, and it has to be re-asked because the router is new.
//
// Three things are asserted, and the third is the one that would have to
// change if a device name ever became a prefix of another's:
//
//   - No overlap was ACCEPTED. publisher.CommandRouter.Attributed reports
//     whether the router had to fall back to MQTT 5.0 Subscription
//     Identifiers to tell two routes' deliveries apart; false means no
//     registered pair can both claim a topic.
//   - Every advertised command topic is claimed by exactly one route.
//   - Registration is closed after Start, so a route added later cannot
//     be checked against a set the caller has already acted on.
func TestCommandRoutesAreUnambiguous(t *testing.T) {
	b, dev, _, _ := pinBridge(t)
	if err := b.subscribeCommands(t.Context()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	t.Cleanup(b.stopCommands)

	if b.commands.Attributed() {
		t.Error("the router accepted an overlapping route pair — it now needs Subscription " +
			"Identifiers to tell their deliveries apart, which is a property this daemon " +
			"never asked for and which an MQTT 3.1.1 link cannot provide")
	}
	_, commands := advertised(t, dev)
	for cfgTopic, ct := range commands {
		claiming := 0
		for _, f := range b.commands.Filters() {
			if publisher.MatchFilter(f, ct) {
				claiming++
			}
		}
		if claiming != 1 {
			t.Errorf("%s advertises %s, claimed by %d routes (%v) — a broker sends one copy per "+
				"matching subscription", cfgTopic, ct, claiming, b.commands.Filters())
		}
	}
	if err := b.commands.Handle(pinRoot+"/#", func(context.Context, publisher.Command) {}); err == nil {
		t.Error("a route was accepted after Start, past the point the whole set was validated")
	}
}

// TestPerDeviceRoutesCannotOverlap: the routes are "<name>/set/<haId>/#"
// per configured device and bridge.New refuses a duplicate haId, so any two
// differ in a literal level. This asserts it over the shape
// rather than over one fixture's names, and asserts the router refuses a
// pair no specificity rule can order, which is the case attribution does
// NOT rescue.
func TestPerDeviceRoutesCannotOverlap(t *testing.T) {
	t.Parallel()
	router := publisher.NewCommandRouter(hagomqtt.Transport(&subRecorder{}),
		publisher.CommandConfig{Logger: slog.New(slog.DiscardHandler)})
	noop := func(context.Context, publisher.Command) {}
	for _, name := range []string{"Geschirrspüler", "Kühlschrank", "Backofen"} {
		f := testLayout(pinRoot).Device(haIDFor(name)).CommandFilter()
		if err := router.Handle(f, noop); err != nil {
			t.Fatalf("route %s: %v", f, err)
		}
	}
	if router.Attributed() {
		t.Error("three per-device routes were treated as overlapping")
	}
	// A pair no specificity rule can order stays refused on any transport.
	if err := router.Handle(pinRoot+"/+/BSH/+", noop); err == nil {
		if err2 := router.Handle(pinRoot+"/+/+/Common", noop); err2 == nil {
			t.Error("two equally-strong claims on the same topic were both accepted")
		}
	}
}

// TestWillRowMatchesTheWillTheRuntimeStates measures the one row of
// topics.json that was prose nothing checked.
//
// `publish_qos_retain.bridge_will` said "qos=0 retain=true" from #40 until
// this step, and the will had gone out at MQTT_QOS since #41 fixed F1 —
// the row went stale in the commit that changed the thing it describes,
// because nothing read it. This is what reads it.
func TestWillRowMatchesTheWillTheRuntimeStates(t *testing.T) {
	t.Parallel()
	for _, mqttQoS := range []int{0, 1} {
		_, _, _, rec := pinBridgeQoS(t, mqttQoS)
		will, err := planeFor(t, rec, mqttQoS).Will()
		if err != nil {
			t.Fatalf("will: %v", err)
		}
		if int(will.QoS) != mqttQoS {
			t.Errorf("MQTT_QOS %d: will qos = %d, and topics.json claims qos=MQTT_QOS", mqttQoS, will.QoS)
		}
		if !will.Retain {
			t.Error("the will is not retained, and topics.json claims retain=true")
		}
		if want := testLayout(pinRoot).Connected(); will.Topic != want || string(will.Payload) != "0" {
			t.Errorf("will = %s %q, want %s 0", will.Topic, will.Payload, want)
		}
		if strings.HasPrefix(will.Topic, pinPrefix+"/") {
			t.Errorf("will topic %q is inside Home Assistant's discovery tree", will.Topic)
		}
	}
}

// TestPublishOnlineRebuildsThePlaneAndAnnounces is the (re)connect hook.
//
// Announcing on every reconnect and not only at boot is load-bearing: the
// broker publishes the will on the drop, so a reconnected daemon that
// does not re-announce stays offline in Home Assistant while happily
// publishing state nobody displays. With no appliance connected yet, the
// level is 1: the broker is up, the upstream is not.
func TestPublishOnlineRebuildsThePlaneAndAnnounces(t *testing.T) {
	b, _, _, rec := pinBridge(t)
	before := b.plane.Runtime()

	b.PublishOnline(t.Context())

	if b.plane.Runtime() == before {
		t.Error("the discovery runtime survived the reconnect — its beliefs describe the old broker connection")
	}
	rec.mu.Lock()
	pubs := append([]pubCall(nil), rec.pubs...)
	rec.mu.Unlock()
	found := false
	connected := testLayout(pinRoot).Connected()
	for _, p := range pubs {
		if p.topic == connected && p.retain && p.qos == pinQoS && string(p.payload) == "1" {
			found = true
		}
	}
	if !found {
		t.Errorf("no retained `1` on %s at MQTT_QOS: %v", connected, pubs)
	}
}

// TestTheRouterItselfDropsARetainedDelivery isolates the router's own
// policy from [shouldDispatch]'s check, which would otherwise mask it.
//
// Both exist and both are wanted — a retained command is somebody's
// `mosquitto_pub -r` left behind and the broker replays it on every
// (re)subscribe, re-firing a stale write each time — but two statements
// of one rule mean neither is tested by a test that only watches the
// outcome. This watches the router alone, with no shouldDispatch in the
// path at all.
func TestTheRouterItselfDropsARetainedDelivery(t *testing.T) {
	b, _, _, _ := pinBridge(t)
	rec := &subRecorder{}
	// The PRODUCTION policy, read off the function the daemon wires the
	// router from — not a literal repeated here, which would pass whatever
	// the daemon actually does.
	router := publisher.NewCommandRouter(hagomqtt.Transport(rec), b.commandConfig(t.Context()))
	var (
		mu        sync.Mutex
		delivered []string
	)
	const filter = "root/dev/#"
	if err := router.Handle(filter, func(_ context.Context, cmd publisher.Command) {
		mu.Lock()
		delivered = append(delivered, cmd.Topic)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("route: %v", err)
	}
	if err := router.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = router.Stop(context.Background()) })

	rec.mu.Lock()
	h := rec.handlers[filter]
	rec.mu.Unlock()
	if h == nil {
		t.Fatal("no handler registered")
	}
	h(&mqtt.Message{Topic: "root/dev/a/set", Payload: []byte("1"), Retain: true})
	h(&mqtt.Message{Topic: "root/dev/b/set", Payload: []byte("1")})
	router.WaitIdle()

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 || delivered[0] != "root/dev/b/set" {
		t.Errorf("delivered %v, want only the live message — a retained command is replayed "+
			"on every (re)subscribe and would re-fire its write each time", delivered)
	}
}

// TestTheSweepWindowOverlapsTheBirthSubscriptionHarmlessly pins an
// overlap this step CREATED, so that it is a measured fact rather than a
// surprise.
//
// The sweep's snapshot window is `<prefix>/#`, which matches Home
// Assistant's own birth topic `<prefix>/status`; the two hand-rolled
// reconcile filters it replaced were `<prefix>/+/+/+/config` shapes and
// did not. A broker sends one copy per matching subscription and go-mqtt
// re-matches each copy locally, so while a window is open a birth message
// reaches the birth handler more than once.
//
// That is harmless HERE and the reason is worth writing down rather than
// assuming, because the general case is not harmless — openccu-loom
// measured a doubled physical action from exactly this shape:
//
//   - The sweep's own handler ignores anything that is not a parseable
//     discovery config topic, and `<prefix>/status` is deliberately not
//     one (publisher.ParseConfigTopic does not match it).
//   - The birth handler's effect is `publishDiscovery` per device, which
//     is idempotent: the per-device reconcile is gated against
//     re-entrancy and publisher.Runtime deduplicates a config against
//     what it has already published, so the second run writes nothing.
//   - Neither subscription is in the COMMAND tree, so no command handler
//     can be reached twice. That is the property that actually matters,
//     and TestCommandRoutesAreUnambiguous is where it is asserted.
//
// The test fails if a third subscription is ever added under the
// discovery prefix, which is the point at which the reasoning above has
// to be redone.
func TestTheSweepWindowOverlapsTheBirthSubscriptionHarmlessly(t *testing.T) {
	shortWindow(t)
	b, dev, disc, rec := pinBridge(t)
	if err := b.subscribeCommands(t.Context()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	t.Cleanup(b.stopCommands)
	b.reconcileOrphansOnce(t.Context(), dev.name, map[string]bool{})

	rec.mu.Lock()
	filters := append([]filterQoS(nil), rec.filters...)
	rec.mu.Unlock()

	var underPrefix []string
	for _, f := range filters {
		if strings.HasPrefix(f.Filter, pinPrefix) {
			underPrefix = append(underPrefix, f.Filter)
		}
	}
	if len(underPrefix) != 2 {
		t.Fatalf("%d subscriptions under %s (%v), want exactly 2 — the sweep window and the "+
			"birth topic. A third one needs the overlap reasoning redone.",
			len(underPrefix), pinPrefix, underPrefix)
	}
	if !publisher.MatchFilter(pinPrefix+"/#", disc.BirthTopic()) {
		t.Fatalf("%s no longer matches %s; this pin is stale", pinPrefix+"/#", disc.BirthTopic())
	}
	// The half that makes it harmless: the birth topic is not a config
	// topic, so the sweep's own handler discards it.
	if _, ok := publisher.ParseConfigTopic(pinPrefix, disc.BirthTopic()); ok {
		t.Errorf("%s now parses as a discovery config topic — the sweep would judge Home "+
			"Assistant's own birth topic", disc.BirthTopic())
	}
	// And neither reaches the command tree.
	for _, f := range underPrefix {
		if strings.HasPrefix(f, pinRoot) {
			t.Errorf("%s is under this daemon's own publish root", f)
		}
	}
}

// pinInstance is the `<name>/info` and maintenance publisher over the
// recorder, as cmd/homeconnect2mqtt builds it.
func pinInstance(rec *subRecorder, level *slog.LevelVar) *publisher.Instance {
	return publisher.NewInstance(hagomqtt.Transport(rec), publisher.InstanceConfig{
		Layout:      hass.NewLayout(testLayout(pinRoot)),
		Name:        "go-homeconnect2mqtt",
		Version:     "0.0.0-test",
		SetLogLevel: publisher.LevelVarSetter(level),
		Logger:      slog.New(slog.DiscardHandler),
	})
}

// TestPublishOnlineReplaysStatusAndAnnouncesInfo is the rest of the
// (re)connect hook since 0.15.0: `<name>/info` on every broker connect
// (spec §6), and every status item re-sent with the bytes the broker last
// accepted, original `ts` included (spec §3.2) — a broker that came back
// without its retained store would otherwise hold a static feature's value
// never again.
func TestPublishOnlineReplaysStatusAndAnnouncesInfo(t *testing.T) {
	b, dev, _, rec := pinBridge(t)
	var level slog.LevelVar
	b.instance = pinInstance(rec, &level)
	item := dev.topics.state(dev.app.Entities()[0])
	b.publish(item, "value")
	first, ok := rec.lastPayload(item)
	if !ok {
		t.Fatal("the status item was not published")
	}

	b.PublishOnline(t.Context())

	waitUntil(t, "the status replay", func() bool {
		n := 0
		for _, topic := range rec.publishedTopics() {
			if topic == item {
				n++
			}
		}
		return n == 2
	})
	if got, _ := rec.lastPayload(item); !bytes.Equal(got, first) {
		t.Errorf("the replay sent %q, want the cached %q", got, first)
	}
	info, ok := rec.lastPayload(pinRoot + "/info")
	if !ok {
		t.Fatal("no <name>/info on connect")
	}
	var doc map[string]any
	if err := json.Unmarshal(info, &doc); err != nil {
		t.Fatalf("info: %v", err)
	}
	if doc["name"] != "go-homeconnect2mqtt" || doc["spec"] != "2.0" {
		t.Errorf("info = %s, want name go-homeconnect2mqtt and spec 2.0", info)
	}
}

// TestMaintenanceIsRoutedBesideTheDeviceRoutes: `<name>/maintenance/set/#`
// shares the router — its QoS 1 and its workers — without overlapping a
// device route, and a log level sent there reaches the daemon's real
// slog level.
func TestMaintenanceIsRoutedBesideTheDeviceRoutes(t *testing.T) {
	b, _, _, rec := pinBridge(t)
	var level slog.LevelVar
	b.instance = pinInstance(rec, &level)
	if err := b.subscribeCommands(t.Context()); err != nil {
		t.Fatalf("subscribeCommands: %v", err)
	}
	t.Cleanup(b.stopCommands)

	const filter = pinRoot + "/maintenance/set/#"
	if !slices.Contains(b.commands.Filters(), filter) {
		t.Fatalf("no maintenance route among %v", b.commands.Filters())
	}
	if b.commands.Attributed() {
		t.Error("the maintenance route overlaps a device route")
	}
	rec.mu.Lock()
	handler := rec.handlers[filter]
	rec.mu.Unlock()
	handler(&mqtt.Message{Topic: pinRoot + "/maintenance/set/loglevel", Payload: []byte("debug")})
	b.commands.WaitIdle()
	if level.Level() != slog.LevelDebug {
		t.Errorf("log level = %v after maintenance/set/loglevel debug, want DEBUG", level.Level())
	}
}
