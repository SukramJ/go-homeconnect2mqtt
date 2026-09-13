// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
)

func TestRunVersion(t *testing.T) {
	var errBuf bytes.Buffer
	if code := run([]string{"--version"}, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(errBuf.String(), "go-homeconnect2mqtt") {
		t.Errorf("version output = %q", errBuf.String())
	}
}

func TestRunBadFlag(t *testing.T) {
	var errBuf bytes.Buffer
	if code := run([]string{"--nope"}, &errBuf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunNoConfig(t *testing.T) {
	// With no config file found, the daemon fails fast with a non-zero code.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var errBuf bytes.Buffer
	if code := run([]string{"--config", "/nonexistent/config.yaml"}, &errBuf); code == 0 {
		t.Fatalf("expected non-zero exit for missing config")
	}
}

// failingPublisher always reports a broker-side failure so the breaker
// counts every publish against its threshold.
type failingPublisher struct{ calls int }

func (p *failingPublisher) Publish(context.Context, string, []byte, mqtt.QoS, bool, ...mqtt.PublishOption) error {
	p.calls++
	return mqtt.ErrNotConnected
}

// recordingSubscriber captures Subscribe/Unsubscribe filters so the
// test can prove the session delegates them to the raw client.
type recordingSubscriber struct {
	subscribed   []string
	unsubscribed []string
}

func (s *recordingSubscriber) Subscribe(_ context.Context, filter string, _ mqtt.QoS, _ mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.subscribed = append(s.subscribed, filter)
	return mqtt.SubscribeResult{}, nil
}

func (s *recordingSubscriber) Unsubscribe(_ context.Context, filter string) error {
	s.unsubscribed = append(s.unsubscribed, filter)
	return nil
}

// TestMQTTSessionPublishIsCircuitGated proves the bridge-facing session
// (mqtt.SplitClient over the breaker and the raw client)
// routes Publish through the breaker: once the failure threshold is
// reached, publishes fail fast with ErrCircuitOpen and no longer hit
// the underlying client.
func TestMQTTSessionPublishIsCircuitGated(t *testing.T) {
	t.Parallel()

	pub := &failingPublisher{}
	session := mqtt.SplitClient(
		mqtt.NewBreaker(pub, mqtt.BreakerConfig{FailureThreshold: 1}),
		&recordingSubscriber{},
	)

	err := session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	if !errors.Is(err, mqtt.ErrNotConnected) {
		t.Fatalf("first publish: got %v, want ErrNotConnected", err)
	}
	err = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	if !errors.Is(err, mqtt.ErrCircuitOpen) {
		t.Fatalf("second publish: got %v, want ErrCircuitOpen", err)
	}
	if pub.calls != 1 {
		t.Fatalf("underlying publisher saw %d calls, want 1 (open circuit must fail fast)", pub.calls)
	}
}

// TestMQTTSessionSubscribeBypassesBreaker proves subscriptions are not
// affected by the publish-side circuit state.
func TestMQTTSessionSubscribeBypassesBreaker(t *testing.T) {
	t.Parallel()

	sub := &recordingSubscriber{}
	session := mqtt.SplitClient(
		mqtt.NewBreaker(&failingPublisher{}, mqtt.BreakerConfig{FailureThreshold: 1}),
		sub,
	)

	// Trip the circuit open on the publish side.
	_ = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	_ = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)

	if _, err := session.Subscribe(t.Context(), "cmd/#", mqtt.QoS1, func(*mqtt.Message) {}); err != nil {
		t.Fatalf("subscribe with open circuit: %v", err)
	}
	if err := session.Unsubscribe(t.Context(), "cmd/#"); err != nil {
		t.Fatalf("unsubscribe with open circuit: %v", err)
	}
	if len(sub.subscribed) != 1 || sub.subscribed[0] != "cmd/#" {
		t.Fatalf("subscriber saw %v, want [cmd/#]", sub.subscribed)
	}
	if len(sub.unsubscribed) != 1 || sub.unsubscribed[0] != "cmd/#" {
		t.Fatalf("unsubscriber saw %v, want [cmd/#]", sub.unsubscribed)
	}
}

// TestWillIsTheAvailabilitySourceEveryEntityReads is F1 on the publisher
// side. The Last Will is the one publish this daemon never makes itself,
// so it is the one the tests could not see: it is asserted here off the
// mqtt.TCPConfig the transport is handed, not off the constants that went
// into it.
//
// Until F1 the will wrote <MQTT_TOPIC>/status, which no discovery payload
// referenced — so a killed daemon left every entity showing its last
// retained value forever. The topic did not move (it was already in the
// daemon's own publish root, not in Home Assistant's discovery tree,
// which is why no retained copy needed retracting); what changed is that
// every payload now declares it.
func TestWillIsTheAvailabilitySourceEveryEntityReads(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{MQTTServer: "tcp://b:1883", MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: 1}
	will := mqttClientConfig(cfg, slog.New(slog.DiscardHandler)).Will
	if will == nil {
		t.Fatal("no Last Will configured: a killed daemon would leave every entity available forever")
	}
	if want := layout.Bridge(cfg.MQTTTopic); will.Topic != want {
		t.Errorf("will topic = %q, want %q", will.Topic, want)
	}
	if strings.HasPrefix(will.Topic, cfg.HASSBaseTopic+"/") {
		t.Errorf("will topic %q is inside Home Assistant's discovery tree", will.Topic)
	}
	if string(will.Payload) != hass.PayloadNotAvailable {
		t.Errorf("will payload = %q, want %q", will.Payload, hass.PayloadNotAvailable)
	}
	if !will.Retain {
		t.Error("will is not retained: a subscriber connecting after the death sees nothing")
	}
	if will.QoS != mqttQoS(cfg) {
		t.Errorf("will qos = %v, want %v (the birth publish's guarantee)", will.QoS, mqttQoS(cfg))
	}
}

// TestMQTTQoSZeroStaysQoSZero is F9's pin at the translation point.
// MQTT_QOS: 0 is an operator-facing promise of at-most-once, and the
// go-hamqtt publisher vocabulary this migration moves onto reads QoS(0) as
// *unset*, resolving it to QoS 1. The one place that translates it is
// pinned so the upgrade cannot happen silently.
func TestMQTTQoSZeroStaysQoSZero(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]mqtt.QoS{0: mqtt.QoS0, 1: mqtt.QoS1} {
		cfg := &config.Config{MQTTQoS: in}
		if got := mqttQoS(cfg); got != want {
			t.Errorf("MQTT_QOS: %d -> %v, want %v", in, got, want)
		}
		if got := mqttClientConfig(cfg, slog.New(slog.DiscardHandler)).Will.QoS; got != want {
			t.Errorf("MQTT_QOS: %d -> will qos %v, want %v", in, got, want)
		}
	}
}
