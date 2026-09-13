// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
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

// TestStatePublishesCarryMQTTQoSAndMQTTRetain drives the real state plane
// and reads both flags off the transport.
//
// The state plane is where entity values, device availability and
// connection_state go, and it is the plane an operator's MQTT_RETAIN
// governs. publisher.StatePublisher.Publish is unconditionally retained
// and Pulse unconditionally not, so a plane that reached for either alone
// would silently change one of the two shipped configurations.
func TestStatePublishesCarryMQTTQoSAndMQTTRetain(t *testing.T) {
	for _, mqttQoS := range []int{0, 1} {
		for _, retain := range []bool{true, false} {
			b, dev, _, rec := pinBridgeQoS(t, mqttQoS)
			b.cfg.MQTTRetain = &retain
			// Rebuild the plane with the retain flag under test; pinBridgeQoS
			// builds it from testCfg's default.
			b.plane = planeFor(t, rec, mqttQoS, retain)

			b.publish(dev.topics.Availability(), []byte(availOnline))
			b.publish(dev.topics.ConnectionState(), []byte("connected"))
			e := dev.app.Entities()[0]
			b.publish(dev.topics.state(e), []byte("value"))

			rec.mu.Lock()
			pubs := append([]pubCall(nil), rec.pubs...)
			rec.mu.Unlock()
			if len(pubs) != 3 {
				t.Fatalf("MQTT_QOS %d retain %v: %d publishes, want 3", mqttQoS, retain, len(pubs))
			}
			for _, p := range pubs {
				if int(p.qos) != mqttQoS {
					t.Errorf("MQTT_QOS %d: %s reached the transport at QoS %v", mqttQoS, p.topic, p.qos)
				}
				if p.retain != retain {
					t.Errorf("MQTT_RETAIN %v: %s reached the transport with retain=%v", retain, p.topic, p.retain)
				}
			}
		}
	}
}

// TestCommandFilterCannotBeStatedToTheStatePlane is a finding written as
// an assertion, and the reason this daemon uses neither
// publisher.CommandRouter.CheckDisjoint nor
// publisher.StateConfig.CommandFilters.
//
// Both guards decide by matching a topic against a command FILTER. This
// daemon's command filter is the whole device sub-tree — the feature path
// is variable-depth, so no fixed-arity filter covers the command tree —
// and it therefore matches every state topic of the device by
// construction. Stating it to the state plane would refuse all 687 state
// publishes per appliance with ErrStateCommandCollision, and
// CheckDisjoint would fail the boot.
//
// The disjointness this daemon actually has is by SUFFIX: a command topic
// ends in "/set" and a state topic in "/state". MQTT filters cannot
// express that, which is why layout.Device.Relative is the guard and
// MQTT 5.0 No Local is the second lock.
func TestCommandFilterCannotBeStatedToTheStatePlane(t *testing.T) {
	t.Parallel()
	_, dev, _, _ := pinBridge(t)
	filter := dev.topics.CommandFilter()

	states := realStateTopics(dev)
	matched := 0
	for _, st := range states {
		if publisher.MatchFilter(filter, st) {
			matched++
		}
		// The suffix rule, which is the one that is actually right.
		if _, ok := dev.topics.Relative(st); ok {
			t.Errorf("%s is a state topic and Relative accepted it as a command", st)
		}
	}
	if matched != len(states) {
		t.Fatalf("the command filter matched %d of %d state topics; if it now matches none, "+
			"publisher.StateConfig.CommandFilters and CommandRouter.CheckDisjoint have become "+
			"usable here and this daemon should state them", matched, len(states))
	}
	// And the converse: every advertised command topic is claimed.
	_, commands := advertised(t, dev)
	for cfgTopic, ct := range commands {
		if !publisher.MatchFilter(filter, ct) {
			t.Errorf("%s advertises %s, which %s does not match", cfgTopic, ct, filter)
		}
		if _, ok := dev.topics.Relative(ct); !ok {
			t.Errorf("%s advertises %s, which Relative does not accept as a command", cfgTopic, ct)
		}
	}
	t.Logf("F4: %s matches all %d state topics and all %d command topics; only the "+
		"/set suffix separates them, and no MQTT filter can say so",
		filter, matched, len(commands))
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
	// And the daemon's own state tree still arrives on the route — that is
	// F4, unchanged and unfixable at the filter — so the guard before
	// dispatch is what must stop it.
	own := realStateTopics(dev)[0]
	if !b.commands.Claims(own) {
		t.Fatalf("%s is not claimed by the route; the filter narrowed and this test is stale", own)
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
	handler(&mqtt.Message{Topic: dev.topics.Base() + "/No/Such/Feature/set", Payload: []byte("1")})
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
		dev.topics.Availability(),
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

// TestPerDeviceRoutesCannotOverlap: the routes are "<root>/<name>/#" per
// configured device and internal/profile refuses a duplicate device name,
// so any two differ in a literal level. This asserts it over the shape
// rather than over one fixture's names, and asserts the router refuses a
// pair no specificity rule can order, which is the case attribution does
// NOT rescue.
func TestPerDeviceRoutesCannotOverlap(t *testing.T) {
	t.Parallel()
	router := publisher.NewCommandRouter(hagomqtt.Transport(&subRecorder{}),
		publisher.CommandConfig{Logger: slog.New(slog.DiscardHandler)})
	noop := func(context.Context, publisher.Command) {}
	for _, name := range []string{"Geschirrspüler", "Kühlschrank", "Backofen"} {
		f := layout.NewDevice(pinRoot, name).CommandFilter()
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
		will, err := planeFor(t, rec, mqttQoS, true).Will()
		if err != nil {
			t.Fatalf("will: %v", err)
		}
		if int(will.QoS) != mqttQoS {
			t.Errorf("MQTT_QOS %d: will qos = %d, and topics.json claims qos=MQTT_QOS", mqttQoS, will.QoS)
		}
		if !will.Retain {
			t.Error("the will is not retained, and topics.json claims retain=true")
		}
		if will.Topic != layout.Bridge(pinRoot) {
			t.Errorf("will topic = %q, want %q", will.Topic, layout.Bridge(pinRoot))
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
// publishing state nobody displays.
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
	for _, p := range pubs {
		if p.topic == layout.Bridge(pinRoot) && p.retain && p.qos == pinQoS {
			found = true
		}
	}
	if !found {
		t.Errorf("no retained online marker on %s at MQTT_QOS: %v", layout.Bridge(pinRoot), pubs)
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
