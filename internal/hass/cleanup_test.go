// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/mqtt"
)

func newDisc() *Discovery {
	return New(newStubPub(), "homeassistant", "homeconnect", mqtt.QoS(1), "en", false, nil)
}

func TestIsOwnConfig(t *testing.T) {
	d := newDisc()
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"ours", `{"unique_id":"homeconnect_dw_op","state_topic":"homeconnect/dw/X/state"}`, true},
		{"ours no state (button)", `{"unique_id":"homeconnect_dw_btn"}`, true},
		{"foreign unique_id", `{"unique_id":"zigbee2mqtt_x","state_topic":"zigbee2mqtt/x"}`, false},
		{"foreign state root", `{"unique_id":"homeconnect_dw_op","state_topic":"other/dw/X/state"}`, false},
		{"not json", `not-json`, false},
	}
	for _, c := range cases {
		if got := d.IsOwnConfig([]byte(c.payload)); got != c.want {
			t.Errorf("%s: IsOwnConfig = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDeviceConfigFilter(t *testing.T) {
	d := newDisc()
	if got := d.DeviceConfigFilter("Geschirrspüler"); got != "homeassistant/+/geschirrspuler/+/config" {
		t.Errorf("DeviceConfigFilter = %q", got)
	}
}

func TestPublishDeviceReturnsTopics(t *testing.T) {
	app, entities := buildEntities(t)
	published := newDisc().PublishDevice(context.Background(), "dw", app.Info(), entities)
	if !published["homeassistant/sensor/dw/bsh_common_status_operationstate/config"] {
		t.Errorf("published set missing OperationState (%d topics)", len(published))
	}
}
