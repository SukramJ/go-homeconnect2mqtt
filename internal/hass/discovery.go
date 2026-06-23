// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/mqtt"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// Publisher is the subset of the MQTT client the discovery path needs.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool) error
}

// Enricher supplies operator-configured device_class/unit overrides per
// feature (implemented by mapping.Catalog).
type Enricher interface {
	DeviceClass(feature string) (string, bool)
	Unit(feature string) (string, bool)
}

// Discovery publishes Home Assistant MQTT discovery config payloads.
type Discovery struct {
	mqtt      Publisher
	baseTopic string // discovery prefix, e.g. "homeassistant"
	rootTopic string // bridge MQTT root, e.g. "homeconnect"
	qos       mqtt.QoS
	logger    *slog.Logger
	enrich    Enricher
}

// SetEnricher installs an optional enrichment source consulted for
// device_class/unit overrides.
func (d *Discovery) SetEnricher(e Enricher) { d.enrich = e }

// New builds a Discovery publisher.
func New(pub Publisher, baseTopic, rootTopic string, qos mqtt.QoS, logger *slog.Logger) *Discovery {
	if logger == nil {
		logger = slog.Default()
	}
	return &Discovery{
		mqtt:      pub,
		baseTopic: strings.TrimRight(baseTopic, "/"),
		rootTopic: strings.TrimRight(rootTopic, "/"),
		qos:       qos,
		logger:    logger,
	}
}

// BirthTopic is the Home Assistant status topic to watch; a payload of
// "online" means HA (re)started and discovery must be re-published.
func (d *Discovery) BirthTopic() string { return d.baseTopic + "/status" }

type entityTopics struct {
	state        string
	command      string
	availability string
}

type deviceBlock struct {
	idPrefix string
	block    map[string]any
}

// featurePath mirrors the bridge topic layout: dotted name -> slash path,
// unnamed -> _uid/<n>.
func featurePath(e *homeconnect.Entity) string {
	if e.Name() == "" {
		return "_uid/" + strconv.Itoa(e.UID())
	}
	return strings.ReplaceAll(e.Name(), ".", "/")
}

func (d *Discovery) topicsFor(device string, e *homeconnect.Entity) entityTopics {
	base := d.rootTopic + "/" + device
	fp := featurePath(e)
	return entityTopics{
		state:        base + "/" + fp + "/state",
		command:      base + "/" + fp + "/set",
		availability: base + "/availability",
	}
}

func (d *Discovery) deviceBlockFor(device string, info profile.DeviceInfo) deviceBlock {
	id := "homeconnect_" + sanitize(device)
	model := info.Model
	if model == "" {
		model = info.Type
	}
	return deviceBlock{
		idPrefix: id,
		block: map[string]any{
			"identifiers":  []string{id},
			"manufacturer": info.Brand,
			"model":        model,
			"name":         device,
		},
	}
}

// applyEnrichment overrides derived device_class/unit with operator values.
func (d *Discovery) applyEnrichment(e *homeconnect.Entity, payload map[string]any) {
	if d.enrich == nil || e.Name() == "" {
		return
	}
	if dc, ok := d.enrich.DeviceClass(e.Name()); ok {
		payload["device_class"] = dc
	}
	if unit, ok := d.enrich.Unit(e.Name()); ok {
		payload["unit_of_measurement"] = unit
	}
}

func (d *Discovery) configTopic(platform, device string, e *homeconnect.Entity) string {
	return d.baseTopic + "/" + platform + "/" + sanitize(device) + "/" + featureKey(e) + "/config"
}

// PublishDevice emits a discovery config for every exposable entity of a
// device. Errors are logged, never fatal (publish what you can).
func (d *Discovery) PublishDevice(ctx context.Context, device string, info profile.DeviceInfo, entities []*homeconnect.Entity) {
	dev := d.deviceBlockFor(device, info)
	for _, e := range entities {
		platform, ok := classify(e)
		if !ok {
			continue
		}
		payload := payloadFor(e, platform, d.topicsFor(device, e), dev)
		d.applyEnrichment(e, payload)
		b, err := json.Marshal(payload)
		if err != nil {
			d.logger.Warn("hass.marshal", slog.String("feature", e.Name()), slog.String("err", err.Error()))
			continue
		}
		topic := d.configTopic(platform, device, e)
		if err := d.mqtt.Publish(ctx, topic, b, d.qos, true); err != nil {
			d.logger.Warn("hass.publish", slog.String("topic", topic), slog.String("err", err.Error()))
		}
	}
}
