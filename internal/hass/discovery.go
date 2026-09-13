// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/topic"
)

// Publisher is the subset of the MQTT client the discovery path needs.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, opts ...mqtt.PublishOption) error
}

// Enricher supplies operator-configured per-feature overrides (implemented by
// mapping.Catalog). Every lookup reports ok=false when nothing is configured,
// leaving the discovery heuristic in place.
type Enricher interface {
	LocalizedName(feature, lang string) (string, bool)
	DeviceClass(feature string) (string, bool)
	Unit(feature string) (string, bool)
	StateClass(feature string) (string, bool)
	EntityCategory(feature string) (string, bool)
	EnabledByDefault(feature string) (val, ok bool)
	Excluded(feature string) bool
}

// Discovery publishes Home Assistant MQTT discovery config payloads.
type Discovery struct {
	mqtt      Publisher
	baseTopic string // discovery prefix, e.g. "homeassistant"
	rootTopic string // bridge MQTT root, e.g. "homeconnect"
	qos       mqtt.QoS
	lang      string // display language for friendly names ("de"/"en")
	curated   bool   // publish only the enabled-by-default (primary) set
	logger    *slog.Logger
	enrich    Enricher
}

// SetEnricher installs an optional enrichment source.
func (d *Discovery) SetEnricher(e Enricher) { d.enrich = e }

// New builds a Discovery publisher. lang selects the friendly-name language;
// curated restricts discovery to the enabled-by-default (primary) entities.
func New(pub Publisher, baseTopic, rootTopic string, qos mqtt.QoS, lang string, curated bool, logger *slog.Logger) *Discovery {
	if logger == nil {
		logger = slog.Default()
	}
	return &Discovery{
		mqtt:      pub,
		baseTopic: strings.TrimRight(baseTopic, "/"),
		rootTopic: strings.TrimRight(rootTopic, "/"),
		qos:       qos,
		lang:      lang,
		curated:   curated,
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
	// bridge is the daemon's own status topic — the one the Last Will
	// writes, and the one every payload declares alongside the device
	// topic (F1).
	bridge string
}

type deviceBlock struct {
	idPrefix string
	block    map[string]any
}

func (d *Discovery) topicsFor(device string, e *homeconnect.Entity) entityTopics {
	dt := topic.NewDevice(d.rootTopic, device)
	return entityTopics{
		state:        dt.State(e.Name(), e.UID()),
		command:      dt.Command(e.Name(), e.UID()),
		availability: dt.Availability(),
		bridge:       topic.Bridge(d.rootTopic),
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

// applyEnrichment localizes the friendly name and applies catalogue overrides
// on top of the heuristic payload.
func (d *Discovery) applyEnrichment(e *homeconnect.Entity, payload map[string]any) {
	if d.enrich == nil || e.Name() == "" {
		return
	}
	f := e.Name()
	if name, ok := d.enrich.LocalizedName(f, d.lang); ok {
		payload["name"] = name
	}
	if dc, ok := d.enrich.DeviceClass(f); ok {
		payload["device_class"] = dc
	}
	if unit, ok := d.enrich.Unit(f); ok {
		payload["unit_of_measurement"] = unit
	}
	if sc, ok := d.enrich.StateClass(f); ok {
		payload["state_class"] = sc
	}
	if ec, ok := d.enrich.EntityCategory(f); ok {
		payload["entity_category"] = ec
	}
	if val, ok := d.enrich.EnabledByDefault(f); ok {
		if val {
			delete(payload, "enabled_by_default")
		} else {
			payload["enabled_by_default"] = false
		}
	}
}

// localizeOptions translates a select's enum options to the configured display
// language so HA dropdown labels match the (also localized) published state,
// and orders them in that language. Uncatalogued values pass through
// unchanged, keeping options and state aligned.
func (d *Discovery) localizeOptions(payload map[string]any) {
	opts, ok := payload["options"].([]string)
	if !ok {
		return
	}
	loc := make([]string, len(opts))
	for i, o := range opts {
		loc[i] = i18n.EnumLabel(o, d.lang)
	}
	// Sort AFTER translating, not before. enumOptions sorts the raw
	// enumeration member names, which are English, so a German dropdown
	// used to come out ordered by its English originals: OperationState
	// read ["Auto", "Aus", "Ein"], i.e. Auto/Off/On (F11). The user sees
	// only the left column.
	sortLocalized(loc)
	payload["options"] = loc
}

func (d *Discovery) configTopic(platform, device string, e *homeconnect.Entity) string {
	return d.baseTopic + "/" + platform + "/" + sanitize(device) + "/" + featureKey(e) + "/config"
}

// disabledByDefault reports whether the built payload ends up disabled.
func disabledByDefault(payload map[string]any) bool {
	v, ok := payload["enabled_by_default"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return !b
}

// PublishDevice emits a discovery config for every exposable entity of a
// device. Errors are logged, never fatal (publish what you can). It returns
// the set of config topics it published, so the caller can clear orphaned ones.
func (d *Discovery) PublishDevice(ctx context.Context, device string, info profile.DeviceInfo, entities []*homeconnect.Entity) map[string]bool {
	dev := d.deviceBlockFor(device, info)
	published := map[string]bool{}
	for _, e := range entities {
		if d.enrich != nil && e.Name() != "" && d.enrich.Excluded(e.Name()) {
			continue
		}
		platform, ok := classify(e)
		if !ok {
			continue
		}
		payload := payloadFor(e, platform, device, d.topicsFor(device, e), dev)
		d.applyEnrichment(e, payload)
		d.localizeOptions(payload)
		sanitizeForPlatform(payload, platform)
		// Curated mode: only publish the enabled-by-default (primary) entities.
		if d.curated && disabledByDefault(payload) {
			continue
		}
		b, err := json.Marshal(payload)
		if err != nil {
			d.logger.Warn("hass.marshal", slog.String("feature", e.Name()), slog.String("err", err.Error()))
			continue
		}
		topic := d.configTopic(platform, device, e)
		published[topic] = true
		if err := d.mqtt.Publish(ctx, topic, b, d.qos, true); err != nil {
			d.logger.Warn("hass.publish", slog.String("topic", topic), slog.String("err", err.Error()))
		}
	}
	d.publishProgramControls(ctx, device, entities, dev, published)
	return published
}

// publishProgramControls emits synthetic Start/Stop buttons for appliances that
// run programs. The appliances expose no start command of their own, so the user
// stages a program with the selected-program select and then presses Start
// (which posts the selected program to /ro/activeProgram); Stop aborts it.
//
// The payload shares basePayload and sanitizeForPlatform with the
// feature-derived entities (F6). Three things stay deliberately different,
// and none of them is drift:
//
//   - payload_press is Home Assistant's own "PRESS", not the "true" a
//     command feature writes: handleProgramControl recognises the topic
//     and never writes a value.
//   - No entity_category and no enabled_by_default, i.e. enabled and
//     prominent. These two ARE the primary program controls — the same
//     status isPrimary already gives the active/selected-program entities
//     they operate — so the curated set keeps them, as it must: a curated
//     install that could stage a program but not start it is not a
//     smaller feature set, it is a broken one.
//   - No enrichment and no exclusion. Both are keyed on a feature name and
//     these back no feature, so there is nothing for the operator
//     catalogue to match. Excluding them is not currently expressible;
//     that is a gap in the catalogue's vocabulary, not a divergence
//     between the two builders, and it is left as it is.
func (d *Discovery) publishProgramControls(ctx context.Context, device string, entities []*homeconnect.Entity, dev deviceBlock, published map[string]bool) {
	hasProgram := false
	for _, e := range entities {
		if e.Desc.Kind == profile.KindActiveProgram || e.Desc.Kind == profile.KindSelectedProgram {
			hasProgram = true
			break
		}
	}
	if !hasProgram {
		return
	}
	dt := topic.NewDevice(d.rootTopic, device)
	controls := []struct{ key, nameEN, nameDE string }{
		{topic.ControlStartProgram, "Start program", "Programm starten"},
		{topic.ControlStopProgram, "Stop program", "Programm stoppen"},
	}
	t := entityTopics{
		command:      "", // no feature behind it; the control topic is its own
		availability: dt.Availability(),
		bridge:       topic.Bridge(d.rootTopic),
	}
	for _, c := range controls {
		name := c.nameEN
		if d.lang == "de" {
			name = c.nameDE
		}
		// The same five common keys as every feature-derived entity, from
		// the same function, so a key added to one path cannot be silently
		// missing from the other (F6).
		payload := basePayload(platformButton, device, c.key, name, t, dev)
		payload["command_topic"] = dt.ControlCommand(c.key)
		payload["payload_press"] = controlPressPayload
		// Run the same last-pass platform filter the derived entities get.
		// It is a no-op on these two today; that is the point — it stays a
		// no-op by construction rather than by nobody having added a key.
		sanitizeForPlatform(payload, platformButton)
		b, err := json.Marshal(payload)
		if err != nil {
			d.logger.Warn("hass.marshal", slog.String("feature", c.key), slog.String("err", err.Error()))
			continue
		}
		topic := d.baseTopic + "/button/" + sanitize(device) + "/" + c.key + "/config"
		published[topic] = true
		if err := d.mqtt.Publish(ctx, topic, b, d.qos, true); err != nil {
			d.logger.Warn("hass.publish", slog.String("topic", topic), slog.String("err", err.Error()))
		}
	}
}

// DeviceConfigFilter is the MQTT filter matching a device's discovery config
// topics (homeassistant/+/<device>/+/config), for collecting retained configs
// to reconcile against the published set.
func (d *Discovery) DeviceConfigFilter(device string) string {
	return d.baseTopic + "/+/" + sanitize(device) + "/+/config"
}

// ConfigFilter matches every discovery config topic the daemon may own
// (homeassistant/+/+/+/config), for a global refresh that clears them all.
func (d *Discovery) ConfigFilter() string {
	return d.baseTopic + "/+/+/+/config"
}

// IsOwnConfig reports whether a retained HA discovery config payload was
// published by this daemon (its unique_id is in our `homeconnect_` namespace
// and its state topic is under our root), so orphan cleanup never touches
// configs owned by another integration or bridge instance.
func (d *Discovery) IsOwnConfig(payload []byte) bool {
	var cfg struct {
		UniqueID   string `json:"unique_id"`
		StateTopic string `json:"state_topic"`
	}
	if json.Unmarshal(payload, &cfg) != nil {
		return false
	}
	return strings.HasPrefix(cfg.UniqueID, "homeconnect_") &&
		(cfg.StateTopic == "" || strings.HasPrefix(cfg.StateTopic, d.rootTopic+"/"))
}
