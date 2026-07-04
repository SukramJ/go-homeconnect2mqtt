// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/SukramJ/go-mqtt"
)

// reconcileCollectWindow is how long we collect retained discovery configs
// after subscribing; the broker delivers them right after the subscribe.
const reconcileCollectWindow = 2 * time.Second

// refreshSettleDelay is how long we wait after clearing all discovery configs so
// Home Assistant removes the entities before the workers re-publish them.
const refreshSettleDelay = 3 * time.Second

// refreshDiscoveryOnce, when HASS_DISCOVERY_REFRESH is set, clears every retained
// discovery config this daemon owns and waits, so the per-device workers then
// re-create the entities from scratch. This is the only way to push changes Home
// Assistant caches at first registration (entity_category, name). It is a
// one-shot, operator-triggered migration — turn the flag off after one run.
func (b *Bridge) refreshDiscoveryOnce(ctx context.Context) {
	if !b.cfg.HASSDiscoveryRefresh || b.hass == nil {
		return
	}
	filter := b.hass.ConfigFilter()
	var mu sync.Mutex
	retained := map[string][]byte{}
	if _, err := b.mqtt.Subscribe(ctx, filter, mqtt.QoS0, func(msg *mqtt.Message) {
		mu.Lock()
		retained[msg.Topic] = append([]byte(nil), msg.Payload...)
		mu.Unlock()
	}); err != nil {
		b.logger.Warn("bridge.refresh_subscribe", slog.String("err", err.Error()))
		return
	}
	select {
	case <-ctx.Done():
		_ = b.mqtt.Unsubscribe(context.WithoutCancel(ctx), filter)
		return
	case <-time.After(reconcileCollectWindow):
	}
	_ = b.mqtt.Unsubscribe(ctx, filter)

	cleared := 0
	mu.Lock()
	for topic, payload := range retained {
		if len(payload) == 0 || !b.hass.IsOwnConfig(payload) {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, publishTimeout)
		if err := b.mqtt.Publish(pctx, topic, nil, b.qos, true); err == nil {
			cleared++
		}
		cancel()
	}
	mu.Unlock()
	b.logger.Info("bridge.discovery_refresh", slog.Int("cleared", cleared))
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
func (b *Bridge) reconcileOrphans(ctx context.Context, device string, published map[string]bool) {
	if b.hass == nil {
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

		filter := b.hass.DeviceConfigFilter(device)
		var mu sync.Mutex
		retained := map[string][]byte{}
		_, err := b.mqtt.Subscribe(ctx, filter, mqtt.QoS0, func(msg *mqtt.Message) {
			mu.Lock()
			retained[msg.Topic] = append([]byte(nil), msg.Payload...)
			mu.Unlock()
		})
		if err != nil {
			b.logger.Warn("bridge.reconcile_subscribe", slog.String("device", device), slog.String("err", err.Error()))
			return
		}
		// Retained configs arrive right after subscribe; collect briefly.
		select {
		case <-ctx.Done():
			// ctx is cancelled here; derive a non-cancelled child so the
			// unsubscribe still goes out without breaking the context chain.
			_ = b.mqtt.Unsubscribe(context.WithoutCancel(ctx), filter)
			return
		case <-time.After(reconcileCollectWindow):
		}
		_ = b.mqtt.Unsubscribe(ctx, filter)

		mu.Lock()
		orphans := b.orphanTopics(retained, published)
		mu.Unlock()

		cleared := 0
		for _, topic := range orphans {
			pctx, cancel := context.WithTimeout(ctx, publishTimeout)
			err := b.mqtt.Publish(pctx, topic, nil, b.qos, true) // empty retained payload removes it
			cancel()
			if err == nil {
				cleared++
			}
		}
		if cleared > 0 {
			b.logger.Info("bridge.discovery_orphans_cleared",
				slog.String("device", device), slog.Int("count", cleared))
		}
	}()
}

// orphanTopics returns the retained config topics that are ours (IsOwnConfig)
// and no longer present in the published set. A foreign integration's config or
// an already-cleared (empty) topic is never returned.
func (b *Bridge) orphanTopics(retained map[string][]byte, published map[string]bool) []string {
	var out []string
	for topic, payload := range retained {
		if len(payload) == 0 || published[topic] {
			continue
		}
		if !b.hass.IsOwnConfig(payload) {
			continue
		}
		out = append(out, topic)
	}
	return out
}
