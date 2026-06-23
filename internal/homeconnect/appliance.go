// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// Appliance is the high-level device: it turns a parsed description into
// live entities, runs the connect + post-init sequence and routes NOTIFY
// updates to entities. mirrors appliance.py (HomeAppliance).
type Appliance struct {
	session sessionAPI
	desc    *profile.Description
	logger  *slog.Logger

	mu       sync.RWMutex
	byUID    map[int]*Entity
	byName   map[string]*Entity
	entities []*Entity

	updateMu sync.RWMutex
	onUpdate func(*Entity)
}

// sessionAPI is the slice of *Session the appliance needs, narrowed so
// tests can inject a fake.
type sessionAPI interface {
	OnNotify(func(*Message))
	Connect(ctx context.Context) error
	PostConnectInit(ctx context.Context) (descChanges, mandatory *Message, err error)
	Close() error
	WriteValues(ctx context.Context, data []map[string]any) (*Message, error)
}

// NewAppliance builds an appliance over a session and parsed description.
func NewAppliance(session sessionAPI, desc *profile.Description, logger *slog.Logger) *Appliance {
	if logger == nil {
		logger = slog.Default()
	}
	a := &Appliance{
		session: session,
		desc:    desc,
		logger:  logger,
		byUID:   map[int]*Entity{},
		byName:  map[string]*Entity{},
	}
	a.buildEntities()
	return a
}

// buildEntities creates one Entity per description entry, isolating any
// single bad entry (FK-3) — though parsing already filtered those.
func (a *Appliance) buildEntities() {
	for _, entry := range a.desc.Entries {
		e := newEntity(entry)
		a.entities = append(a.entities, e)
		a.byUID[e.UID()] = e
		if e.Name() != "" {
			a.byName[e.Name()] = e
		}
	}
}

// OnUpdate registers a callback fired after each entity value/state change.
func (a *Appliance) OnUpdate(fn func(*Entity)) {
	a.updateMu.Lock()
	a.onUpdate = fn
	a.updateMu.Unlock()
}

// Entities returns all entities sorted by feature name then uid (stable
// ordering for publishing/discovery).
func (a *Appliance) Entities() []*Entity {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*Entity, len(a.entities))
	copy(out, a.entities)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name() != out[j].Name() {
			return out[i].Name() < out[j].Name()
		}
		return out[i].UID() < out[j].UID()
	})
	return out
}

// Entity returns an entity by uid.
func (a *Appliance) Entity(uid int) (*Entity, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	e, ok := a.byUID[uid]
	return e, ok
}

// EntityByName returns an entity by feature name.
func (a *Appliance) EntityByName(name string) (*Entity, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	e, ok := a.byName[name]
	return e, ok
}

// Info exposes the device metadata.
func (a *Appliance) Info() profile.DeviceInfo { return a.desc.Info }

// Connect registers the NOTIFY handler, runs the session handshake and
// applies the bulk post-init values. A tolerated 500 leaves entities
// un-synced but the connection up (FK-2).
func (a *Appliance) Connect(ctx context.Context) error {
	a.session.OnNotify(a.handleNotify)
	if err := a.session.Connect(ctx); err != nil {
		return err
	}
	descChanges, mandatory, err := a.session.PostConnectInit(ctx)
	if err != nil {
		return err
	}
	a.applyMessage(descChanges)
	a.applyMessage(mandatory)
	return nil
}

// Close tears down the underlying session.
func (a *Appliance) Close() error { return a.session.Close() }

// handleNotify routes value/description updates to entities.
func (a *Appliance) handleNotify(msg *Message) {
	switch msg.Resource {
	case "/ro/values", "/ro/descriptionChange":
		a.applyMessage(msg)
	default:
		// Other notifications are ignored at this layer.
	}
}

// applyMessage applies every data item of a message to its entity.
func (a *Appliance) applyMessage(msg *Message) {
	if msg == nil {
		return
	}
	for _, item := range msg.Data {
		a.applyItem(item)
	}
}

func (a *Appliance) applyItem(item map[string]any) {
	uid, ok := asInt(item["uid"])
	if !ok {
		return
	}
	a.mu.RLock()
	e, ok := a.byUID[uid]
	a.mu.RUnlock()
	if !ok {
		a.logger.Debug("homeconnect.unknown_uid", slog.Int("uid", uid))
		return
	}
	e.update(item)
	a.updateMu.RLock()
	cb := a.onUpdate
	a.updateMu.RUnlock()
	if cb != nil {
		cb(e)
	}
}

// WriteValue normalises and writes a single feature value (uid resolved by
// the bridge). The full device-specific start paths are layered on in P7.
func (a *Appliance) WriteValue(ctx context.Context, uid int, value any) error {
	a.mu.RLock()
	e, ok := a.byUID[uid]
	a.mu.RUnlock()
	if !ok {
		return fmt.Errorf("homeconnect: unknown uid %d", uid)
	}
	if !e.Writable() {
		return fmt.Errorf("homeconnect: feature %d not writable (access %q, available %v)", uid, e.Access(), e.Available())
	}
	raw, err := e.resolveWriteValue(value)
	if err != nil {
		return err
	}
	resp, err := a.session.WriteValues(ctx, []map[string]any{{"uid": uid, "value": raw}})
	if err != nil {
		return err
	}
	// Optimistically set the shadow value (real value follows via NOTIFY).
	if resp != nil && resp.Code == nil {
		e.mu.Lock()
		e.valueShadow = raw
		e.mu.Unlock()
	}
	return nil
}
