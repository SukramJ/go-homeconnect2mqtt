// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/state"
)

// sseHeartbeat is the comment-line interval that keeps proxies from
// dropping an idle SSE stream (docs/09 §4).
const sseHeartbeat = 20 * time.Second

// handleSSE streams live events: a snapshot first, then value/connection/
// health updates. An optional ?device= filters to one device.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, "internal", http.StatusInternalServerError, "streaming unsupported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	deviceFilter := r.URL.Query().Get("device")

	ch, cancel := s.store.Subscribe()
	defer cancel()

	_, _ = fmt.Fprint(w, "retry: 5000\n\n")
	s.writeEvent(w, state.EventSnapshot, map[string]any{
		"devices": s.store.Snapshot().Devices,
		"mqtt":    map[string]any{"connected": s.mqttUp()},
	})
	flusher.Flush()

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ":\n\n") // heartbeat comment
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if deviceFilter != "" && !eventMatchesDevice(ev, deviceFilter) {
				continue
			}
			s.writeEvent(w, ev.Type, ev.Data)
			flusher.Flush()
		}
	}
}

func (s *Server) writeEvent(w http.ResponseWriter, typ state.EventType, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
}

// eventMatchesDevice reports whether an event concerns the named device.
func eventMatchesDevice(ev state.Event, device string) bool {
	m, ok := ev.Data.(map[string]any)
	if !ok {
		return true // snapshot/health are global
	}
	d, ok := m["device"].(string)
	if !ok {
		return true
	}
	return d == device
}
