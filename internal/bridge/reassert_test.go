// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"log/slog"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// outageBridge is a real Bridge over the real publish plane and a recorder
// that can refuse a topic, with no discovery: what is under test is the
// state plane across a broker outage, and nothing else.
func outageBridge(t *testing.T) (*Bridge, *Device, *subRecorder) {
	t.Helper()
	cfg := testCfg()
	cfg.MQTTTopic = pinRoot
	rec := &subRecorder{}
	b, err := New(Deps{
		Config: cfg,
		MQTT:   rec,
		Plane:  planeFor(t, rec, int(pinQoS)),
		Logger: slog.New(slog.DiscardHandler),
		Devices: []DeviceSpec{{
			Config: profile.DeviceConfig{
				Name: "dishwasher", HaID: haIDFor("dishwasher"), Host: "192.168.1.50",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dev := b.devices[0]
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); dev.pub.run(ctx, b.safePublish) }()
	t.Cleanup(func() { cancel(); <-done })
	return b, dev, rec
}

func lastVal(rec *subRecorder, topic string) any {
	raw, ok := rec.lastPayload(topic)
	if !ok {
		return nil
	}
	return statusVal(string(raw))
}

// TestABrokerReconnectReassertsTheLinkAsItIsNow is the outage the status
// replay cannot cover: the appliance drops while the broker is away, the
// `online` false publish fails, and the replay on the next connection
// re-sends the last value the broker ACCEPTED — true. Without the
// re-assert, every entity of an unreachable appliance read as available
// until its link next changed.
func TestABrokerReconnectReassertsTheLinkAsItIsNow(t *testing.T) {
	b, dev, rec := outageBridge(t)
	online, state := dev.topics.Online(), dev.topics.ConnectionState()

	b.onState(dev, homeconnect.StateConnected)
	if got := lastVal(rec, online); got != true {
		t.Fatalf("online = %v before the outage, want true", got)
	}

	// The broker is gone and the appliance drops: the `online` false
	// publish fails, so the state plane still holds true.
	rec.setFail(online)
	b.onState(dev, homeconnect.StateOffline)
	rec.setFail("")
	if got := lastVal(rec, online); got != true {
		t.Fatalf("online = %v during the outage; the recorder took a refused publish", got)
	}
	b.PublishOnline(t.Context())
	waitUntil(t, "online re-asserted as false", func() bool { return lastVal(rec, online) == false })

	// A second outage, in which only connection_state moves.
	rec.setFail(state)
	b.onState(dev, homeconnect.StateReconnecting)
	rec.setFail("")
	if got := lastVal(rec, state); got != string(homeconnect.StateOffline) {
		t.Fatalf("connection_state = %v during the outage, want the accepted offline", got)
	}
	b.PublishOnline(t.Context())
	waitUntil(t, "connection_state re-asserted", func() bool {
		return lastVal(rec, state) == string(homeconnect.StateReconnecting)
	})
}

// TestABrokerReconnectRequeuesValuesThatChangedDuringTheOutage is the same
// gap for a feature: the value that changed while the broker was away is
// published on the next connection, after the replay of the older one.
func TestABrokerReconnectRequeuesValuesThatChangedDuringTheOutage(t *testing.T) {
	b, dev, rec := outageBridge(t)
	e, ok := dev.app.Entity(0x1002)
	if !ok {
		t.Fatal("fixture has no OperationState")
	}
	topic := dev.topics.state(e)

	dev.app.ApplyValues([]map[string]any{{"uid": 0x1002, "value": 0}})
	waitUntil(t, "the first value", func() bool { return lastVal(rec, topic) == "Inactive" })

	rec.setFail(topic)
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1002, "value": 3}})
	// The drain tried and failed; nothing reached the recorder.
	waitUntil(t, "the queue to drain", func() bool {
		dev.pub.mu.Lock()
		defer dev.pub.mu.Unlock()
		return len(dev.pub.queue) == 0
	})
	rec.setFail("")

	b.PublishOnline(t.Context())
	waitUntil(t, "the value from during the outage", func() bool { return lastVal(rec, topic) == "Run" })
}

// TestTheReassertSaysOnlyWhatOnStateHasDeclared: a broker reconnect
// during an onState pass must not declare the appliance online before that
// pass has — online goes out after the device document, and the re-assert
// says only what onState has already said.
func TestTheReassertSaysOnlyWhatOnStateHasDeclared(t *testing.T) {
	b, dev, rec := outageBridge(t)
	b.reassertLink(dev)
	if _, ok := rec.lastPayload(dev.topics.Online()); ok {
		t.Fatal("re-assert before any onState published online")
	}
	dev.linkMu.Lock()
	dev.link.state = homeconnect.StateConnected // onState is between its two halves
	dev.linkMu.Unlock()
	b.reassertLink(dev)
	if _, ok := rec.lastPayload(dev.topics.Online()); ok {
		t.Fatal("re-assert declared online before onState did")
	}
	if got := lastVal(rec, dev.topics.ConnectionState()); got != string(homeconnect.StateConnected) {
		t.Errorf("connection_state = %v, want connected", got)
	}
}
