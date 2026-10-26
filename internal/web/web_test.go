// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/bridge"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

type stubDispatch struct{ err error }

func (s stubDispatch) Dispatch(context.Context, string, string, any) error { return s.err }

func newTestServer(t *testing.T, disp Dispatcher, mqttUp bool) (ts *httptest.Server, store *state.Store) {
	t.Helper()
	store = state.New()
	store.RegisterDevice("dw", "HA-1", "BOSCH", "Dishwasher", "SMV6", map[string]any{"brand": "BOSCH"})
	store.SetConnectionState("dw", "connected", true)
	store.UpdateFeature("dw", state.Feature{Feature: "BSH.Common.Status.OperationState", UID: 1, Value: "Run", Access: "read"})
	srv := New(Config{}, store, disp, VersionInfo{Version: "0.1.0", Commit: "abc"}, func() bool { return mqttUp }, nil)
	ts = httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func getJSON(t *testing.T, url string) (status int, body map[string]any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx,gosec // test
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// /api/status is go-bridgeui's: the health with this bridge's tiles, and
// every appliance as a device with its features as entries.
func TestStatusEndpoint(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, true)
	code, m := getJSON(t, ts.URL+"/api/status")
	if code != http.StatusOK {
		t.Fatalf("status code = %d", code)
	}
	health, _ := m["health"].(map[string]any)
	if health["version"] != "0.1.0" {
		t.Errorf("version = %v", health["version"])
	}
	snap, _ := m["snapshot"].(map[string]any)
	devs, _ := snap["devices"].([]any)
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %v", snap["devices"])
	}
	dev, _ := devs[0].(map[string]any)
	if dev["id"] != "dw" || dev["title"] != "BOSCH Dishwasher" {
		t.Errorf("device = %v", dev)
	}
}

func TestHealthOK(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, true)
	code, m := getJSON(t, ts.URL+"/api/health")
	if code != 200 || m["status"] != "ok" {
		t.Errorf("health = %d %v", code, m["status"])
	}
}

func TestHealthDegradedWhenMQTTDown(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, false)
	code, m := getJSON(t, ts.URL+"/api/health")
	if code != 503 || m["status"] != "degraded" {
		t.Errorf("health = %d %v, want 503 degraded", code, m["status"])
	}
}

func TestDeviceEndpoints(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, true)
	if code, _ := getJSON(t, ts.URL+"/api/devices/dw"); code != http.StatusOK {
		t.Errorf("device = %d", code)
	}
	if code, _ := getJSON(t, ts.URL+"/api/devices/nope"); code != 404 {
		t.Errorf("unknown device = %d, want 404", code)
	}
	if code, _ := getJSON(t, ts.URL+"/api/devices/dw/features/BSH.Common.Status.OperationState"); code != http.StatusOK {
		t.Errorf("feature = %d", code)
	}
	if code, _ := getJSON(t, ts.URL+"/api/devices/dw/features/Nope"); code != 404 {
		t.Errorf("unknown feature = %d, want 404", code)
	}
}

func TestSetDispatch(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		key  string
	}{
		{"ok", nil, 202, ""},
		{"device", bridge.ErrDeviceNotFound, 404, "device_not_found"},
		{"feature", bridge.ErrFeatureNotFound, 404, "feature_not_found"},
		{"notwritable", bridge.ErrNotWritable, 403, "not_writable"},
		{"window", &homeconnect.CodeResponseError{Code: 541}, 409, "write_window_closed"},
		{"deverr", &homeconnect.CodeResponseError{Code: 400}, 502, "device_error"},
	}
	for _, tc := range cases {
		ts, _ := newTestServer(t, stubDispatch{err: tc.err}, true)
		body := strings.NewReader(`{"feature":"BSH.Common.Setting.PowerState","value":"On"}`)
		resp, err := http.Post(ts.URL+"/api/devices/dw/set", "application/json", body) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("%s: POST: %v", tc.name, err)
		}
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
		if tc.key != "" && m["error"] != tc.key {
			t.Errorf("%s: error key = %v, want %q", tc.name, m["error"], tc.key)
		}
	}
}

func TestSetBadRequest(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, true)
	resp, _ := http.Post(ts.URL+"/api/devices/dw/set", "application/json", strings.NewReader("not json")) //nolint:noctx // test client
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestSetBodySizeLimit exercises the request-body cap on the set endpoint:
// an oversized body is rejected with 413, a small valid body still works.
func TestSetBodySizeLimit(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    int
		wantKey string
	}{
		{
			name: "small valid body accepted",
			body: `{"feature":"BSH.Common.Setting.PowerState","value":"On"}`,
			want: http.StatusAccepted,
		},
		{
			name:    "oversized body rejected",
			body:    `{"feature":"F","value":"` + strings.Repeat("x", maxSetBodyBytes+1024) + `"}`,
			want:    http.StatusRequestEntityTooLarge,
			wantKey: "payload_too_large",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newTestServer(t, stubDispatch{}, true)
			resp, err := http.Post(ts.URL+"/api/devices/dw/set", "application/json", strings.NewReader(tc.body)) //nolint:noctx // test client
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			var m map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&m)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.wantKey != "" && m["error"] != tc.wantKey {
				t.Errorf("error key = %v, want %q", m["error"], tc.wantKey)
			}
		})
	}
}

// TestRunListenError checks that Run wraps and returns a listen failure.
func TestRunListenError(t *testing.T) {
	store := state.New()
	srv := New(Config{Bind: "127.0.0.1:-1"}, store, stubDispatch{}, VersionInfo{}, nil, slog.New(slog.DiscardHandler))
	if err := srv.Run(context.Background()); err == nil {
		t.Fatal("Run with invalid bind should return an error")
	}
}

func TestBasicAuth(t *testing.T) {
	st := state.New()
	srv := New(Config{User: "u", Password: "p"}, st, stubDispatch{}, VersionInfo{}, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// No credentials -> 401.
	resp, _ := http.Get(ts.URL + "/api/status") //nolint:noctx // test client
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Correct credentials -> 200.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", http.NoBody) //nolint:noctx // test client
	req.SetBasicAuth("u", "p")
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("auth status = %d, want 200", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
}

func TestVersionEndpoint(t *testing.T) {
	ts, _ := newTestServer(t, stubDispatch{}, true)
	code, m := getJSON(t, ts.URL+"/api/version")
	if code != 200 || m["version"] != "0.1.0" {
		t.Errorf("version = %d %v", code, m["version"])
	}
}
