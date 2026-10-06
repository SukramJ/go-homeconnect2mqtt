// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	hatopic "github.com/SukramJ/go-hamqtt/topic"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
)

// legacySweepWindow is how long the migration sweep listens for retained
// messages before it decides. MQTT has no end-of-retained signal, so the
// window always runs out; the broker delivers a retained tree right after
// the SUBSCRIBE, so a few seconds is generous. Shortenable for the pins.
var legacySweepWindow = newTunable(3 * time.Second)

// The migration sweep (openccu-loom ADR 0083, "Migration").
//
// 0.15.0 moved every topic this daemon publishes, and a retained message
// stays on the broker until somebody clears it: without this, every
// installation would carry its whole pre-0.15.0 tree — one retained value
// per feature per appliance, the availability markers, the old status
// topic with its last "offline" — forever, and every tool that browses the
// broker would show two trees for one daemon.
//
// So on every start the daemon reads back what is retained and clears what
// the OLD layout left. It is idempotent (a second run finds nothing), runs
// for the life of the 0.x line that introduced it, and also cleans after a
// rollback and re-upgrade.
//
// # What it may clear, and the rules that keep it from clearing more
//
// The hazard is a sibling's live tree, and it is not hypothetical: ADR 0070's
// review of this very bridge measured a prefix rule deleting 510 live
// components of a sibling instance. So:
//
//  1. Exact shapes only, never a prefix. A topic is cleared when it is one
//     this instance's OLD layout rendered for a device name it is configured
//     with — `<root>/<name>/<Feature/Path>/state` for one of that
//     appliance's features, `<root>/<name>/availability`,
//     `<root>/<name>/connection_state` — or the bare `<root>/status`, which
//     the new layout never publishes (its two levels are a function prefix,
//     not a topic). A device name this instance does not know is not
//     touched, nor is a feature path its appliance does not have.
//  2. A topic whose second level is a function name is NEW and never
//     cleared as an old one. The old device segment is an operator-chosen
//     name with no guard against spelling `status`, `set`, `info` or `meta`;
//     such a device's old topics are left retained — stale, never wrongly
//     deleted — and the README says so.
//  3. The windows are narrow on purpose. One `<root>/#` would also match the
//     command tree, and a broker sends one copy per matching subscription:
//     a command arriving while the window is open would reach its handler
//     twice, which openccu-loom measured as a doubled physical action. The
//     filters are `<root>/<name>/#` per configured device whose name passes
//     rule 2, `<root>/status` exactly, and `<name>/status/<haId>/#` — none
//     of which can match `<name>/set/…`.
//
// The last filter is mqtt-smarthome 2.0 §3.2's steady-state rule, served by
// the same read-back: an item under a configured appliance's status tree
// that its current feature set does not contain (a description re-parsed
// with fewer features) is cleared. An appliance that is no longer
// configured is not touched — its haId is not this instance's to judge.
func (b *Bridge) sweepLegacy(ctx context.Context) int {
	plan := b.legacySweepPlan()
	var (
		mu    sync.Mutex
		stale = map[string]bool{}
	)
	err := b.plane.SnapshotRetained(ctx, plan.filters, legacySweepWindow.Get(), func(topic string, _ []byte) bool {
		// Runs on the transport's read loop: cheap, and it publishes
		// nothing.
		if plan.clears(topic) {
			mu.Lock()
			stale[topic] = true
			mu.Unlock()
		}
		return false
	})
	if err != nil && ctx.Err() == nil {
		// Logged, never fatal: whatever the window DID collect is still
		// exactly what it is, and the next start runs the sweep again.
		b.logger.Warn("bridge.migration_sweep", slog.String("err", err.Error()))
	}
	mu.Lock()
	topics := make([]string, 0, len(stale))
	for t := range stale {
		topics = append(topics, t)
	}
	mu.Unlock()
	if len(topics) == 0 {
		return 0
	}
	sort.Strings(topics)
	// On a context of its own, bounded: the clears are the sweep's whole
	// point, and a shutdown that cancelled Run halfway through would
	// otherwise leave the rest for the next start for no reason.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bundlePublishTimeout)
	defer cancel()
	if err := b.plane.Evict(cctx, topics...); err != nil {
		b.logger.Warn("bridge.migration_sweep_clear", slog.String("err", err.Error()))
	}
	b.logger.Info("bridge.migration_sweep",
		slog.Int("cleared", len(topics)),
		slog.String("note", "retained topics of the pre-0.15.0 layout and status items no appliance has any more"))
	return len(topics)
}

// sweepPlan is what one sweep subscribes to and what it may clear.
type sweepPlan struct {
	filters []string
	// legacy is every exact old-layout topic this instance may clear.
	legacy map[string]bool
	// current maps a configured appliance's status prefix,
	// `<name>/status/<haId>/`, to the items it currently has.
	current map[string]map[string]bool
}

// clears reports whether a retained topic is one the sweep removes.
func (p sweepPlan) clears(topic string) bool {
	if p.legacy[topic] {
		return true
	}
	for prefix, items := range p.current {
		if strings.HasPrefix(topic, prefix) {
			return !items[topic]
		}
	}
	return false
}

// legacySweepPlan builds the plan from what this instance is configured
// with: its old root (MQTT_TOPIC — this bridge's old root and new name are
// the same key with the same default) and its devices.
func (b *Bridge) legacySweepPlan() sweepPlan {
	root := b.cfg.MQTTTopic
	plan := sweepPlan{legacy: map[string]bool{}, current: map[string]map[string]bool{}}

	bare := layout.LegacyBridge(root)
	plan.filters = append(plan.filters, bare)
	plan.legacy[bare] = true

	for _, d := range b.devices {
		old := layout.NewLegacyDevice(root, d.name)
		if second, _, _ := strings.Cut(d.name, "/"); hatopic.IsFunction(second) {
			// Rule 2: this device's old tree shares its second level with
			// the new grammar, so nothing in it can be told from a new
			// topic by its shape. Left alone, said once.
			b.logger.Warn("bridge.migration_sweep_skipped",
				slog.String("device", d.name),
				slog.String("reason", "the device name is an mqtt-smarthome function name; "+
					"its pre-0.15.0 topics under "+old.Base()+"/ are left retained and must be cleared by hand"))
		} else {
			plan.filters = append(plan.filters, old.Base()+"/#")
			plan.legacy[old.Availability()] = true
			plan.legacy[old.ConnectionState()] = true
			for _, e := range d.app.Entities() {
				plan.legacy[old.State(e.Name(), e.UID())] = true
			}
		}

		items := map[string]bool{
			d.topics.Online():          true,
			d.topics.ConnectionState(): true,
		}
		for _, e := range d.app.Entities() {
			items[d.topics.state(e)] = true
		}
		plan.filters = append(plan.filters, d.topics.StatusPrefix()+"#")
		plan.current[d.topics.StatusPrefix()] = items
	}
	return plan
}
