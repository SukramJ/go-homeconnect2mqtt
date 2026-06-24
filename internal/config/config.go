// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package config loads, merges and validates the daemon configuration.
//
// Resolution order is YAML file -> HC2M_* environment overrides ->
// defaults -> aggregated validation, mirroring the sister project
// go-mtec2mqtt. The Config struct is intentionally flat and maps 1:1 to
// the YAML keys so operators can reason about the file without nesting.
package config

import "time"

// Daemon-wide constants consumed by the loader engine in load.go.
const (
	// ClientID is the MQTT client identifier.
	ClientID = "homeconnect2mqtt"
	// EnvPrefix is the prefix for environment overrides (HC2M_MQTT_SERVER=...).
	EnvPrefix = "HC2M_"
	// AppDirName is the per-user config sub-directory under XDG/APPDATA.
	AppDirName = "homeconnect2mqtt"
	// ConfigFile is the config file name searched by Locate.
	ConfigFile = "config.yaml"
)

// Config is the flat daemon configuration, decoded directly from YAML.
//
// Per-device settings (host, keys, profile path) live in a separate
// devices file handled by internal/profile, keeping operator secrets out
// of the main config.
type Config struct {
	// --- MQTT ---
	MQTTServer   string `yaml:"MQTT_SERVER"`
	MQTTLogin    string `yaml:"MQTT_LOGIN"`
	MQTTPassword string `yaml:"MQTT_PASSWORD"`
	MQTTTopic    string `yaml:"MQTT_TOPIC"`
	MQTTQoS      int    `yaml:"MQTT_QOS"`
	// MQTTRetain is a pointer so an unset value can default to true while
	// still letting operators force false.
	MQTTRetain *bool `yaml:"MQTT_RETAIN"`

	// --- Home Assistant discovery ---
	HASSEnable         bool   `yaml:"HASS_ENABLE"`
	HASSBaseTopic      string `yaml:"HASS_BASE_TOPIC"`
	HASSBirthGracetime int    `yaml:"HASS_BIRTH_GRACETIME"` // seconds

	// --- Connection / resilience (see docs/05-resilience.md) ---
	AppName          string `yaml:"APP_NAME"`
	AppID            string `yaml:"APP_ID"`
	ReconnectInitial int    `yaml:"RECONNECT_INITIAL"` // seconds
	ReconnectMax     int    `yaml:"RECONNECT_MAX"`     // seconds
	ReconnectJitter  int    `yaml:"RECONNECT_JITTER"`  // milliseconds
	HandshakeTimeout int    `yaml:"HANDSHAKE_TIMEOUT"` // seconds
	SendTimeout      int    `yaml:"SEND_TIMEOUT"`      // seconds
	Heartbeat        int    `yaml:"HEARTBEAT"`         // seconds

	// --- Web UI (optional, opt-in) ---
	WebEnable   bool   `yaml:"WEB_ENABLE"`
	WebBind     string `yaml:"WEB_BIND"`
	WebUser     string `yaml:"WEB_USER"`
	WebPassword string `yaml:"WEB_PASSWORD"`

	// --- Misc ---
	Language string `yaml:"LANGUAGE"`
	Debug    bool   `yaml:"DEBUG"`
}

// ReconnectInitialDuration returns the initial reconnect backoff.
func (c *Config) ReconnectInitialDuration() time.Duration {
	return time.Duration(c.ReconnectInitial) * time.Second
}

// ReconnectMaxDuration returns the maximum reconnect backoff.
func (c *Config) ReconnectMaxDuration() time.Duration {
	return time.Duration(c.ReconnectMax) * time.Second
}

// ReconnectJitterDuration returns the reconnect jitter window.
func (c *Config) ReconnectJitterDuration() time.Duration {
	return time.Duration(c.ReconnectJitter) * time.Millisecond
}

// HandshakeTimeoutDuration returns the device handshake timeout.
func (c *Config) HandshakeTimeoutDuration() time.Duration {
	return time.Duration(c.HandshakeTimeout) * time.Second
}

// SendTimeoutDuration returns the request/response timeout.
func (c *Config) SendTimeoutDuration() time.Duration {
	return time.Duration(c.SendTimeout) * time.Second
}

// HeartbeatDuration returns the websocket heartbeat interval.
func (c *Config) HeartbeatDuration() time.Duration {
	return time.Duration(c.Heartbeat) * time.Second
}

// HASSBirthGracetimeDuration returns the Home Assistant birth grace time.
func (c *Config) HASSBirthGracetimeDuration() time.Duration {
	return time.Duration(c.HASSBirthGracetime) * time.Second
}

// RetainEnabled reports whether MQTT messages should be published with
// the retain flag. Unset (nil) defaults to true.
func (c *Config) RetainEnabled() bool {
	return c.MQTTRetain == nil || *c.MQTTRetain
}
