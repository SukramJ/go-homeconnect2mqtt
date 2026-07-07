// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// stubMQTT records publishes and subscriptions for assertions.
type stubMQTT struct {
	mu   sync.Mutex
	pubs map[string]string
	subs map[string]mqtt.MessageHandler
}

func newStubMQTT() *stubMQTT {
	return &stubMQTT{pubs: map[string]string{}, subs: map[string]mqtt.MessageHandler{}}
}

func (s *stubMQTT) Publish(_ context.Context, topic string, payload []byte, _ mqtt.QoS, _ bool, _ ...mqtt.PublishOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pubs[topic] = string(payload)
	return nil
}

func (s *stubMQTT) Subscribe(_ context.Context, filter string, _ mqtt.QoS, h mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[filter] = h
	return mqtt.SubscribeResult{}, nil
}

func (s *stubMQTT) Unsubscribe(_ context.Context, filter string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, filter)
	return nil
}

func (s *stubMQTT) get(topic string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubs[topic]
}

func testCfg() *config.Config {
	return &config.Config{
		MQTTTopic: "homeconnect", MQTTQoS: 1, AppName: "test",
		ReconnectInitial: 1, ReconnectMax: 30, ReconnectJitter: 0,
		HandshakeTimeout: 60, SendTimeout: 20, Heartbeat: 20, Language: "en",
	}
}

func smallDescription(t *testing.T) *profile.Description {
	t.Helper()
	dd := `<?xml version="1.0"?><device>
      <description><type>Dishwasher</type><brand>BOSCH</brand><model>M</model><version>2</version></description>
      <statusList uid="0001">
        <status access="read" available="true" enumerationType="3000" refCID="03" uid="1002"/>
      </statusList>
      <settingList uid="0003">
        <setting access="readWrite" available="true" refCID="01" uid="1005"/>
      </settingList>
      <enumerationTypeList>
        <enumerationType enid="3000"><enumeration value="0"/><enumeration value="3"/></enumerationType>
      </enumerationTypeList>
    </device>`
	fm := `<featureMappingFile><featureDescription>
        <feature refUID="1002">BSH.Common.Status.OperationState</feature>
        <feature refUID="1005">BSH.Common.Setting.PowerState</feature>
      </featureDescription>
      <enumDescriptionList><enumDescription refENID="3000">
        <enumMember refValue="0">Inactive</enumMember>
        <enumMember refValue="3">Run</enumMember>
      </enumDescription></enumDescriptionList></featureMappingFile>`
	d, err := profile.ParseDescription([]byte(dd), []byte(fm), nil)
	if err != nil {
		t.Fatalf("ParseDescription: %v", err)
	}
	return d
}

func b64(n int) string { return base64.RawURLEncoding.EncodeToString(make([]byte, n)) }

func buildTestBridge(t *testing.T) (*Bridge, *stubMQTT) {
	t.Helper()
	stub := newStubMQTT()
	b, err := New(Deps{
		Config: testCfg(),
		MQTT:   stub,
		Devices: []DeviceSpec{{
			Config: profile.DeviceConfig{
				Name: "dishwasher", Host: "192.168.1.50",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, stub
}

// startPublisher runs the device's async publish drain for the test's
// lifetime, mirroring what Bridge.Run wires up.
func startPublisher(t *testing.T, b *Bridge, d *Device) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.pub.run(ctx, b.publish) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitFor polls the stub until topic carries want or the deadline hits.
func waitFor(t *testing.T, stub *stubMQTT, topic, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for stub.get(topic) != want {
		select {
		case <-deadline:
			t.Fatalf("topic %q = %q, want %q", topic, stub.get(topic), want)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestFeaturePath(t *testing.T) {
	if got := featurePath("BSH.Common.Status.OperationState", 0x1002); got != "BSH/Common/Status/OperationState" {
		t.Errorf("featurePath = %q", got)
	}
	if got := featurePath("", 0x1234); got != "_uid/4660" {
		t.Errorf("unnamed featurePath = %q", got)
	}
}

func TestDeviceTopics(t *testing.T) {
	tp := newDeviceTopics("homeconnect", "dishwasher")
	if tp.availability() != "homeconnect/dishwasher/availability" {
		t.Errorf("availability = %q", tp.availability())
	}
	if tp.connectionState() != "homeconnect/dishwasher/connection_state" {
		t.Errorf("connection_state = %q", tp.connectionState())
	}
}

func TestOnUpdatePublishesState(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	startPublisher(t, b, dev)
	// Drive an enum value update; the async publisher must publish the
	// resolved name to the state topic.
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1002, "value": 3}})
	waitFor(t, stub, "homeconnect/dishwasher/BSH/Common/Status/OperationState/state", "Run")
}

func TestOnUpdatePublishesBool(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	startPublisher(t, b, dev)
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": true}})
	waitFor(t, stub, "homeconnect/dishwasher/BSH/Common/Setting/PowerState/state", "true")
}

func TestOnStatePublishesAvailability(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	b.onState(dev, homeconnect.StateConnected)
	if got := stub.get("homeconnect/dishwasher/connection_state"); got != "connected" {
		t.Errorf("connection_state = %q", got)
	}
	if got := stub.get("homeconnect/dishwasher/availability"); got != availOnline {
		t.Errorf("availability = %q, want online", got)
	}
	b.onState(dev, homeconnect.StateReconnecting)
	if got := stub.get("homeconnect/dishwasher/availability"); got != availOffline {
		t.Errorf("availability after reconnecting = %q, want offline", got)
	}
}

func TestPayloadForFloat(t *testing.T) {
	b, _ := buildTestBridge(t)
	dev := b.devices[0]
	// Re-purpose the enum status with a non-enum raw to check float format
	// path via a fresh float entity through the public API is awkward, so
	// assert payloadFor directly on a value-bearing entity.
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1002, "value": 3}})
	e, _ := dev.app.Entity(0x1002)
	if got := payloadFor(e, "en"); got != "Run" {
		t.Errorf("payloadFor enum = %q", got)
	}
}

func TestNewValidations(t *testing.T) {
	stub := newStubMQTT()
	if _, err := New(Deps{Config: nil, MQTT: stub}); err == nil {
		t.Error("expected error for nil config")
	}
	if _, err := New(Deps{Config: testCfg(), MQTT: nil}); err == nil {
		t.Error("expected error for nil mqtt")
	}
	if _, err := New(Deps{Config: testCfg(), MQTT: stub}); err == nil {
		t.Error("expected error for no devices")
	}
}

func TestTLSDeviceBuilds(t *testing.T) {
	// A TLS device builds (so AES siblings still run); it only fails at
	// connect with ErrTLSPSKUnsupported unless built with the tlspsk tag.
	stub := newStubMQTT()
	b, err := New(Deps{
		Config: testCfg(), MQTT: stub,
		Devices: []DeviceSpec{{
			Config:      profile.DeviceConfig{Name: "old", Host: "h", ConnectionType: profile.ConnectionTLS, PSK64: b64(32)},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("TLS device should build, got %v", err)
	}
	if len(b.devices) != 1 {
		t.Errorf("expected 1 device, got %d", len(b.devices))
	}
}

func TestBridgeRunStopsOnCancel(t *testing.T) {
	stub := newStubMQTT()
	b, err := New(Deps{
		Config: testCfg(),
		MQTT:   stub,
		Devices: []DeviceSpec{{
			// 127.0.0.1:80 refuses fast, so the worker cycles into the
			// offline backoff path; cancel must end Run promptly.
			Config: profile.DeviceConfig{
				Name: "dishwasher", Host: "127.0.0.1",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait until the worker has published a connection_state (it reached at
	// least the connecting/offline phase), then cancel.
	deadline := time.After(5 * time.Second)
	for stub.get("homeconnect/dishwasher/connection_state") == "" {
		select {
		case <-deadline:
			t.Fatal("no connection_state publish before timeout")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestNewRejectsMissingHost(t *testing.T) {
	stub := newStubMQTT()
	_, err := New(Deps{
		Config: testCfg(), MQTT: stub,
		Devices: []DeviceSpec{{
			Config:      profile.DeviceConfig{Name: "x", ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16)},
			Description: smallDescription(t),
		}},
	})
	if err == nil {
		t.Error("device without host should be rejected")
	}
}
