// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

// Default values applied to zero-valued fields after YAML+env merge.
// Mandatory fields (e.g. MQTT_SERVER) are intentionally left zero so
// Validate can report them.
const (
	DefaultMQTTTopic          = "homeconnect"
	DefaultMQTTQoS            = 1
	DefaultHASSBaseTopic      = "homeassistant"
	DefaultHASSBirthGracetime = 15
	DefaultHASSDiscovery      = "full"
	DefaultAppName            = "go-homeconnect2mqtt"
	DefaultReconnectInitial   = 1
	DefaultReconnectMax       = 30
	DefaultReconnectJitter    = 500
	DefaultHandshakeTimeout   = 60
	DefaultSendTimeout        = 20
	DefaultHeartbeat          = 20
	DefaultWebBind            = "127.0.0.1:8080"
	DefaultLanguage           = "de"
)

// applyDefaults fills zero-valued, non-mandatory fields with sane
// defaults. MQTT_RETAIN defaults to true via a pointer sentinel because a
// false bool is indistinguishable from "unset".
func applyDefaults(c *Config) {
	if c.MQTTTopic == "" {
		c.MQTTTopic = DefaultMQTTTopic
	}
	if c.MQTTRetain == nil {
		v := true
		c.MQTTRetain = &v
	}
	if c.MQTTQoS == 0 {
		c.MQTTQoS = DefaultMQTTQoS
	}
	if c.HASSBaseTopic == "" {
		c.HASSBaseTopic = DefaultHASSBaseTopic
	}
	if c.HASSBirthGracetime == 0 {
		c.HASSBirthGracetime = DefaultHASSBirthGracetime
	}
	if c.HASSDiscovery == "" {
		c.HASSDiscovery = DefaultHASSDiscovery
	}
	if c.AppName == "" {
		c.AppName = DefaultAppName
	}
	if c.ReconnectInitial == 0 {
		c.ReconnectInitial = DefaultReconnectInitial
	}
	if c.ReconnectMax == 0 {
		c.ReconnectMax = DefaultReconnectMax
	}
	if c.ReconnectJitter == 0 {
		c.ReconnectJitter = DefaultReconnectJitter
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.Heartbeat == 0 {
		c.Heartbeat = DefaultHeartbeat
	}
	if c.WebBind == "" {
		c.WebBind = DefaultWebBind
	}
	if c.Language == "" {
		c.Language = DefaultLanguage
	}
}
