// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"errors"
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
	if cfg.QoSLevel() != DefaultMQTTQoS {
		t.Errorf("MQTTQoS = %d, want %d", cfg.QoSLevel(), DefaultMQTTQoS)
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

// TestMQTTQoSZeroSurvivesTheLoader is F9's pin one layer below where it was
// pinned before, and the layer that actually defeated it.
//
// The translation point (internal/haplane/qos.go) has always mapped 0 to
// at-most-once, and TestMQTTQoSZeroStaysQoSZero has always asserted it —
// over a config.Config built BY HAND. Between an operator's file and that
// function sits applyDefaults, which read `MQTT_QOS: 0` as "unset" and
// wrote 1, so the level the daemon documents, offers in
// config-template.yaml and accepts in Validate could not be obtained from
// a config file at all. Nothing noticed, because no test had ever asked
// the LOADER.
//
// So this drives config.Load, from both sources an operator has, and it
// asserts the default is still the default — the over-correction that
// would silently downgrade every installation instead.
func TestMQTTQoSZeroSurvivesTheLoader(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		env  mapEnv
		want int
	}{
		{"an explicit 0 in the file", "MQTT_SERVER: tcp://h:1883\nMQTT_QOS: 0\n", nil, 0},
		{"an explicit 1 in the file", "MQTT_SERVER: tcp://h:1883\nMQTT_QOS: 1\n", nil, 1},
		{"the key absent", "MQTT_SERVER: tcp://h:1883\n", nil, DefaultMQTTQoS},
		{"0 from the environment", "MQTT_SERVER: tcp://h:1883\n", mapEnv{EnvPrefix + "MQTT_QOS": "0"}, 0},
		{"0 from the environment over a 1 in the file", "MQTT_SERVER: tcp://h:1883\nMQTT_QOS: 1\n", mapEnv{EnvPrefix + "MQTT_QOS": "0"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env Env
			if tc.env != nil {
				env = tc.env
			}
			cfg, err := Load(strings.NewReader(tc.yaml), env)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.QoSLevel(); got != tc.want {
				t.Errorf("MQTT_QOS = %d, want %d — 0 is a level an operator can ask for, "+
					"not the absence of an answer", got, tc.want)
			}
		})
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
	if !errors.As(err, &ve) {
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
		MQTTQoS:          intPtr(5), // out of range
		ReconnectInitial: 30,        // > max
		ReconnectMax:     10,        // < initial
		HandshakeTimeout: 60,
		SendTimeout:      20,
		Heartbeat:        20,
		Language:         "fr", // not allowed
		MQTTLogin:        "u",  // password missing -> both-or-neither
	}
	err := Validate(c)
	var ve *ValidationError
	if !errors.As(err, &ve) {
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
		MQTTServer: "tcp://h:1883", MQTTTopic: "t", MQTTQoS: intPtr(1),
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

// intPtr is MQTT_QOS's sentinel in a test literal. The field is a pointer
// so an explicit `MQTT_QOS: 0` survives applyDefaults; see Config.MQTTQoS.
func intPtr(v int) *int { return &v }
