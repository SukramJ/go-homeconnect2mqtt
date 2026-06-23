// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"log/slog"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/bridge"
)

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard error object (docs/09 §2.3).
func writeError(w http.ResponseWriter, key string, status int, msg string, deviceCode *int) {
	writeJSON(w, status, map[string]any{
		"error": key, "message": msg, "code": status, "device_code": deviceCode,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.version.Version,
		"commit":         s.version.Commit,
		"build_date":     s.version.BuildDate,
		"started_at":     s.store.StartedAt().UTC().Format(time.RFC3339),
		"uptime_seconds": int64(time.Since(s.store.StartedAt()).Seconds()),
		"mqtt":           map[string]any{"connected": s.mqttUp()},
		"devices":        snap.Devices,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	h := s.store.Health()
	status := http.StatusOK
	if h.Status != "ok" || !s.mqttUp() {
		status = http.StatusServiceUnavailable
		h.Status = "degraded"
	}
	writeJSON(w, status, map[string]any{
		"status":  h.Status,
		"mqtt":    map[string]any{"connected": s.mqttUp()},
		"devices": h.Devices,
	})
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot())
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.store.Device(r.PathValue("device"))
	if !ok {
		writeError(w, "device_not_found", http.StatusNotFound, "unknown device", nil)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleFeature(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.store.Device(r.PathValue("device"))
	if !ok {
		writeError(w, "device_not_found", http.StatusNotFound, "unknown device", nil)
		return
	}
	feature := r.PathValue("feature")
	for _, f := range detail.Features {
		if f.Feature == feature {
			writeJSON(w, http.StatusOK, f)
			return
		}
	}
	writeError(w, "feature_not_found", http.StatusNotFound, "unknown feature", nil)
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.version.Version, "commit": s.version.Commit, "build_date": s.version.BuildDate,
	})
}

type setRequest struct {
	Feature string `json:"feature"`
	Value   any    `json:"value"`
}

func (s *Server) handleSet(w http.ResponseWriter, r *http.Request) {
	device := r.PathValue("device")
	var req setRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "bad_request", http.StatusBadRequest, "invalid JSON body", nil)
		return
	}
	if req.Feature == "" {
		writeError(w, "bad_request", http.StatusBadRequest, "feature is required", nil)
		return
	}
	err := s.dispatch.Dispatch(r.Context(), device, req.Feature, req.Value)
	if err == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted": true, "device": device, "feature": req.Feature, "value": req.Value,
		})
		return
	}
	s.mapDispatchError(w, err)
}

// mapDispatchError translates a bridge dispatch error to the HTTP taxonomy
// (docs/09 §5).
func (s *Server) mapDispatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bridge.ErrDeviceNotFound):
		writeError(w, "device_not_found", http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, bridge.ErrFeatureNotFound):
		writeError(w, "feature_not_found", http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, bridge.ErrNotWritable):
		writeError(w, "not_writable", http.StatusForbidden, err.Error(), nil)
	default:
		if code, ok := bridge.DeviceErrorCode(err); ok {
			c := code
			if code == 541 {
				writeError(w, "write_window_closed", http.StatusConflict, err.Error(), &c)
				return
			}
			writeError(w, "device_error", http.StatusBadGateway, err.Error(), &c)
			return
		}
		s.logger.Warn("web.dispatch", slog.String("err", err.Error()))
		writeError(w, "internal", http.StatusInternalServerError, err.Error(), nil)
	}
}
