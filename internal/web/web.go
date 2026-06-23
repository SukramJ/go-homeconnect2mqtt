// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package web is the optional, opt-in diagnostics/health UI: a small HTTP
// API + SSE stream plus an embedded SPA. It is only started when
// WEB_ENABLE is set and never required for the core MQTT bridge
// (docs/09-web-api.md).
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

//go:embed static
var staticFS embed.FS

// Dispatcher applies a write command (implemented by *bridge.Bridge).
type Dispatcher interface {
	Dispatch(ctx context.Context, device, feature string, value any) error
}

// VersionInfo is the build metadata shown by the API.
type VersionInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Config configures the web server.
type Config struct {
	Bind     string
	User     string
	Password string
}

// Server is the HTTP server for the diagnostics UI.
type Server struct {
	cfg      Config
	store    *state.Store
	dispatch Dispatcher
	version  VersionInfo
	logger   *slog.Logger
	mqttUp   func() bool

	handler http.Handler
}

// New builds the web server. mqttUp may be nil (then MQTT is reported up).
func New(cfg Config, store *state.Store, dispatch Dispatcher, version VersionInfo, mqttUp func() bool, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if mqttUp == nil {
		mqttUp = func() bool { return true }
	}
	s := &Server{cfg: cfg, store: store, dispatch: dispatch, version: version, logger: logger, mqttUp: mqttUp}
	s.handler = s.buildHandler()
	return s
}

// Handler exposes the configured handler (for tests via httptest).
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("GET /api/devices/{device}", s.handleDevice)
	mux.HandleFunc("GET /api/devices/{device}/features/{feature...}", s.handleFeature)
	mux.HandleFunc("POST /api/devices/{device}/set", s.handleSet)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("GET /api/version", s.handleVersion)

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	return s.withBasicAuth(mux)
}

// Run serves until the context is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Bind,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("web.listening", slog.String("bind", s.cfg.Bind))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// withBasicAuth enforces HTTP basic auth when both user and password are
// configured; otherwise it passes through (docs/09 §1).
func (s *Server) withBasicAuth(next http.Handler) http.Handler {
	if s.cfg.User == "" || s.cfg.Password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.User)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="go-homeconnect2mqtt"`)
			writeError(w, "unauthorized", http.StatusUnauthorized, "authentication required", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
