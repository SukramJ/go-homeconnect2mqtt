// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"sync"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
)

// priorDocuments is what the retained device documents declared BEFORE
// anything this connection published, keyed by the document's node id.
//
// It is the only thing that can tell a component this daemon REMOVED from
// one it never had, and nothing else in this process knows it: the
// catalogue is compiled in, nothing is persisted, and the orphan sweep runs
// after the publish, so its snapshot holds the document this boot just
// wrote. See internal/hass/tombstone.go for what a tombstone is and why
// every failure direction of this read produces fewer of them.
//
// # Where this state lives, and why that is the whole design
//
// It is keyed on the publisher.Runtime it was read through. That runtime is
// rebuilt by haplane.Plane.Reconnect at the head of every (re)connect, so a
// memo taken on one connection is self-invalidating on the next — there is
// no flag for a caller to forget to clear and no window in which a
// reconnect and a load race over one. That matters twice here:
//
//   - A statement about what the BROKER holds may not outlive the
//     connection it was made on. A broker that came back without its
//     retained store holds nothing, and a cached previous document would
//     then tombstone components against a document that no longer exists —
//     harmless in Home Assistant, but it would also mean the daemon never
//     re-read the truth.
//   - The same rebuild is what makes publisher.Runtime's own `superseded`
//     memo correct per connection. A read-back cached across connections
//     would undo exactly that property on the artefact beside it.
type priorDocuments struct {
	mu sync.Mutex
	// rt is the connection docs describes. A nil rt means nothing has been
	// read on this connection yet.
	rt   *publisher.Runtime
	docs map[string]map[string]discovery.Component
}

// forConnection reports the previous components of one device document,
// reading every
// retained device document back from the broker the first time it is asked
// on a given connection.
//
// The lock is held across the read, which serialises the first
// publishDiscovery of every appliance behind one window. That is required
// rather than incidental: the window is ONE fleet-wide snapshot, and an
// appliance that published its document before it opened would have
// overwritten the very document the window exists to read.
func (p *priorDocuments) forConnection(
	ctx context.Context,
	rt *publisher.Runtime,
	nodeID string,
	read func(context.Context) map[string]map[string]discovery.Component,
) map[string]discovery.Component {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rt != rt {
		// Recorded even when the read failed or saw nothing: one attempt
		// per connection. A retry loop in front of the migration would
		// spend a fleet-wide snapshot window per appliance to learn the
		// same thing.
		p.docs = read(ctx)
		p.rt = rt
	}
	return p.docs[nodeID]
}

// record remembers what a document that WENT OUT declares, so a second pass
// on the same connection does not re-tombstone what the first one removed.
//
// rt is the runtime the document was published through, and it is compared
// rather than trusted: a pass that started before a reconnect finishes
// against the dead connection's runtime, and its result describes a broker
// state the new connection never had.
func (p *priorDocuments) record(rt *publisher.Runtime, nodeID string, live map[string]discovery.Component) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rt != rt {
		return
	}
	if p.docs == nil {
		p.docs = map[string]map[string]discovery.Component{}
	}
	if len(live) == 0 {
		delete(p.docs, nodeID)
		return
	}
	p.docs[nodeID] = live
}

// readPriorDocuments opens one narrow snapshot window over the device
// documents and returns each one's component set, keyed by node id.
//
// # Why this is not publisher.Runtime.Sweep
//
// go-daikin2mqtt reads its prior documents through a ReportOnly sweep, and
// that transfers only halfway. Sweep's filter is hard-wired to
// `<prefix>/#`, which on this bridge replays all 687 retained per-entity
// configs and the ~473 KB document itself — on the one path this release
// cannot undo — and it is the SAME filter the post-publish orphan sweep
// uses, so two windows become indistinguishable to the pin that asserts the
// sweep did not run. [haplane.Plane.Snapshot] over
// `<prefix>/device/+/config` replays one message per appliance and is
// visible as its own subscription.
//
// # The failure directions
//
// A window that opens and sees nothing, a broker that never delivers, a
// context that expires, a document that does not parse, and a document
// whose components are not provably this instance's all produce the same
// thing: no prior state, hence no tombstones, hence exactly the behaviour
// this daemon had before tombstones existed. There is no path here that
// produces a DIFFERENT tombstone, only fewer.
//
// The direction that would be destructive is a document written by a
// SIBLING INSTANCE, and it is reachable: two instances with different
// MQTT_TOPIC roots, the same HASS_BASE_TOPIC and an appliance name in
// common address the same document topic, because MQTT_TOPIC appears in
// neither the prefix nor the node id (F8). Nothing in the topic can tell
// them apart. [hass.Discovery.BundleComponents] therefore judges every
// component by the payload rule the orphan sweep already uses, and a
// component that is not provably ours never becomes prior state.
//
// "Provably" is the load-bearing word, and it is conditional rather than
// absolute. The payload rule attributes on an EXACT match against a topic
// only this instance renders; when it was a topic-prefix test, an instance
// rooted at `homeconnect` accepted every component of one rooted at
// `homeconnect/kitchen` as its own and tombstoned the live ones out of
// Home Assistant. Two instances that share one MQTT_TOPIC root are not
// separable by this rule or any other, and the answer there is
// configuration, not code.
func (b *Bridge) readPriorDocuments(ctx context.Context) map[string]map[string]discovery.Component {
	out := map[string]map[string]discovery.Component{}
	var mu sync.Mutex
	filter := b.hass.BundleFilter()
	// What this window is waiting for, so it can stop waiting. The
	// read-back sits in front of the migration, so every millisecond it
	// spends is a millisecond the appliance's discovery config is older
	// than it needs to be; a steady-state boot has all of its documents
	// within the first round trip and has no reason to hold the
	// subscription open for the rest of the window.
	//
	// Counted over the CONFIGURED devices rather than over what arrives:
	// an installation whose broker holds no document yet has nothing to
	// complete, and runs the window out, which is the only answer MQTT
	// offers — there is no end-of-retained signal.
	want := make(map[string]bool, len(b.devices))
	for _, d := range b.devices {
		want[b.hass.BundleNodeID(d.name)] = true
	}
	err := b.plane.Snapshot(ctx, filter, reconcileCollectWindow.Get(), func(topic string, payload []byte) bool {
		// Runs on the transport's read loop: cheap, and it publishes
		// nothing.
		node, ok := b.hass.BundleNodeIDOf(topic)
		if !ok {
			return false
		}
		comps := b.hass.BundleComponents(payload)
		if len(comps) == 0 {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		out[node] = comps
		for node := range want {
			if _, have := out[node]; !have {
				return false
			}
		}
		return true
	})
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		// Logged, never returned: whatever the window DID collect is still
		// usable, and what it missed simply is not tombstoned.
		b.logger.Warn("bridge.discovery_prior_read",
			slog.String("filter", filter), slog.Int("documents", len(out)),
			slog.String("err", err.Error()),
			slog.String("consequence",
				"a component this daemon has stopped publishing stays in Home Assistant as a "+
					"phantom entity until a later connection reads the document successfully"))
		return out
	}
	b.logger.Debug("bridge.discovery_prior_read",
		slog.String("filter", filter), slog.Int("documents", len(out)))
	return out
}
