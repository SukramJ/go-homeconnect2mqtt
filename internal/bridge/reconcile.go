// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/mqtt"
)

// reconcileCollectWindow is how long we collect retained discovery configs
// after subscribing; the broker delivers them right after the subscribe.
const reconcileCollectWindow = 2 * time.Second

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
		err := b.mqtt.Subscribe(ctx, filter, mqtt.QoS0, func(topic string, payload []byte) {
			mu.Lock()
			retained[topic] = append([]byte(nil), payload...)
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
