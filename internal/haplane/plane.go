// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package haplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
	hatopic "github.com/SukramJ/go-hamqtt/topic"
)

// ErrDocumentTooLarge is reported by [Plane.PublishBundle] when the device
// document would not fit inside the largest packet the broker said it
// accepts.
//
// It is a refusal BEFORE anything is written, and that is the whole of its
// value. publisher.Runtime.PublishBundle retracts every superseded
// per-entity config first and publishes the document second, so a document
// the broker refuses at the second step has already destroyed the fleet the
// first step cleared: go-mqtt reports mqtt.ErrPacketTooLarge from inside the
// PUBLISH, after the retractions are on the wire, and the appliance is left
// with no discovery config at all. Refusing here leaves the installed fleet
// exactly where it was.
var ErrDocumentTooLarge = errors.New("haplane: device document exceeds the broker's maximum packet size")

// publishOverhead is what a PUBLISH costs beyond its topic and its payload:
// the fixed header byte, the remaining-length varint, the two-byte topic
// length prefix, the packet identifier at QoS > 0 and the MQTT 5.0 property
// block. Sixty-four bytes is generous for all of it by an order of
// magnitude, and generous is the correct direction — being wrong here
// withholds a migration that would have fitted, which is loud and
// recoverable, rather than starting one that cannot finish.
//
// It is also the entire margin between "the preflight says it fits" and
// go-mqtt's mqtt.ErrPacketTooLarge, which is raised from inside the
// PUBLISH with every per-entity config already retracted — so this is the
// one number in this release that must never be REDUCED. Nothing read it
// until TestPublishOverheadCoversARealPublishOnTheWire, which encodes a
// real retained QoS 1 MQTT 5.0 PUBLISH with go-mqtt's own encoder and
// requires [PacketSize] to be at least that large. A floor DERIVED from
// the encoder, not a second copy of this constant: the second copy that
// did exist (a test-local `payloadLen + len(topic) + 64`, which also fed
// the measured "on the wire" figure) is precisely why the first one could
// not be caught being wrong.
const publishOverhead = 64

// Config parameterises a [Plane]. Every field is required; there is no
// usable zero value, because each omission is a silent one.
type Config struct {
	// Prefix is HASS_BASE_TOPIC, the discovery prefix.
	Prefix string

	// StatusTopic is the daemon's own availability topic, <MQTT_TOPIC>/status.
	//
	// It is stated even though Layout renders it, and that IS the
	// assertion: publisher.New fills an empty StatusTopic from the layout
	// and refuses one that disagrees with it. This is the single string
	// every entity's availability list references, and under
	// `availability_mode: all` a typo greys out the whole fleet with
	// nothing on the wire naming the cause.
	StatusTopic string

	// Layout is the same topic.Layout the discovery renderer hands
	// discovery.StdContext.
	Layout hatopic.Layout

	// QoS is MQTT_QOS in the publisher vocabulary; see [QoS]. It governs
	// the discovery publishes, the retractions, the birth and death
	// markers, the Last Will and the orphan sweep's snapshot window.
	QoS publisher.QoS

	// Retain is MQTT_RETAIN, which this daemon applies to the state
	// plane. See [Plane.PublishState] for what it selects between.
	Retain bool

	// BrokerMaxPacketSize reports the largest packet the broker said it
	// would accept, and whether that answer is known at all.
	//
	// It is the broker's OUTBOUND limit — MQTT 5.0's Maximum Packet Size
	// property (0x27) off the CONNACK, reachable as
	// go-mqtt's mqtt.ConnectResult.MaximumPacketSize — and it must not be
	// confused with mqtt.TCPConfig.MaximumPacketSize, which is the largest
	// packet this CLIENT will accept inbound and has a 1 MiB default that
	// says nothing about what the broker will take. go-mtec2mqtt's notes
	// conflated the two; the number that refuses a device document is this
	// one.
	//
	// The second return separates "the broker set no limit" from "nobody has
	// asked yet": a nil hook, a false second return and a zero limit all
	// mean the same thing here — UNKNOWN, which is published, not withheld.
	// Treating an unknown limit as a small one would refuse the migration on
	// every MQTT 3.1.1 link, where there is no property to read at all.
	BrokerMaxPacketSize func() (uint32, bool)

	// Logger receives the library's diagnostics.
	Logger *slog.Logger
}

// Plane is this daemon's go-hamqtt publish runtime.
//
// The discovery half is rebuilt on every (re)connect (see the package
// comment); the state half is not, because everything a
// publisher.StatePublisher remembers is recoverable with
// publisher.StatePublisher.Reset, and its index is also the worklist a
// removed device's retained values are cleared from.
type Plane struct {
	cfg    Config
	newRT  func() *publisher.Runtime
	rt     atomic.Pointer[publisher.Runtime]
	state  *publisher.StatePublisher
	logger *slog.Logger
}

// New builds the plane over tr.
//
// tr is normally a [*Transport] at this point: the runtime's answer to
// publisher.Runtime.Will is needed to build the MQTT client whose adapter
// eventually becomes the target.
func New(tr publisher.Transport, cfg Config) *Plane {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	p := &Plane{
		cfg: cfg,
		newRT: func() *publisher.Runtime {
			return publisher.New(tr, publisher.Config{
				Prefix:      cfg.Prefix,
				StatusTopic: cfg.StatusTopic,
				Layout:      cfg.Layout,
				QoS:         cfg.QoS,
				Logger:      logger,
			})
		},
		logger: logger,
	}
	p.rt.Store(p.newRT())
	p.state = publisher.NewStatePublisher(tr, publisher.StateConfig{
		// Both levels are stated. QoS's zero value is QoSUnset (QoS 1)
		// and PulseQoS's default is QoS 0, so a struct that set only the
		// first would publish a non-retained state at a level the
		// operator never chose.
		QoS:      cfg.QoS,
		PulseQoS: cfg.QoS,
		// RawEncoding, not the library default: this daemon publishes the
		// bare value and no discovery payload carries a value_template.
		// The zero Encoding is EnvelopeEncoding, which would make every
		// state topic unreadable by the configs already on the broker.
		Encoding: discovery.RawEncoding,
		// CommandFilters is deliberately EMPTY, and that is a finding
		// rather than an omission. The guard refuses a state publish whose
		// topic matches one of the consumer's command filters. This
		// daemon's command filter is "<root>/<device>/#" — the feature
		// path is variable-depth, so no fixed-arity filter covers the
		// command tree — and it therefore matches every state topic of the
		// device by construction. Stating it here would refuse all 687
		// state publishes per appliance. The disjointness this daemon
		// actually has is by SUFFIX ("/set" against "/state"), which an
		// MQTT filter cannot express and publisher.MatchFilter cannot see;
		// it is enforced by layout.Device.Relative before dispatch, and by
		// MQTT 5.0 No Local at the broker. See F4, and
		// TestCommandFilterCannotBeStatedToTheStatePlane.
		Logger: logger,
	})
	return p
}

// Runtime is the discovery plane for the CURRENT broker connection.
//
// Never store the result across a call that can block on the broker: a
// reconnect swaps it underneath, and acting on the old one is the defect
// [Plane.Reconnect] exists to remove.
func (p *Plane) Runtime() *publisher.Runtime { return p.rt.Load() }

// Will is the Last Will this plane's availability policy assumes. Every
// field is copied to the MQTT client verbatim; a literal at that call site
// is what lets the two halves drift.
func (p *Plane) Will() (publisher.Will, error) { return p.Runtime().Will() }

// StatusTopic is the daemon's own availability topic.
func (p *Plane) StatusTopic() string { return p.Runtime().BridgeTopic() }

// Reconnect installs a fresh discovery runtime and opens the state
// plane's dedup gate. Call it at the head of every (re)connect, before
// anything is published on the new connection.
//
// Both halves answer the same question — what does the BROKER hold? — and
// both used to be answered from process memory:
//
//   - The discovery runtime's superseded/declared/announced maps are
//     statements about a broker. A QoS 0 publish "succeeds" when the bytes
//     reach a socket; if that socket then died, the broker applied none of
//     them. Keeping the maps across the reconnect is how a retry skips a
//     retraction the broker never applied.
//   - The state plane's dedup cache is the same statement. A broker that
//     came back without a persistent retained store holds nothing, while
//     the cache still answers "already published" — and every entity sits
//     blank until its datapoint next changes, which for a static feature
//     is never. Reset opens the gate without forgetting the index.
func (p *Plane) Reconnect() {
	if old := p.rt.Swap(p.newRT()); old != nil {
		old.Close()
	}
	p.state.Reset()
}

// AnnounceOnline publishes the retained birth marker on the status topic.
func (p *Plane) AnnounceOnline(ctx context.Context) error { return p.Runtime().AnnounceOnline(ctx) }

// AnnounceOffline publishes the retained death marker — the counterpart a
// graceful DISCONNECT suppresses the Last Will for.
func (p *Plane) AnnounceOffline(ctx context.Context) error { return p.Runtime().AnnounceOffline(ctx) }

// Publish writes one retained discovery config and reports whether it
// reached the broker. It is the method internal/hass publishes through.
func (p *Plane) Publish(ctx context.Context, topic string, payload []byte) (bool, error) {
	return p.Runtime().Publish(ctx, topic, payload)
}

// PublishBundle writes one appliance's retained device document, having
// first refused it if the broker said it would not accept a packet that
// size.
//
// The preflight is the whole reason this method exists rather than a direct
// call on the runtime. publisher.Runtime.PublishBundle retracts every
// per-entity config the document supersedes BEFORE it publishes the
// document — the order Home Assistant forces, because a retained per-entity
// config and a document carrying the same unique id cannot coexist — so the
// two publishes are one migration with a window in between where the
// appliance has no discovery config at all. A document refused at the
// second step is that window made permanent. go-mqtt reports
// mqtt.ErrPacketTooLarge from inside the PUBLISH, with the retractions
// already gone, and no error anywhere says that 687 entities were deleted
// rather than moved.
//
// So: measure first, and fail CLOSED. An unknown limit is published (see
// [Config.BrokerMaxPacketSize]); a known limit the document does not fit
// under withholds the whole migration and leaves the installed fleet
// standing.
//
// The bool is publisher.Runtime.PublishBundle's: false with a nil error
// means the document was already declared on this connection, byte for
// byte, and nothing was written — which still means the broker holds it.
func (p *Plane) PublishBundle(ctx context.Context, b *discovery.Bundle) (bool, error) {
	if b == nil {
		return false, errors.New("haplane: nil device document")
	}
	if err := p.preflight(b); err != nil {
		return false, err
	}
	return p.Runtime().PublishBundle(ctx, b)
}

// preflight measures the document against the broker's advertised Maximum
// Packet Size. See [ErrDocumentTooLarge].
func (p *Plane) preflight(b *discovery.Bundle) error {
	if p.cfg.BrokerMaxPacketSize == nil {
		return nil
	}
	limit, known := p.cfg.BrokerMaxPacketSize()
	// Three spellings of the same answer, and none of them is "small":
	// no hook, no connection result yet, and a broker that named no limit.
	if !known || limit == 0 {
		return nil
	}
	payload, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("haplane: marshal device document %s: %w", b.NodeID, err)
	}
	topic := publisher.BundleConfigTopic(p.cfg.Prefix, b.NodeID)
	size := PacketSize(topic, len(payload))
	if size <= uint64(limit) {
		return nil
	}
	return fmt.Errorf("%w: %s is %d bytes against the broker's %d",
		ErrDocumentTooLarge, topic, size, limit)
}

// PacketSize is what one retained PUBLISH of payloadLen bytes to topic
// costs on the wire, overhead included.
//
// Exported so the refusal can be asserted against the same arithmetic that
// performs it rather than against a second copy of it, which is how the two
// end up disagreeing about whether a document fits.
func PacketSize(topic string, payloadLen int) uint64 {
	return uint64(payloadLen) + uint64(len(topic)) + publishOverhead //nolint:gosec // both lengths are non-negative
}

// LegacyForms names the per-entity config topic shapes
// publisher.Runtime.PublishBundle retracts under.
//
// Exported so a test can assert what the composition root actually stated,
// because [Config] cannot state it: publisher.Config.LegacyEntityTopics is
// deliberately left nil here, which means the five-segment
// publisher.LegacyTopicWithNodeID form alone — measured against this
// daemon's fleet at 687 of 687 (ADR 0070 phase 7, step 4). Naming any form
// REPLACES that default rather than extending it, so a well-meant addition
// would silently stop retracting the shape this bridge is actually on, and
// the failure looks entirely clean: the document publishes, nothing logs,
// and Home Assistant refuses every entity with one WARNING line.
func (p *Plane) LegacyForms() []string { return p.Runtime().LegacyForms() }

// Retract clears retained discovery configs this daemon no longer
// publishes.
func (p *Plane) Retract(ctx context.Context, topics ...string) error {
	return p.Runtime().Retract(ctx, topics...)
}

// Declared is every discovery config topic this process currently claims.
func (p *Plane) Declared() []string { return p.Runtime().Declared() }

// PublishState writes one entity-state, availability or connection-state
// payload, honouring MQTT_RETAIN.
//
// The branch is the whole reason this method exists rather than a direct
// call. publisher.StatePublisher.Publish is unconditionally retained and
// publisher.StatePublisher.Pulse unconditionally not, so neither alone can
// express an operator flag that selects between them — and picking the
// first would silently retain a fleet that an operator deliberately runs
// non-retained. The four combinations reproduce exactly what this daemon
// put on the wire before the plane moved:
//
//	retain + payload   -> Publish  (retained, dedup-gated)
//	retain + empty     -> Evict    (retained, empty: the retraction the
//	                                old code performed by accident)
//	no retain + any    -> Pulse    (not retained, not deduplicated)
//
// An empty payload is reachable — payloadFor renders a nil value and a
// value it cannot marshal as no bytes at all — and publisher rejects it
// with ErrEmptyStatePayload rather than quietly retracting, which is why
// saying so is this method's job and not the caller's.
func (p *Plane) PublishState(ctx context.Context, topic string, payload []byte) error {
	if !p.cfg.Retain {
		return p.state.Pulse(ctx, topic, payload)
	}
	if len(payload) == 0 {
		return p.state.Evict(ctx, topic)
	}
	_, err := p.state.Publish(ctx, topic, payload)
	return err
}

// Close finishes with the current discovery runtime.
//
// What it actually does is drain publisher.Runtime's birth-replay worker,
// and that worker exists only once publisher.Runtime.WatchBirth has been
// called. This daemon watches Home Assistant's birth topic itself, so the
// call is inert here — it is made because the runtime is the library's to
// finish with and the day this daemon adopts WatchBirth it must already be
// wired, not because it closes the shutdown window. The window is closed by
// the consumer, before the final offline marker: see
// internal/bridge's Bridge.StopDiscovery.
func (p *Plane) Close() { p.Runtime().Close() }
