// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
)

type reconcileNoopPub struct{}

func (reconcileNoopPub) Publish(context.Context, string, []byte, mqtt.QoS, bool, ...mqtt.PublishOption) error {
	return nil
}

func TestOrphanTopics(t *testing.T) {
	b := &Bridge{hass: hass.New(reconcileNoopPub{}, "homeassistant", "homeconnect", mqtt.QoS0, "en", false, nil)}

	own := func(uid string) []byte {
		return []byte(`{"unique_id":"` + uid + `","state_topic":"homeconnect/dw/x/state"}`)
	}
	retained := map[string][]byte{
		"homeassistant/sensor/dw/current/config": own("homeconnect_dw_current"),           // ours + still published -> keep
		"homeassistant/sensor/dw/orphan/config":  own("homeconnect_dw_orphan"),            // ours + not published -> clear
		"homeassistant/sensor/dw/foreign/config": []byte(`{"unique_id":"zigbee2mqtt_x"}`), // foreign -> never touch
		"homeassistant/sensor/dw/empty/config":   {},                                      // already cleared -> skip
	}
	published := map[string]bool{"homeassistant/sensor/dw/current/config": true}

	orphans := b.orphanTopics(retained, published)
	if len(orphans) != 1 || orphans[0] != "homeassistant/sensor/dw/orphan/config" {
		t.Errorf("orphanTopics = %v, want [homeassistant/sensor/dw/orphan/config]", orphans)
	}
}
