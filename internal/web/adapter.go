// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SukramJ/go-bridgeui"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/bridge"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

// adapter presents the state store and the dispatcher as go-bridgeui's
// contract: every appliance is a device, every feature an entry, every
// feature discovery makes controllable an item.
type adapter struct {
	store    *state.Store
	dispatch Dispatcher
	version  string
	mqttUp   func() bool
	cfg      *config.Config

	notifier bridgeui.Notifier
	problems bridgeui.ProblemLog
}

// controls are the platforms the UI can write, as Home Assistant can.
var controls = map[string]bool{"button": true, "select": true, "switch": true, "number": true}

// commandPress is what a command button writes, as Home Assistant's does.
const commandPress = "true"

func (a *adapter) Snapshot() bridgeui.Snapshot {
	snap := a.store.Snapshot()
	out := bridgeui.Snapshot{Devices: make([]bridgeui.Device, 0, len(snap.Devices))}
	for i := range snap.Devices {
		sum := &snap.Devices[i]
		detail, ok := a.store.Device(sum.Name)
		if !ok {
			continue
		}
		online := sum.Available
		dev := bridgeui.Device{
			ID:        sum.Name,
			Title:     strings.TrimSpace(sum.Brand + " " + sum.Type),
			Subtitle:  subtitle(sum),
			Online:    &online,
			UpdatedAt: parseTime(sum.UpdatedAt),
			Entries:   make([]bridgeui.Entry, 0, len(detail.Features)),
		}
		if dev.Title == "" {
			dev.Title = sum.Name
		}
		for i := range detail.Features {
			f := &detail.Features[i]
			if f.Platform == "button" {
				continue // a command has no state to show
			}
			dev.Entries = append(dev.Entries, bridgeui.Entry{
				Group: featureGroup(f.Feature), Key: f.Feature, Name: firstNonEmpty(f.Name, f.Feature),
				Unit: deref(f.Unit), Value: f.Value,
			})
		}
		out.Devices = append(out.Devices, dev)
	}
	return out
}

// featureGroup is the kind a Home Connect feature name carries in its
// path — "BSH.Common.Status.DoorState" is a status — lowercased; the root
// program nodes count as programs.
func featureGroup(feature string) string {
	for _, seg := range strings.Split(feature, ".") {
		switch seg {
		case "Status", "Setting", "Option", "Event", "Command", "Program":
			return strings.ToLower(seg)
		case "Root":
			return "program"
		}
	}
	return "other"
}

func subtitle(sum *state.DeviceSummary) string {
	var parts []string
	if sum.Vib != "" {
		parts = append(parts, sum.Vib)
	}
	if sum.HaID != "" {
		parts = append(parts, "haId "+sum.HaID)
	}
	if sum.ConnectionState != "" {
		parts = append(parts, sum.ConnectionState)
	}
	return strings.Join(parts, "  ·  ")
}

func (a *adapter) Health() bridgeui.Health {
	h := a.store.Health()
	mqttUp := a.mqttUp()
	status := h.Status
	if !mqttUp {
		status = "degraded"
	}
	stale := 0
	for _, d := range h.Devices {
		if d.Stale {
			stale++
		}
	}
	mqtt := bridgeui.Tile{Label: "health.mqtt", Text: "val.disconnected", Tone: "err", Sub: a.cfg.MQTTServer}
	if mqttUp {
		mqtt.Text, mqtt.Tone = "val.connected", "ok"
	}
	staleTile := bridgeui.Tile{Label: "health.stale", Value: stale}
	if stale > 0 {
		staleTile.Tone = "warn"
	}
	hass := bridgeui.Tile{Label: "health.hass", Text: "val.off"}
	if a.cfg.HASSEnable {
		hass.Text, hass.Tone = "val.active", "ok"
	}
	return bridgeui.Health{
		Status:    status,
		Version:   a.version,
		StartedAt: a.store.StartedAt(),
		Tiles: []bridgeui.Tile{
			mqtt,
			{Label: "health.topic", Value: firstNonEmpty(a.cfg.MQTTTopic, "—")},
			hass,
			{Label: "health.devices", Value: len(h.Devices)},
			staleTile,
		},
		Problems: a.problems.List(),
	}
}

func (a *adapter) Config() []bridgeui.Tile {
	c := a.cfg
	onOff := func(label string, on bool, text string) bridgeui.Tile {
		if on {
			return bridgeui.Tile{Label: label, Text: text}
		}
		return bridgeui.Tile{Label: label, Text: "val.off"}
	}
	hass := onOff("cfg.hass", c.HASSEnable, "val.active")
	if c.HASSEnable {
		hass.Sub = c.HASSBaseTopic + " · " + c.HASSDiscovery
	}
	auth := bridgeui.Tile{Label: "cfg.webAuth", Text: "val.on"}
	if c.WebUser == "" && c.WebPassword == "" {
		auth = bridgeui.Tile{Label: "cfg.webAuth", Text: "val.off", Tone: "warn"}
	}
	return []bridgeui.Tile{
		{Label: "cfg.mqtt", Value: firstNonEmpty(c.MQTTServer, "—"), Sub: c.MQTTTopic},
		hass,
		{Label: "cfg.reconnect", Value: strconv.Itoa(c.ReconnectInitial) + "–" + strconv.Itoa(c.ReconnectMax) + " s"},
		{Label: "cfg.heartbeat", Value: strconv.Itoa(c.Heartbeat) + " s"},
		auth,
		{Label: "cfg.language", Value: firstNonEmpty(c.Language, "de")},
		onOff("cfg.debug", c.Debug, "val.on"),
	}
}

// Items lists, per appliance, the features discovery makes controllable.
func (a *adapter) Items() []bridgeui.Item {
	var out []bridgeui.Item
	devices := a.store.Snapshot().Devices
	for i := range devices {
		sum := &devices[i]
		detail, ok := a.store.Device(sum.Name)
		if !ok {
			continue
		}
		for i := range detail.Features {
			f := &detail.Features[i]
			if !controls[f.Platform] || (f.Platform != "button" && !f.Writable) {
				continue
			}
			it := bridgeui.Item{
				Key: f.Feature, Device: sum.Name, Name: firstNonEmpty(f.Name, f.Feature),
				Unit: deref(f.Unit), Component: f.Platform, Min: f.Min, Max: f.Max, Step: f.Step,
			}
			if f.Platform == "button" {
				it.Payload = commandPress
			}
			for _, o := range f.Options {
				it.Options = append(it.Options, bridgeui.Option{Value: o, Label: o})
			}
			out = append(out, it)
		}
	}
	return out
}

// Write dispatches through the same path as an MQTT `set`, classes the
// dispatcher's errors for go-bridgeui and records a failure.
func (a *adapter) Write(ctx context.Context, device, key, value string) error {
	err := a.dispatch.Dispatch(ctx, device, key, value)
	if err == nil {
		return nil
	}
	a.problems.Add(bridgeui.Problem{Device: device, Event: "web.write_failed", Detail: key + " = " + value + ": " + err.Error()})
	switch {
	case errors.Is(err, bridge.ErrDeviceNotFound):
		return fmt.Errorf("%w: %w", bridgeui.ErrUnknownDevice, err)
	case errors.Is(err, bridge.ErrFeatureNotFound), errors.Is(err, bridge.ErrNotWritable):
		return fmt.Errorf("%w: %w", bridgeui.ErrNotWritable, err)
	}
	if code, ok := bridge.DeviceErrorCode(err); ok && code == 541 {
		// The appliance's write window is closed (door open, remote
		// control off): retry later.
		return fmt.Errorf("%w: %w", bridgeui.ErrNotReady, err)
	}
	return err
}

// Changes turns the store's typed events and the problem log into one
// coalesced signal.
func (a *adapter) Changes() (events <-chan struct{}, cancel func()) {
	evs, unsubscribe := a.store.Subscribe()
	probs, release := a.notifier.Subscribe()
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	signal := func() {
		select {
		case out <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case <-probs:
				signal()
			case _, ok := <-evs:
				if !ok {
					return
				}
				signal()
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			close(done)
			unsubscribe()
			release()
		})
	}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
