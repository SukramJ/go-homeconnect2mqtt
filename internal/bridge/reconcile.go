// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// reconcileCollectWindow is how long a snapshot subscription listens
// before it decides what is an orphan; the broker delivers the retained
// discovery tree right after the subscribe.
//
// A var rather than a const so the pins can shorten it. A sweep test that
// waits two seconds for a window it controls is a test that will one day
// be deleted for being slow.
var reconcileCollectWindow = 2 * time.Second

// refreshSettleDelay is how long we wait after clearing all discovery configs so
// Home Assistant removes the entities before the workers re-publish them.
var refreshSettleDelay = 3 * time.Second

// The orphan sweep, and why it reports rather than retracts.
//
// publisher.Runtime.Sweep can do both: an ordinary pass judges a retained
// config on SweepRequest.Owns alone and clears what it owns and this
// process does not claim. Owns sees the parsed TOPIC and nothing else,
// which for this daemon is the weaker of the two ownership rules it has
// always had. The stronger one reads the retained PAYLOAD: a config is
// ours when its unique_id is in the `homeconnect_` namespace AND its
// state topic sits under this daemon's own MQTT root.
//
// That difference is exactly finding F8. Two instances of this daemon on
// one broker with different MQTT_TOPIC roots, the same HASS_BASE_TOPIC
// and any appliance name in common publish the SAME config topics, the
// same node id and the same unique_ids — MQTT_TOPIC appears in none of
// them. No predicate that sees only a topic can tell the two apart, so a
// retracting pass would clear the sibling's fleet, and the sibling has no
// reason to republish it. The state topic in the payload is the one place
// the two differ, and Inspect is where it can be read.
//
// So: ReportOnly, then the caller's own payload check, then an explicit
// publisher.Runtime.Retract of the list the caller chose. The warning is
// not hypothetical — openccu-loom's retraction prefixes turned out to own
// 100 % of a sibling daemon's configs while claiming none, and
// go-mtec2mqtt's reviewer proved a staggered two-instance upgrade deletes
// the sibling's whole fleet (its PR #54, finding F4).

// refreshDiscoveryOnce, when HASS_DISCOVERY_REFRESH is set, clears every retained
// discovery config this daemon owns and waits, so the per-device workers then
// re-create the entities from scratch. This is the only way to push changes Home
// Assistant caches at first registration (entity_category, name). It is a
// one-shot, operator-triggered migration — turn the flag off after one run.
//
// It runs before any worker has published, which is the one moment a
// fleet-wide scope is safe: nothing is claimed yet, so "owned and
// unclaimed" is "owned", which is precisely what this flag means to clear.
func (b *Bridge) refreshDiscoveryOnce(ctx context.Context) {
	if !b.cfg.HASSDiscoveryRefresh || b.hass == nil || b.plane == nil {
		return
	}
	names := make([]string, 0, len(b.devices))
	for _, d := range b.devices {
		names = append(names, d.name)
	}
	orphans, res, err := b.collectOwnConfigs(ctx, names...)
	if err != nil {
		b.logger.Warn("bridge.refresh_sweep", slog.String("err", err.Error()))
	}
	cleared := b.retract(ctx, orphans)
	b.logger.Info("bridge.discovery_refresh",
		slog.Int("inspected", res.Inspected), slog.Int("cleared", cleared))
	// Let HA drop the entities before the workers re-publish fresh configs.
	select {
	case <-ctx.Done():
	case <-time.After(refreshSettleDelay):
	}
}

// reconcileOrphans clears this daemon's retained Home Assistant discovery
// configs for a device that are no longer in the just-published set — features
// now excluded, renamed, re-platformed, or dropped by curated mode — so they
// do not linger as unavailable entities in HA. It runs asynchronously and is
// gated per device (a re-entrant call for the same device is skipped).
//
// The scope is ONE device, and that is not a refinement. A fleet-wide
// predicate would judge a second appliance's configs unclaimed during the
// window in which only the first has published, and retract them; this
// daemon mirrors several appliances that connect independently, so that
// window is the normal case rather than a race.
func (b *Bridge) reconcileOrphans(ctx context.Context, device string, published map[string]bool) {
	if b.hass == nil || b.plane == nil {
		return
	}
	b.reconcileMu.Lock()
	if b.reconciling[device] {
		b.reconcileMu.Unlock()
		return
	}
	b.reconciling[device] = true
	b.reconcileMu.Unlock()

	go func() {
		defer func() {
			b.reconcileMu.Lock()
			delete(b.reconciling, device)
			b.reconcileMu.Unlock()
		}()
		b.reconcileOrphansOnce(ctx, device, published)
	}()
}

// reconcileOrphansOnce is reconcileOrphans without the goroutine and
// without the re-entrancy gate: one window, one verdict, one retraction
// pass. It is separate so a test can drive the whole sequence and read
// what it cleared, rather than racing a background goroutine — both
// outcomes of that race look like a pass, which is how a sweep pin
// becomes a flake.
func (b *Bridge) reconcileOrphansOnce(ctx context.Context, device string, published map[string]bool) int {
	owned, res, err := b.collectOwnConfigs(ctx, device)
	if err != nil {
		b.logger.Warn("bridge.reconcile_sweep", slog.String("device", device), slog.String("err", err.Error()))
	}
	cleared := b.retract(ctx, b.orphanTopics(owned, published))
	if cleared > 0 {
		b.logger.Info("bridge.discovery_orphans_cleared",
			slog.String("device", device),
			// Inspected is logged beside the count because the pair is
			// what makes a silent sweep diagnosable: zero inspected means
			// the window saw none of this daemon's retained configs at
			// all, which is a completely different fault from a window
			// that saw all 687 and correctly found nothing orphaned. Both
			// read as "0 cleared" otherwise.
			slog.Int("inspected", res.Inspected),
			slog.Int("count", cleared))
	}
	return cleared
}

// collectOwnConfigs opens one report-only snapshot window over the
// discovery tree and returns the retained config topics that are this
// daemon's — by topic namespace AND by payload.
func (b *Bridge) collectOwnConfigs(ctx context.Context, devices ...string) ([]string, publisher.SweepResult, error) {
	var (
		mu    sync.Mutex
		owned []string
	)
	res, err := b.plane.Runtime().Sweep(ctx, publisher.SweepRequest{
		// Look, never touch: the retraction below is the caller's, over a
		// list the caller narrowed. See the note at the head of this file.
		ReportOnly: true,
		Window:     reconcileCollectWindow,
		Owns:       b.hass.OwnsConfigTopic(devices...),
		Inspect: func(t publisher.ConfigTopic, body []byte) {
			// Runs on the transport's read loop: cheap, and it publishes
			// nothing. IsOwnConfig is the payload half of the ownership
			// rule, and the half that keeps a sibling instance's configs
			// (F8) out of the list.
			if !b.hass.IsOwnConfig(body) {
				return
			}
			mu.Lock()
			owned = append(owned, b.hass.ConfigTopicFor(t))
			mu.Unlock()
		},
	})
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), owned...), res, err
}

// orphanTopics narrows the owned set to what nothing claims.
//
// Both claim sets are subtracted and neither is redundant. published is
// what the batch that just ran MINTED, including a config whose own
// publish failed — an open circuit breaker leaves it out of the runtime's
// declared set, and retracting it would delete an entity this daemon is
// actively trying to create. Declared is what the PROCESS has written,
// which covers a config published on an earlier, larger batch that a
// transient classification shrank.
func (b *Bridge) orphanTopics(owned []string, published map[string]bool) []string {
	claimed := make(map[string]bool, len(published))
	for topic := range published {
		claimed[topic] = true
	}
	for _, topic := range b.plane.Declared() {
		claimed[topic] = true
	}
	out := make([]string, 0, len(owned))
	for _, topic := range owned {
		if !claimed[topic] {
			out = append(out, topic)
		}
	}
	return out
}

// retract clears the given retained config topics and reports how many
// went out. An empty retained payload is MQTT's deletion.
func (b *Bridge) retract(ctx context.Context, topics []string) int {
	cleared := 0
	for _, topic := range topics {
		pctx, cancel := context.WithTimeout(ctx, publishTimeout)
		err := b.plane.Retract(pctx, topic)
		cancel()
		if err == nil {
			cleared++
		}
	}
	return cleared
}
