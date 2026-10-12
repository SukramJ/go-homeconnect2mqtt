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
	// MQTTCA is the path to a PEM file with the CA certificate(s) used to
	// verify the broker's TLS certificate. Empty uses the system trust
	// store. Read only when MQTT_SERVER selects a TLS scheme (tls://,
	// ssl://, mqtts://); on a plaintext scheme the library ignores it and
	// logs a warning.
	MQTTCA string `yaml:"MQTT_CA"`
	// MQTTQoS is a pointer for the same reason MQTTRetain is, and the
	// reason is not symmetry: 0 is a LEVEL an operator can ask for, and a
	// bare int cannot tell `MQTT_QOS: 0` from "the key is absent".
	// applyDefaults filled the zero with 1, so the at-most-once level this
	// daemon documents, offers in config-template.yaml and accepts in
	// Validate could not be reached from a config file at all — F9 was
	// fixed at the translation point (internal/haplane/qos.go) and
	// defeated one layer below it, where every test that watched for it
	// built a Config by hand instead of loading one.
	//
	// Since 0.15.0 it governs what lies OUTSIDE the convention's status and
	// set functions: the discovery configs and their retractions, the
	// `<name>/connected` markers and the Last Will, and the snapshot windows.
	// Status items are QoS 0 and `set` is subscribed at QoS 1, as
	// mqtt-smarthome 2.0 and openccu-loom ADR 0083 fix them.
	MQTTQoS *int `yaml:"MQTT_QOS"`
	// MQTTMaintenance switches the mqtt-smarthome maintenance topics
	// (`<name>/maintenance/…`) on or off. A pointer so an unset value can
	// default to true while still letting operators force false.
	MQTTMaintenance *bool `yaml:"MQTT_MAINTENANCE"`
	// MQTTStatsInterval is the period of `<name>/maintenance/stats` in
	// seconds. A pointer for the reason MQTTQoS is one: 0 is an answer —
	// "off" — and not the absence of one.
	MQTTStatsInterval *int `yaml:"MQTT_STATS_INTERVAL"`

	// --- Home Assistant discovery ---
	HASSEnable         bool   `yaml:"HASS_ENABLE"`
	HASSBaseTopic      string `yaml:"HASS_BASE_TOPIC"`
	HASSBirthGracetime int    `yaml:"HASS_BIRTH_GRACETIME"` // seconds
	HASSDiscovery      string `yaml:"HASS_DISCOVERY"`       // full | curated
	// HASSDiscoveryRefresh is a one-shot migration flag: on start the daemon
	// clears every retained discovery config it owns and re-creates the entities,
	// so Home Assistant picks up changes it caches at first registration
	// (entity_category, name). Turn it off again after one run.
	HASSDiscoveryRefresh bool `yaml:"HASS_DISCOVERY_REFRESH"`

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

	// Removed lists the configuration keys this release no longer reads
	// and found set anyway, so the daemon can say so at start instead of
	// ignoring them in silence. Filled by [Load]; never decoded.
	Removed []string `yaml:"-"`
}

// removedKeys are the keys earlier releases read and this one does not,
// with what replaced them.
var removedKeys = map[string]string{
	// mqtt-smarthome 2.0 §3.2: persistent state MUST be retained, so the
	// retain flag is no longer an operator choice.
	"MQTT_RETAIN": "status items are always retained (mqtt-smarthome 2.0 §3.2)",
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

// QoSLevel is MQTT_QOS as a level. Unset (nil) is the shipped default;
// an explicit 0 stays 0.
func (c *Config) QoSLevel() int {
	if c.MQTTQoS == nil {
		return DefaultMQTTQoS
	}
	return *c.MQTTQoS
}

// MaintenanceEnabled reports whether the maintenance topics are on. Unset
// (nil) defaults to true, as mqtt-smarthome 2.0 §7 recommends.
func (c *Config) MaintenanceEnabled() bool {
	return c.MQTTMaintenance == nil || *c.MQTTMaintenance
}

// StatsIntervalSeconds is MQTT_STATS_INTERVAL in seconds. Unset is the
// shipped default; an explicit 0 stays 0, which switches the topic off.
func (c *Config) StatsIntervalSeconds() int {
	if c.MQTTStatsInterval == nil {
		return DefaultMQTTStatsInterval
	}
	return *c.MQTTStatsInterval
}

// RemovedKeyNote explains what replaced a key listed in [Config.Removed].
func RemovedKeyNote(key string) string { return removedKeys[key] }
