// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"strings"
	"testing"
)

// mapEnv is a hermetic Env backed by a map.
type mapEnv map[string]string

func (m mapEnv) LookupEnv(key string) (string, bool) { v, ok := m[key]; return v, ok }

func (m mapEnv) Environ() []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(strings.NewReader("MQTT_SERVER: tcp://localhost:1883\n"), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MQTTTopic != DefaultMQTTTopic {
		t.Errorf("MQTTTopic = %q, want %q", cfg.MQTTTopic, DefaultMQTTTopic)
	}
	if cfg.MQTTQoS != DefaultMQTTQoS {
		t.Errorf("MQTTQoS = %d, want %d", cfg.MQTTQoS, DefaultMQTTQoS)
	}
	if !cfg.RetainEnabled() {
		t.Error("RetainEnabled() = false, want true (default)")
	}
	if cfg.AppName != DefaultAppName {
		t.Errorf("AppName = %q, want %q", cfg.AppName, DefaultAppName)
	}
	if cfg.ReconnectMax != DefaultReconnectMax {
		t.Errorf("ReconnectMax = %d, want %d", cfg.ReconnectMax, DefaultReconnectMax)
	}
	if cfg.Language != DefaultLanguage {
		t.Errorf("Language = %q, want %q", cfg.Language, DefaultLanguage)
	}
}

func TestRetainCanBeForcedFalse(t *testing.T) {
	cfg, err := Load(strings.NewReader("MQTT_SERVER: tcp://h:1883\nMQTT_RETAIN: false\n"), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RetainEnabled() {
		t.Error("RetainEnabled() = true, want false when explicitly set")
	}
}

func TestEnvOverridesWithCoercion(t *testing.T) {
	env := mapEnv{
		"HC2M_MQTT_SERVER":          "tcp://broker:1883",
		"HC2M_HASS_BIRTH_GRACETIME": "30",
		"HC2M_HASS_ENABLE":          "true",
		"HC2M_DEBUG":                "true",
	}
	cfg, err := Load(strings.NewReader("MQTT_TOPIC: custom\n"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MQTTServer != "tcp://broker:1883" {
		t.Errorf("MQTTServer = %q", cfg.MQTTServer)
	}
	if cfg.HASSBirthGracetime != 30 {
		t.Errorf("HASSBirthGracetime = %d, want 30 (int coercion from env)", cfg.HASSBirthGracetime)
	}
	if !cfg.HASSEnable {
		t.Error("HASSEnable should be true from env")
	}
	if !cfg.Debug {
		t.Error("Debug should be true from env")
	}
	if cfg.MQTTTopic != "custom" {
		t.Errorf("MQTTTopic = %q, want custom (from yaml)", cfg.MQTTTopic)
	}
}

func TestValidateAggregatesIssues(t *testing.T) {
	// Empty body: MQTT_SERVER missing -> single issue.
	_, err := Load(strings.NewReader(""), mapEnv{})
	if err == nil {
		t.Fatal("expected validation error for missing MQTT_SERVER")
	}
	var ve *ValidationError
	if !asValidation(err, &ve) {
		t.Fatalf("error is not *ValidationError: %v", err)
	}
	if len(ve.Issues) == 0 {
		t.Error("expected at least one issue")
	}
}

func TestValidateMultipleIssues(t *testing.T) {
	c := &Config{
		MQTTServer:       "tcp://h:1883",
		MQTTTopic:        "t",
		MQTTQoS:          5,  // out of range
		ReconnectInitial: 30, // > max
		ReconnectMax:     10, // < initial
		HandshakeTimeout: 60,
		SendTimeout:      20,
		Heartbeat:        20,
		Language:         "fr", // not allowed
		MQTTLogin:        "u",  // password missing -> both-or-neither
	}
	err := Validate(c)
	var ve *ValidationError
	if !asValidation(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if len(ve.Issues) < 4 {
		t.Errorf("expected >=4 issues, got %d: %v", len(ve.Issues), ve.Issues)
	}
	msg := ve.Error()
	if !strings.Contains(msg, "validation issue") {
		t.Errorf("aggregated message malformed: %q", msg)
	}
}

func TestValidateWebBind(t *testing.T) {
	c := &Config{
		MQTTServer: "tcp://h:1883", MQTTTopic: "t", MQTTQoS: 1,
		ReconnectInitial: 1, ReconnectMax: 30, HandshakeTimeout: 60,
		SendTimeout: 20, Heartbeat: 20, Language: "en",
		WebEnable: true, WebBind: "not-a-hostport",
	}
	if err := Validate(c); err == nil {
		t.Fatal("expected WEB_BIND validation failure")
	}
}

func TestDurationHelpers(t *testing.T) {
	c := &Config{ReconnectInitial: 2, ReconnectMax: 30, ReconnectJitter: 500, HandshakeTimeout: 60, SendTimeout: 20, Heartbeat: 20, HASSBirthGracetime: 15}
	if c.ReconnectInitialDuration().Seconds() != 2 {
		t.Error("ReconnectInitialDuration")
	}
	if c.ReconnectJitterDuration().Milliseconds() != 500 {
		t.Error("ReconnectJitterDuration")
	}
	if c.HeartbeatDuration().Seconds() != 20 {
		t.Error("HeartbeatDuration")
	}
}

// asValidation is a tiny errors.As helper kept local to avoid an import
// churn in the test.
func asValidation(err error, target **ValidationError) bool {
	for err != nil {
		if ve, ok := err.(*ValidationError); ok {
			*target = ve
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
		} else {
			return false
		}
	}
	return false
}
