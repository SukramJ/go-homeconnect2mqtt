// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type fakeConn struct {
	mu        sync.Mutex
	connectFn func() error
	dropped   chan struct{}
	closes    int
	connects  int
}

func (c *fakeConn) Connect(context.Context) error {
	c.mu.Lock()
	c.connects++
	fn := c.connectFn
	c.mu.Unlock()
	var err error
	if fn != nil {
		err = fn()
	}
	if err == nil {
		c.mu.Lock()
		c.dropped = make(chan struct{})
		c.mu.Unlock()
	}
	return err
}

func (c *fakeConn) Disconnected() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == nil {
		c.dropped = make(chan struct{})
	}
	return c.dropped
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) triggerDrop() {
	c.mu.Lock()
	d := c.dropped
	c.mu.Unlock()
	if d != nil {
		close(d)
	}
}

// Every failed connect attempt must release whatever it left behind: the
// manager closes the Connectable before backing off, so no half-open
// socket or receive loop leaks per retry.
func TestFailedConnectClosesConn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := &fakeConn{connectFn: func() error { return errors.New("offline") }}
		m := NewManager(conn, ReconnectConfig{
			InitialBackoff: time.Second,
			MaxBackoff:     4 * time.Second,
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = m.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }() // a failed assertion must not leak the run goroutine out of the bubble

		// Attempts start at 0s, 1s and 3s; the next one is due at 7s.
		time.Sleep(4 * time.Second)
		synctest.Wait()
		cancel()
		<-done

		conn.mu.Lock()
		connects, closes := conn.connects, conn.closes
		conn.mu.Unlock()
		if connects != 3 {
			t.Errorf("connects = %d, want 3", connects)
		}
		if closes < connects {
			t.Errorf("closes = %d, want >= connects (%d): failed attempts must be cleaned up", closes, connects)
		}
	})
}

func TestReconnectBackoffExponential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var attempts []time.Duration // offsets from start; only touched from the run goroutine and after synctest.Wait
		conn := &fakeConn{connectFn: func() error {
			attempts = append(attempts, time.Since(start))
			return errors.New("offline")
		}}
		m := NewManager(conn, ReconnectConfig{
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     800 * time.Millisecond,
			Jitter:         0,
			LogThrottle:    time.Hour,
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = m.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }() // a failed assertion must not leak the run goroutine out of the bubble

		// Backoffs 100, 200, 400, 800, 800 ms put the attempts at these offsets.
		want := []time.Duration{0, 100, 300, 700, 1500, 2300}
		time.Sleep(2300 * time.Millisecond)
		synctest.Wait()
		if len(attempts) != len(want) {
			t.Fatalf("attempts at %v, want %d of them", attempts, len(want))
		}
		for i, w := range want {
			if attempts[i] != w*time.Millisecond {
				t.Errorf("attempt[%d] at %v, want %v", i, attempts[i], w*time.Millisecond)
			}
		}

		cancel()
		<-done
		if m.State() != StateClosed {
			t.Errorf("final state = %q, want closed", m.State())
		}
	})
}

func TestReconnectSuccessThenDrop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := &fakeConn{connectFn: func() error { return nil }}
		m := NewManager(conn, ReconnectConfig{
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     time.Second,
			LogThrottle:    time.Hour,
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = m.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }() // a failed assertion must not leak the run goroutine out of the bubble

		synctest.Wait()
		if got := m.State(); got != StateConnected {
			t.Fatalf("state = %q, want connected", got)
		}

		conn.triggerDrop()
		synctest.Wait()
		if got := m.State(); got != StateReconnecting {
			t.Fatalf("state after drop = %q, want reconnecting", got)
		}

		// The reconnect waits out the initial backoff: not a nanosecond early.
		time.Sleep(10*time.Millisecond - time.Nanosecond)
		synctest.Wait()
		if got := m.State(); got != StateReconnecting {
			t.Fatalf("state 1ns before the backoff ends = %q, want reconnecting", got)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := m.State(); got != StateConnected {
			t.Fatalf("state after the backoff = %q, want connected (reconnected)", got)
		}

		cancel()
		<-done
		if conn.closesCount() == 0 {
			t.Error("Close should have been called on drop/shutdown")
		}
	})
}

func (c *fakeConn) closesCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

func TestReconnectContextCancel(t *testing.T) {
	conn := &fakeConn{connectFn: func() error { return nil }}
	m := NewManager(conn, ReconnectConfig{LogThrottle: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Wait until connected, then cancel.
	deadline := time.After(2 * time.Second)
	for m.State() != StateConnected {
		select {
		case <-deadline:
			t.Fatal("never reached connected")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if m.State() != StateClosed {
		t.Errorf("final state = %q, want closed", m.State())
	}
}

func TestJitteredBounds(t *testing.T) {
	m := NewManager(&fakeConn{}, ReconnectConfig{
		Jitter:  500 * time.Millisecond,
		randInt: func(int64) int64 { return 0 }, // -> -jitter
	})
	if got := m.jittered(time.Second); got != 500*time.Millisecond {
		t.Errorf("jittered with rand=0 = %v, want 500ms", got)
	}
	m.cfg.randInt = func(n int64) int64 { return n - 1 } // -> +jitter-1ns
	if got := m.jittered(time.Second); got <= time.Second {
		t.Errorf("jittered with max rand = %v, want > 1s", got)
	}
}

func TestConnectTimeoutApplied(t *testing.T) {
	// Connect blocks until its context is cancelled; ConnectTimeout must
	// cancel it so the loop progresses to Offline.
	synctest.Test(t, func(t *testing.T) {
		conn := &fakeConn{connectFn: func() error { return nil }}
		blocking := &blockingConn{inner: conn}
		m := NewManager(blocking, ReconnectConfig{
			ConnectTimeout: 50 * time.Millisecond,
			InitialBackoff: time.Millisecond,
			LogThrottle:    time.Hour,
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = m.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }() // a failed assertion must not leak the run goroutine out of the bubble

		time.Sleep(50*time.Millisecond - time.Nanosecond)
		synctest.Wait()
		if got := m.State(); got != StateConnecting {
			t.Fatalf("state 1ns before the timeout = %q, want connecting", got)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := m.State(); got != StateOffline {
			t.Fatalf("state at the timeout = %q, want offline: the connect timeout was not applied", got)
		}

		cancel()
		<-done
	})
}

// blockingConn blocks in Connect until the supplied context is done.
type blockingConn struct{ inner *fakeConn }

func (b *blockingConn) Connect(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (b *blockingConn) Disconnected() <-chan struct{} { return b.inner.Disconnected() }
func (b *blockingConn) Close() error                  { return b.inner.Close() }
