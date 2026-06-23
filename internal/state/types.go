// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package state is the optional in-memory cache behind the diagnostics web
// UI (docs/09-web-api.md). It is only instantiated when WEB_ENABLE is set,
// so the core MQTT bridge carries no overhead otherwise.
package state

import "time"

// Feature is the canonical representation of one device feature, shared by
// the REST API and SSE value events (docs/09 §2.1).
type Feature struct {
	Feature      string   `json:"feature"`
	Topic        string   `json:"topic"`
	UID          int      `json:"uid"`
	Value        any      `json:"value"`
	ValueRaw     any      `json:"value_raw"`
	ProtocolType string   `json:"protocol_type"`
	ContentType  string   `json:"content_type"`
	Access       string   `json:"access"`
	Available    bool     `json:"available"`
	Writable     bool     `json:"writable"`
	Unit         *string  `json:"unit"`
	DeviceClass  *string  `json:"device_class"`
	Options      []string `json:"options"`
	Min          *float64 `json:"min"`
	Max          *float64 `json:"max"`
	Step         *float64 `json:"step"`
	UpdatedAt    string   `json:"updated_at"`
}

// DeviceSummary is the per-device header (docs/09 §2.2).
type DeviceSummary struct {
	Name            string `json:"name"`
	HaID            string `json:"haId"`
	Brand           string `json:"brand"`
	Type            string `json:"type"`
	Vib             string `json:"vib"`
	ConnectionState string `json:"connection_state"`
	Available       bool   `json:"available"`
	UpdatedAt       string `json:"updated_at"`
	AgeSeconds      int64  `json:"age_seconds"`
	FeatureCount    int    `json:"feature_count"`
}

// DeviceDetail is a device plus all its features (docs/09 §3).
type DeviceDetail struct {
	Device   DeviceSummary  `json:"device"`
	Info     map[string]any `json:"info"`
	Features []Feature      `json:"features"`
}

// Snapshot is a consistent copy of the whole store for the API/SSE.
type Snapshot struct {
	Devices []DeviceSummary `json:"devices"`
}

// HealthDevice is the compact per-device health entry (docs/09 §3 health).
type HealthDevice struct {
	Name            string `json:"name"`
	ConnectionState string `json:"connection_state"`
	AgeSeconds      int64  `json:"age_seconds"`
	Stale           bool   `json:"stale"`
}

// Health is the daemon health report.
type Health struct {
	Status  string         `json:"status"`
	Devices []HealthDevice `json:"devices"`
}

// EventType enumerates the SSE event kinds (docs/09 §4).
type EventType string

// SSE event types.
const (
	EventSnapshot   EventType = "snapshot"
	EventValue      EventType = "value"
	EventConnection EventType = "connection"
	EventHealth     EventType = "health"
)

// Event is a single SSE event.
type Event struct {
	Type EventType
	Data any
}

// formatTime renders a timestamp as RFC3339 UTC, or "" for the zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
