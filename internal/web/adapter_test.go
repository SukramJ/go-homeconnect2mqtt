// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/SukramJ/go-bridgeui"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/bridge"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

func ptr[T any](v T) *T { return &v }

// newAdapter is a dishwasher with a status, a power switch, a selectable
// program, a numeric option and a command.
func newAdapter(disp Dispatcher, mqttUp bool) *adapter {
	st := state.New()
	st.RegisterDevice("dw", "HA-1", "BOSCH", "Dishwasher", "SMV6", nil)
	st.SetConnectionState("dw", "connected", true)
	features := []state.Feature{
		{Feature: "BSH.Common.Status.DoorState", Value: "Closed", Platform: "sensor", Name: "Door state"},
		{
			Feature: "BSH.Common.Setting.PowerState", Value: "On", Writable: true, Platform: "select", Name: "Power state",
			Options: []string{"On", "Standby"},
		},
		{
			Feature: "BSH.Common.Root.SelectedProgram", Value: "Eco50", Writable: true, Platform: "select", Name: "Selected program",
			Options: []string{"Eco50", "Auto2"},
		},
		{
			Feature: "Dishcare.Dishwasher.Option.StartInRelative", Value: 0.0, Writable: true, Platform: "number", Name: "Start in",
			Unit: ptr("s"), Min: ptr(0.0), Max: ptr(86400.0), Step: ptr(60.0),
		},
		{Feature: "BSH.Common.Command.AbortProgram", Platform: "button", Name: "Abort program"},
		{Feature: "BSH.Common.Status.RemoteControlActive", Value: true, Platform: "binary_sensor"},
	}
	for i := range features {
		st.UpdateFeature("dw", features[i])
	}
	a := &adapter{store: st, dispatch: disp, version: "0.18.0", mqttUp: func() bool { return mqttUp }, cfg: &config.Config{MQTTServer: "broker"}}
	a.problems.OnAdd = a.notifier.Notify
	return a
}

func TestFeatureGroup(t *testing.T) {
	for in, want := range map[string]string{
		"BSH.Common.Status.DoorState":                "status",
		"BSH.Common.Setting.PowerState":              "setting",
		"Dishcare.Dishwasher.Option.StartInRelative": "option",
		"BSH.Common.Root.ActiveProgram":              "program",
		"Dishcare.Dishwasher.Program.Eco50":          "program",
		"BSH.Common.Event.ProgramFinished":           "event",
		"BSH.Common.Command.AbortProgram":            "command",
		"Vendor.Thing":                               "other",
	} {
		if got := featureGroup(in); got != want {
			t.Errorf("featureGroup(%q) = %q, want %q", in, got, want)
		}
	}
}

// Features become entries by kind; a command, which has no state, does not.
func TestSnapshotMapping(t *testing.T) {
	snap := newAdapter(stubDispatch{}, true).Snapshot()
	if len(snap.Devices) != 1 {
		t.Fatalf("devices = %+v", snap.Devices)
	}
	d := snap.Devices[0]
	if d.ID != "dw" || d.Title != "BOSCH Dishwasher" || d.Online == nil || !*d.Online || d.UpdatedAt.IsZero() {
		t.Errorf("device = %+v", d)
	}
	byKey := map[string]bridgeui.Entry{}
	for _, e := range d.Entries {
		byKey[e.Key] = e
	}
	if _, ok := byKey["BSH.Common.Command.AbortProgram"]; ok {
		t.Error("a command is shown as a value")
	}
	if e := byKey["Dishcare.Dishwasher.Option.StartInRelative"]; e.Group != "option" || e.Unit != "s" || e.Name != "Start in" {
		t.Errorf("option entry = %+v", e)
	}
	if e := byKey["BSH.Common.Status.RemoteControlActive"]; e.Name != "BSH.Common.Status.RemoteControlActive" {
		t.Errorf("unnamed feature = %+v", e)
	}
}

// Only what discovery makes controllable is an item; a command is a
// button writing what Home Assistant's does.
func TestItems(t *testing.T) {
	items := newAdapter(stubDispatch{}, true).Items()
	byKey := map[string]bridgeui.Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if len(items) != 4 {
		t.Fatalf("items = %+v", items)
	}
	if it := byKey["BSH.Common.Setting.PowerState"]; it.Component != "select" || len(it.Options) != 2 || it.Device != "dw" {
		t.Errorf("power = %+v", it)
	}
	if it := byKey["Dishcare.Dishwasher.Option.StartInRelative"]; it.Component != "number" || *it.Max != 86400 || *it.Step != 60 {
		t.Errorf("start in = %+v", it)
	}
	if it := byKey["BSH.Common.Command.AbortProgram"]; it.Component != "button" || it.Payload != "true" {
		t.Errorf("abort = %+v", it)
	}
	if _, ok := byKey["BSH.Common.Status.DoorState"]; ok {
		t.Error("a sensor is an item")
	}
}

// recordDispatch remembers the last write.
type recordDispatch struct {
	device, feature string
	value           any
	err             error
}

func (r *recordDispatch) Dispatch(_ context.Context, device, feature string, value any) error {
	r.device, r.feature, r.value = device, feature, value
	return r.err
}

func TestWriteClassesAndRecords(t *testing.T) {
	rec := &recordDispatch{}
	a := newAdapter(rec, true)
	if err := a.Write(t.Context(), "dw", "BSH.Common.Setting.PowerState", "Standby"); err != nil || rec.value != "Standby" {
		t.Fatalf("write: %v, %+v", err, rec)
	}
	for _, c := range []struct {
		err  error
		want error
	}{
		{bridge.ErrDeviceNotFound, bridgeui.ErrUnknownDevice},
		{bridge.ErrFeatureNotFound, bridgeui.ErrNotWritable},
		{bridge.ErrNotWritable, bridgeui.ErrNotWritable},
		{&homeconnect.CodeResponseError{Code: 541}, bridgeui.ErrNotReady},
	} {
		rec.err = c.err
		if err := a.Write(t.Context(), "dw", "x", "1"); !errors.Is(err, c.want) {
			t.Errorf("%v → %v, want %v", c.err, err, c.want)
		}
	}
	rec.err = &homeconnect.CodeResponseError{Code: 400}
	if err := a.Write(t.Context(), "dw", "x", "1"); errors.Is(err, bridgeui.ErrNotReady) || err == nil {
		t.Errorf("device error = %v", err)
	}
	if ps := a.Health().Problems; len(ps) != 5 || ps[0].Event != "web.write_failed" || ps[0].Device != "dw" {
		t.Errorf("problems = %+v", ps)
	}
}

// A store event and a recorded problem both reach the stream, coalesced;
// a released subscription goes quiet.
func TestChanges(t *testing.T) {
	a := newAdapter(stubDispatch{}, true)
	ch, cancel := a.Changes()
	a.store.SetConnectionState("dw", "offline", false)
	<-ch
	a.problems.Add(bridgeui.Problem{Event: "x"})
	<-ch
	cancel()
	cancel()
}

func TestHealthDegradesWithMQTT(t *testing.T) {
	if h := newAdapter(stubDispatch{}, true).Health(); h.Status != "ok" || h.Tiles[0].Text != "val.connected" {
		t.Errorf("up: %+v", h)
	}
	if h := newAdapter(stubDispatch{}, false).Health(); h.Status != "degraded" || h.Tiles[0].Tone != "err" {
		t.Errorf("down: %+v", h)
	}
}

// Every label the adapter and the layout use is in both bundles.
func TestLabelsTranslated(t *testing.T) {
	tr, err := bridgeui.LoadTranslations(i18nFS, "i18n")
	if err != nil {
		t.Fatal(err)
	}
	a := newAdapter(stubDispatch{}, true)
	var keys []string
	for _, tl := range append(a.Health().Tiles, a.Config()...) {
		keys = append(keys, tl.Label)
	}
	for _, s := range layout.Sections {
		keys = append(keys, s.Title, s.Nav)
		for _, g := range s.Groups {
			keys = append(keys, "group."+g)
		}
	}
	for _, g := range layout.SystemGroups {
		keys = append(keys, "group."+g)
	}
	for _, lang := range []string{"en", "de"} {
		for _, k := range keys {
			if _, ok := tr[lang][k]; !ok {
				t.Errorf("%s.json lacks %q", lang, k)
			}
		}
	}
}

// The device API's set route now takes JSON from the same origin only.
func TestSetRefusesCrossSite(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, true)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/devices/dw/set", strings.NewReader(`{"feature":"F","value":"On"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site set = %d, want 403", res.StatusCode)
	}
}
