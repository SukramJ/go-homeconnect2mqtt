// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"fmt"
	"net"
	"strings"
)

// allowedLanguages is the whitelist for the display language.
var allowedLanguages = map[string]bool{"de": true, "en": true}

// ValidationError aggregates every problem found in a single pass so the
// operator sees all of them at once instead of fixing one per restart.
type ValidationError struct{ Issues []string }

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		return "config: " + e.Issues[0]
	}
	return fmt.Sprintf("config: %d validation issue(s):\n  - %s",
		len(e.Issues), strings.Join(e.Issues, "\n  - "))
}

// Validate checks the post-defaults config and returns an aggregated
// *ValidationError, or nil when the config is usable.
func Validate(c *Config) error {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	rangeCheck := func(name string, v, lo, hi int) {
		if v < lo || v > hi {
			add("%s must be %d..%d, got %d", name, lo, hi, v)
		}
	}

	if c.MQTTServer == "" {
		add("MQTT_SERVER is required")
	}
	if c.MQTTTopic == "" {
		add("MQTT_TOPIC is required")
	}
	rangeCheck("MQTT_QOS", c.MQTTQoS, 0, 1)
	// Both-or-neither for MQTT credentials.
	if (c.MQTTLogin == "") != (c.MQTTPassword == "") {
		add("MQTT_LOGIN and MQTT_PASSWORD must both be set or both empty")
	}

	if c.HASSEnable && c.HASSBaseTopic == "" {
		add("HASS_BASE_TOPIC is required when HASS_ENABLE is true")
	}
	rangeCheck("HASS_BIRTH_GRACETIME", c.HASSBirthGracetime, 0, 600)

	rangeCheck("RECONNECT_INITIAL", c.ReconnectInitial, 1, 3600)
	rangeCheck("RECONNECT_MAX", c.ReconnectMax, 1, 3600)
	if c.ReconnectMax < c.ReconnectInitial {
		add("RECONNECT_MAX (%d) must be >= RECONNECT_INITIAL (%d)", c.ReconnectMax, c.ReconnectInitial)
	}
	rangeCheck("RECONNECT_JITTER", c.ReconnectJitter, 0, 60000)
	rangeCheck("HANDSHAKE_TIMEOUT", c.HandshakeTimeout, 1, 600)
	rangeCheck("SEND_TIMEOUT", c.SendTimeout, 1, 600)
	rangeCheck("HEARTBEAT", c.Heartbeat, 1, 600)

	if c.WebEnable {
		if _, _, err := net.SplitHostPort(c.WebBind); err != nil {
			add("WEB_BIND must be host:port, got %q", c.WebBind)
		}
		if (c.WebUser == "") != (c.WebPassword == "") {
			add("WEB_USER and WEB_PASSWORD must both be set or both empty")
		}
	}

	if !allowedLanguages[c.Language] {
		add("LANGUAGE must be one of de, en; got %q", c.Language)
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
