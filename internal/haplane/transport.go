// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package haplane is this daemon's go-hamqtt publish runtime: the
// discovery, state and availability planes expressed as
// github.com/SukramJ/go-hamqtt/publisher types, plus the two pieces of
// plumbing a consumer has to supply itself.
//
// It exists as its own package for a layering reason rather than a
// cosmetic one. internal/hass renders discovery configs and must be able
// to publish them; internal/bridge publishes state and runs the orphan
// sweep; cmd/homeconnect2mqtt needs the Last Will before either exists.
// All three need the same publisher.Runtime, and the one that renders
// must not import the one that publishes.
//
// Two pieces of plumbing live here because getting either wrong is
// silent:
//
//   - [Transport] closes the ordering knot at the composition root. The
//     Last Will is part of CONNECT, so the MQTT client needs
//     publisher.Runtime.Will before it is built — while the runtime needs
//     a transport that wraps that very client. The runtime is therefore
//     built over this placeholder and the real client wired into it
//     before the lifecycle starts.
//   - [Plane] rebuilds the publisher.Runtime on every (re)connect. That
//     is not a refinement: everything a runtime remembers — which legacy
//     topics it has superseded, which configs it has declared, which are
//     in flight — is a statement about a BROKER, and a reconnect means
//     the broker may have applied none of it. go-mtec2mqtt shipped a
//     process-lifetime runtime and its in-process retry published a
//     device document into a tree still holding every per-entity config
//     it believed it had retracted (its PR #54, finding F1). A runtime
//     that never outlives its connection has no per-field question to get
//     wrong, and covers a field the library adds later on the day it is
//     added.
package haplane

import (
	"context"
	"errors"
	"sync"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// ErrTransportNotWired is reported by a [Transport] used before its
// client was supplied.
//
// A programming error, reported rather than panicked: every caller is a
// publish or subscribe path, and a daemon that loses one message is a
// better outcome than a daemon that dies.
var ErrTransportNotWired = errors.New("haplane: transport used before the client was wired")

// Transport is a publisher.Transport whose target is supplied after
// construction. See the package comment for the ordering it closes.
//
// The mutex is an RWMutex rather than a Mutex because once connected the
// target is read concurrently from the transport's own read loop (the
// sweep window and the birth subscription both deliver there) and from
// every device worker's publish path.
type Transport struct {
	mu sync.RWMutex
	tr publisher.Transport
}

var _ publisher.Transport = (*Transport)(nil)

// Wire supplies the real transport. Calling it twice replaces the
// target, which is what a reconnect that rebuilds the client would need.
func (d *Transport) Wire(tr publisher.Transport) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tr = tr
}

func (d *Transport) target() (publisher.Transport, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.tr == nil {
		return nil, ErrTransportNotWired
	}
	return d.tr, nil
}

// Publish implements publisher.Transport.
func (d *Transport) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Publish(ctx, topic, payload, qos, retain)
}

// Subscribe implements publisher.Transport.
func (d *Transport) Subscribe(ctx context.Context, filter string, qos byte, handler publisher.Handler) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Subscribe(ctx, filter, qos, handler)
}

// Unsubscribe implements publisher.Transport.
func (d *Transport) Unsubscribe(ctx context.Context, filter string) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Unsubscribe(ctx, filter)
}
