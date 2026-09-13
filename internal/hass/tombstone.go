// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"encoding/json"
	"sort"

	"github.com/SukramJ/go-hamqtt/discovery"
)

// This file is the half of the device-document migration ADR 0070 phase 7
// step 6 deferred: a component this daemon stops publishing is REMOVED from
// Home Assistant instead of being left behind.
//
// A device document does not delete a component by omitting it. Home
// Assistant removes one when its entry is present and carries a platform
// and nothing else, so "what did the previous document declare that this
// one does not" is a question that has to be answered before the answer is
// overwritten — and this daemon has no memory of it. The catalogue is
// compiled in, nothing is persisted, and the orphan sweep runs AFTER the
// publish, so its snapshot holds the document this boot just wrote.
//
// The only restart-surviving source is the broker, so the previous document
// is read back from it once per connection, immediately before the first
// publish of that connection. That puts a broker interaction in front of
// the one publish of this release that cannot be undone, and the reason it
// is acceptable is a property of the code below rather than a hope:
//
//	Every failure direction produces FEWER tombstones, never different
//	ones.
//
// A window that sees nothing, a document that does not parse, a component
// that carries no platform, a component whose identity or topics are not
// this instance's, and a key the new document still declares all simply do
// not become a tombstone — and the publish proceeds exactly as it would
// have without the read-back. The failure mode of the read-back is the
// behaviour of not having it, which is the only failure mode that may be
// added to this path.
//
// The direction that would be destructive rather than merely absent is a
// retained document written by a SIBLING INSTANCE. It is reachable here:
// two instances with different MQTT_TOPIC roots, the same HASS_BASE_TOPIC
// and an appliance name in common address the SAME document topic, because
// MQTT_TOPIC appears in neither the discovery prefix nor the node id (F8).
// [Discovery.BundleComponents] therefore judges every component by
// [Discovery.IsOwnConfig] — the payload rule, the only one that can tell
// the two apart — and a component that is not provably ours is not prior
// state, so it can never be tombstoned.
//
// That promise is CONDITIONAL on attributability, and stating it
// unconditionally is how it went wrong once already. [Discovery.IsOwnConfig]
// attributes on an exact match against a topic only this instance renders;
// while it was a topic PREFIX test instead, an instance rooted at
// `homeconnect` claimed all 687 components of one rooted at
// `homeconnect/kitchen` and tombstoned the 510 that were live. Two
// instances sharing one MQTT_TOPIC root remain indistinguishable — nothing
// in the topic tree or in the payload separates them — and that is a
// configuration this daemon cannot make safe, only document.

// BundleComponents reads a retained device document and returns the
// components it declared that are provably THIS instance's.
//
// It returns nil for anything that is not a well-formed document with at
// least one such component, and that direction is deliberate: the caller
// turns this into tombstones, and a tombstone for a component that is
// actually alive deletes a working entity. A read that cannot be trusted
// must produce fewer tombstones, never different ones.
//
// Three filters, and none of them is redundant:
//
//   - A component with no platform is skipped. It is either noise or a
//     TOMBSTONE this daemon itself wrote on an earlier connection, and
//     carrying a tombstone forward as prior state would re-mark a key that
//     is already gone in every document this daemon ever publishes again.
//   - A component that [Discovery.IsOwnConfig] does not claim is skipped:
//     a sibling instance's, or anything else that ended up under this
//     topic. That predicate is the same one the orphan sweep uses on a
//     per-entity config, and it reads the same keys — a document's
//     components carry `unique_id`, `state_topic`/`command_topic` and the
//     two-source `availability` list exactly as a per-entity config does,
//     because a component IS that payload minus the `platform` its topic
//     used to carry.
//   - A document whose components all fail those tests yields nil rather
//     than an empty map, so a caller cannot tell "read nothing" from "read
//     a document that proved nothing" — because it must treat them the
//     same.
func (d *Discovery) BundleComponents(payload []byte) map[string]discovery.Component {
	var doc struct {
		Components map[string]json.RawMessage `json:"components"`
	}
	if json.Unmarshal(payload, &doc) != nil || len(doc.Components) == 0 {
		return nil
	}
	out := make(map[string]discovery.Component, len(doc.Components))
	for key, raw := range doc.Components {
		var comp discovery.Component
		if json.Unmarshal(raw, &comp) != nil {
			continue
		}
		if comp.Platform == "" {
			continue
		}
		if !d.IsOwnConfig(raw) {
			continue
		}
		out[key] = comp
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LiveComponents is the component set a published document actually
// declares: everything that is not a tombstone.
//
// It is what a caller remembers as the prior state of the document it just
// wrote, and the filter is the same one [Discovery.BundleComponents]
// applies to a document read back from the broker — a tombstone is not
// prior state, it is a component that is already gone.
func LiveComponents(b *discovery.Bundle) map[string]discovery.Component {
	if b == nil {
		return nil
	}
	out := make(map[string]discovery.Component, len(b.Components))
	for _, key := range b.Keys() {
		if comp := b.Components[key]; comp.Platform != "" && comp.UniqueID != "" {
			out[key] = comp
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ApplyTombstones marks every component prior declared that b does not, and
// returns the keys it marked, sorted.
//
// The subtraction is against b.Components, which is what keeps a LIVE
// component from ever being tombstoned: a key the new document declares is
// never in the gone set, so discovery.Bundle.RemoveComponents is never
// handed one and can never overwrite a rendered entry with a platform-only
// one. That is asserted rather than stated —
// TestATombstoneNeverOverwritesALiveComponent.
//
// The removal is discovery.Bundle.RemoveComponents rather than
// discovery.Bundle.Remove because the case that occurs here is the one the
// document never held: an entity the curated filter dropped, or one that
// left the description file, is not rendered at all, so there is no entry
// for Remove to remember. RemoveComponents takes the previous document's
// components as the memory instead, which is also where the removed
// entity's `unique_id` comes from — it goes into discovery.Bundle.Tombstones
// (`json:"-"`), OUTSIDE the payload, because an entry carrying a `unique_id`
// is not a removal at all.
func ApplyTombstones(b *discovery.Bundle, prior map[string]discovery.Component) []string {
	if b == nil || len(prior) == 0 {
		return nil
	}
	gone := make([]string, 0, len(prior))
	for key := range prior {
		if _, live := b.Components[key]; live {
			continue
		}
		gone = append(gone, key)
	}
	if len(gone) == 0 {
		return nil
	}
	sort.Strings(gone)
	b.RemoveComponents(prior, gone...)
	return gone
}
