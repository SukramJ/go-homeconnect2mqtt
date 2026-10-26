// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package web is the optional, opt-in diagnostics UI. The dashboard —
// server, hardening, SSE stream and single-page app — is go-bridgeui, the
// one every go-*2mqtt bridge embeds; this package presents the state store
// and the write dispatcher as go-bridgeui's contract, names the page's
// layout and labels, and keeps the device API of docs/08-web-api.md
// beside it. It is only started when WEB_ENABLE is set and never required
// for the core MQTT bridge.
package web

import (
	"context"
	"embed"
	"log/slog"
	"net/http"

	"github.com/SukramJ/go-bridgeui"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

// i18nFS holds this bridge's labels — sections, groups, status and
// configuration tiles — merged over go-bridgeui's built-in bundles.
//
//go:embed i18n
var i18nFS embed.FS

// Dispatcher applies a write command (implemented by *bridge.Bridge).
type Dispatcher interface {
	Dispatch(ctx context.Context, device, feature string, value any) error
}

// VersionInfo is the build identity GET /api/version reports.
type VersionInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Config parameterises the web server.
type Config struct {
	// Bind is the listen address "host:port".
	Bind string
	// User / Password enable HTTP Basic auth when either is non-empty.
	User     string
	Password string
	// App is the daemon's configuration, shown sanitised under System.
	App *config.Config
}

// layout is the Home Connect page: an appliance's state, its programs and
// options; its settings and commands under System.
var layout = bridgeui.Layout{
	Brand:   "homeconnect2mqtt",
	Product: "Home Connect",
	Icon:    "🏠",
	Sections: []bridgeui.Section{
		{ID: "state", Title: "section.state", Nav: "nav.state", Groups: []string{"status", "event"}},
		{ID: "programs", Title: "section.programs", Nav: "nav.programs", Groups: []string{"program", "option"}, Optional: true},
	},
	SystemGroups: []string{"setting", "command", "other"},
}

// New builds the dashboard with the device API beside it. It does not bind
// a socket — call Run on the result.
func New(cfg Config, store *state.Store, dispatch Dispatcher, version VersionInfo, mqttUp func() bool, logger *slog.Logger) *bridgeui.Server {
	if logger == nil {
		logger = slog.Default()
	}
	if mqttUp == nil {
		mqttUp = func() bool { return true }
	}
	app := cfg.App
	if app == nil {
		app = &config.Config{}
	}
	tr, err := bridgeui.LoadTranslations(i18nFS, "i18n")
	if err != nil {
		// The bundles are embedded and covered by a test.
		panic("web: " + err.Error())
	}
	a := &adapter{store: store, dispatch: dispatch, version: version.Version, mqttUp: mqttUp, cfg: app}
	a.problems.OnAdd = a.notifier.Notify
	r := &routes{store: store, dispatch: dispatch, version: version, logger: logger}
	return bridgeui.New(bridgeui.Options{
		Bind:         cfg.Bind,
		User:         cfg.User,
		Password:     cfg.Password,
		Realm:        "homeconnect2mqtt",
		Logger:       logger,
		Language:     app.Language,
		Layout:       layout,
		Translations: tr,
		// docs/08-web-api.md: /api/health is a readiness probe.
		HealthProbe: true,
		Routes:      r.register,
	}, a)
}

// routes is the device API of docs/08-web-api.md, served beside the
// dashboard's own.
type routes struct {
	store    *state.Store
	dispatch Dispatcher
	version  VersionInfo
	logger   *slog.Logger
}

// register adds the device API to the dashboard's mux. The one mutating
// route takes JSON from the same origin only.
func (s *routes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("GET /api/devices/{device}", s.handleDevice)
	mux.HandleFunc("GET /api/devices/{device}/features/{feature...}", s.handleFeature)
	mux.Handle("POST /api/devices/{device}/set", bridgeui.SameOrigin(http.HandlerFunc(s.handleSet)))
	mux.HandleFunc("GET /api/version", s.handleVersion)
}
