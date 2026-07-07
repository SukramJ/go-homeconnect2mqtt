// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// ConnectionState is the public lifecycle state surfaced over MQTT/web
// (docs/01-protocol.md §10).
type ConnectionState string

// Connection states.
const (
	StateConnecting   ConnectionState = "connecting"
	StateConnected    ConnectionState = "connected"
	StateReconnecting ConnectionState = "reconnecting"
	StateClosing      ConnectionState = "closing"
	StateClosed       ConnectionState = "closed"
	StateOffline      ConnectionState = "offline"
)

// Connectable is the lifecycle contract the reconnect manager drives;
// *Appliance satisfies it.
type Connectable interface {
	Connect(ctx context.Context) error
	Close() error
	Disconnected() <-chan struct{}
}

// ReconnectConfig governs the reconnect loop (FK-1).
type ReconnectConfig struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Jitter         time.Duration
	ConnectTimeout time.Duration
	LogThrottle    time.Duration // min interval between repeated offline logs
	Logger         *slog.Logger
	OnState        func(ConnectionState)

	// Injectable for deterministic tests; default to the real clock/rng.
	sleep   func(time.Duration) <-chan time.Time
	randInt func(int64) int64
}

// Manager drives a Connectable with exponential backoff + jitter, full
// reconnect on drop, and "offline is normal" semantics: a connect failure
// never aborts the loop, only context cancellation does.
type Manager struct {
	conn Connectable
	cfg  ReconnectConfig

	mu    sync.RWMutex
	state ConnectionState

	logMu       sync.Mutex
	lastOffline time.Time
	offlineRuns int
}

// NewManager builds a reconnect manager.
func NewManager(conn Connectable, cfg ReconnectConfig) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.LogThrottle <= 0 {
		cfg.LogThrottle = 30 * time.Second
	}
	if cfg.sleep == nil {
		cfg.sleep = time.After
	}
	if cfg.randInt == nil {
		cfg.randInt = rand.Int63n //nolint:gosec // jitter only, not security-sensitive
	}
	return &Manager{conn: conn, cfg: cfg, state: StateClosed}
}

// State returns the current connection state.
func (m *Manager) State() ConnectionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *Manager) setState(s ConnectionState) {
	m.mu.Lock()
	changed := m.state != s
	m.state = s
	m.mu.Unlock()
	if changed && m.cfg.OnState != nil {
		m.cfg.OnState(s)
	}
}

// Run drives the connect/reconnect loop until ctx is cancelled. It always
// returns ctx.Err() (or nil) — connect failures are handled internally.
func (m *Manager) Run(ctx context.Context) error {
	backoff := m.cfg.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			m.setState(StateClosed)
			return err
		}
		m.setState(StateConnecting)
		if err := m.connectOnce(ctx); err != nil {
			// Release whatever the failed attempt left behind (a
			// half-open socket, its receive loop); Close is nil-safe
			// and idempotent on every transport.
			_ = m.conn.Close()
			m.setState(StateOffline)
			m.logOffline(err)
			if !m.wait(ctx, m.jittered(backoff)) {
				m.setState(StateClosed)
				return ctx.Err()
			}
			backoff = m.nextBackoff(backoff)
			continue
		}

		m.setState(StateConnected)
		m.resetOfflineLog()
		backoff = m.cfg.InitialBackoff

		select {
		case <-ctx.Done():
			m.setState(StateClosing)
			_ = m.conn.Close()
			m.setState(StateClosed)
			return ctx.Err()
		case <-m.conn.Disconnected():
			m.setState(StateReconnecting)
			_ = m.conn.Close()
			if !m.wait(ctx, m.jittered(backoff)) {
				m.setState(StateClosed)
				return ctx.Err()
			}
		}
	}
}

// connectOnce applies the optional connect timeout around conn.Connect.
func (m *Manager) connectOnce(ctx context.Context) error {
	if m.cfg.ConnectTimeout <= 0 {
		return m.conn.Connect(ctx)
	}
	cctx, cancel := context.WithTimeout(ctx, m.cfg.ConnectTimeout)
	defer cancel()
	return m.conn.Connect(cctx)
}

// wait sleeps for d, returning false if the context is cancelled first.
func (m *Manager) wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-m.cfg.sleep(d):
		return true
	}
}

// nextBackoff doubles the backoff, capped at MaxBackoff.
func (m *Manager) nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > m.cfg.MaxBackoff {
		return m.cfg.MaxBackoff
	}
	return d
}

// jittered adds ±Jitter to d.
func (m *Manager) jittered(d time.Duration) time.Duration {
	if m.cfg.Jitter <= 0 {
		return d
	}
	delta := time.Duration(m.cfg.randInt(int64(m.cfg.Jitter*2))) - m.cfg.Jitter
	if d+delta < 0 {
		return 0
	}
	return d + delta
}

// logOffline rate-limits the recurring offline log so a permanently-off
// appliance does not spam (FK-1, #41).
func (m *Manager) logOffline(err error) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	m.offlineRuns++
	if time.Since(m.lastOffline) < m.cfg.LogThrottle && !m.lastOffline.IsZero() {
		return
	}
	m.cfg.Logger.Warn("homeconnect.offline",
		slog.String("err", err.Error()), slog.Int("attempts", m.offlineRuns))
	m.lastOffline = time.Now()
}

func (m *Manager) resetOfflineLog() {
	m.logMu.Lock()
	m.lastOffline = time.Time{}
	m.offlineRuns = 0
	m.logMu.Unlock()
}
