// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package state

import (
	"sort"
	"sync"
	"time"
)

// DefaultStaleThreshold is the age beyond which a device counts as stale.
const DefaultStaleThreshold = 120 * time.Second

// deviceState is the mutable per-device record.
type deviceState struct {
	summary   DeviceSummary
	info      map[string]any
	features  map[string]Feature // keyed by feature name (or uid string)
	updatedAt time.Time
}

// Store is the thread-safe in-memory cache feeding the web UI.
type Store struct {
	now            func() time.Time
	staleThreshold time.Duration
	started        time.Time

	mu      sync.RWMutex
	devices map[string]*deviceState

	subMu sync.Mutex
	subs  map[int]chan Event
	nextS int
}

// New builds a store. now is injectable for deterministic tests.
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		now:            now,
		staleThreshold: DefaultStaleThreshold,
		started:        now(),
		devices:        map[string]*deviceState{},
		subs:           map[int]chan Event{},
	}
}

// StartedAt returns the store creation time.
func (s *Store) StartedAt() time.Time { return s.started }

func (s *Store) device(name string) *deviceState {
	d, ok := s.devices[name]
	if !ok {
		d = &deviceState{
			summary:  DeviceSummary{Name: name, ConnectionState: "closed"},
			features: map[string]Feature{},
			info:     map[string]any{},
		}
		s.devices[name] = d
	}
	return d
}

// RegisterDevice seeds a device's static metadata.
func (s *Store) RegisterDevice(name, haID, brand, typ, vib string, info map[string]any) {
	s.mu.Lock()
	d := s.device(name)
	d.summary.HaID = haID
	d.summary.Brand = brand
	d.summary.Type = typ
	d.summary.Vib = vib
	if info != nil {
		d.info = info
	}
	s.mu.Unlock()
}

// SetConnectionState records a device's connection state and availability.
func (s *Store) SetConnectionState(name, connState string, available bool) {
	s.mu.Lock()
	d := s.device(name)
	d.summary.ConnectionState = connState
	d.summary.Available = available
	s.mu.Unlock()
	s.publish(Event{Type: EventConnection, Data: map[string]any{
		"device": name, "connection_state": connState, "available": available,
		"updated_at": formatTime(s.now()),
	}})
}

// UpdateFeature records a feature value change.
func (s *Store) UpdateFeature(device string, f Feature) {
	now := s.now()
	f.UpdatedAt = formatTime(now)
	s.mu.Lock()
	d := s.device(device)
	d.features[f.Feature] = f
	d.summary.FeatureCount = len(d.features)
	d.updatedAt = now
	d.summary.UpdatedAt = formatTime(now)
	s.mu.Unlock()
	s.publish(Event{Type: EventValue, Data: map[string]any{
		"device": device, "feature": f.Feature, "value": f.Value,
		"value_raw": f.ValueRaw, "updated_at": f.UpdatedAt,
	}})
}

// Snapshot returns a consistent copy of all device summaries.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Devices: make([]DeviceSummary, 0, len(s.devices))}
	for _, d := range s.devices {
		out.Devices = append(out.Devices, s.summaryLocked(d))
	}
	sort.Slice(out.Devices, func(i, j int) bool { return out.Devices[i].Name < out.Devices[j].Name })
	return out
}

// Device returns one device's detail, false if unknown. The lookup matches
// the device name or its haId.
func (s *Store) Device(nameOrHaID string) (DeviceDetail, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := s.devices[nameOrHaID]
	if d == nil {
		for _, cand := range s.devices {
			if cand.summary.HaID == nameOrHaID {
				d = cand
				break
			}
		}
	}
	if d == nil {
		return DeviceDetail{}, false
	}
	feats := make([]Feature, 0, len(d.features))
	for _, f := range d.features {
		feats = append(feats, f)
	}
	sort.Slice(feats, func(i, j int) bool { return feats[i].Feature < feats[j].Feature })
	return DeviceDetail{Device: s.summaryLocked(d), Info: d.info, Features: feats}, true
}

// summaryLocked computes a fresh summary (age) under the read lock.
func (s *Store) summaryLocked(d *deviceState) DeviceSummary {
	sum := d.summary
	if !d.updatedAt.IsZero() {
		sum.AgeSeconds = int64(s.now().Sub(d.updatedAt).Seconds())
	}
	return sum
}

// Health computes the daemon health (docs/09 §3).
func (s *Store) Health() Health {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := Health{Status: "ok", Devices: make([]HealthDevice, 0, len(s.devices))}
	for _, d := range s.devices {
		sum := s.summaryLocked(d)
		stale := s.isStale(d, sum)
		if stale {
			h.Status = "degraded"
		}
		h.Devices = append(h.Devices, HealthDevice{
			Name: sum.Name, ConnectionState: sum.ConnectionState, AgeSeconds: sum.AgeSeconds, Stale: stale,
		})
	}
	sort.Slice(h.Devices, func(i, j int) bool { return h.Devices[i].Name < h.Devices[j].Name })
	return h
}

func (s *Store) isStale(d *deviceState, sum DeviceSummary) bool {
	switch sum.ConnectionState {
	case "offline", "reconnecting", "connecting", "closed":
		return true
	}
	if d.updatedAt.IsZero() {
		return true
	}
	return s.now().Sub(d.updatedAt) > s.staleThreshold
}

// Subscribe registers an SSE subscriber. The returned channel is buffered
// (cap 1, latest-wins): a slow consumer drops intermediate events rather
// than blocking the publish path. Call cancel to unsubscribe.
func (s *Store) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 1)
	s.subMu.Lock()
	id := s.nextS
	s.nextS++
	s.subs[id] = ch
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
		s.subMu.Unlock()
	}
}

// publish fans an event out to all subscribers (non-blocking, latest-wins).
func (s *Store) publish(ev Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
			// Drop the oldest, keep the newest.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}
