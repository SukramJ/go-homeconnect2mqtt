// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"errors"
	"sync"
	"testing"
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

// readySleep returns a sleep func that records each requested duration and
// completes immediately, so backoff timing is deterministic and fast.
func readySleep(durs chan time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		durs <- d
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
}

// Every failed connect attempt must release whatever it left behind: the
// manager closes the Connectable before backing off, so no half-open
// socket or receive loop leaks per retry.
func TestFailedConnectClosesConn(t *testing.T) {
	conn := &fakeConn{connectFn: func() error { return errors.New("offline") }}
	durs := make(chan time.Duration, 16)
	m := NewManager(conn, ReconnectConfig{
		InitialBackoff: time.Second,
		MaxBackoff:     4 * time.Second,
		sleep:          readySleep(durs),
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { _ = m.Run(ctx); close(done) }()

	for range 3 {
		<-durs
	}
	cancel()
	<-done

	conn.mu.Lock()
	connects, closes := conn.connects, conn.closes
	conn.mu.Unlock()
	if closes < connects {
		t.Errorf("closes = %d, want >= connects (%d): failed attempts must be cleaned up", closes, connects)
	}
}

func TestReconnectBackoffExponential(t *testing.T) {
	durs := make(chan time.Duration, 100)
	conn := &fakeConn{connectFn: func() error { return errors.New("offline") }}
	m := NewManager(conn, ReconnectConfig{
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     800 * time.Millisecond,
		Jitter:         0,
		LogThrottle:    time.Hour,
		sleep:          readySleep(durs),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = m.Run(ctx); close(done) }()

	want := []time.Duration{100, 200, 400, 800, 800}
	for i, w := range want {
		got := <-durs
		if got != w*time.Millisecond {
			t.Errorf("backoff[%d] = %v, want %v", i, got, w*time.Millisecond)
		}
	}

	// connectFn never succeeds, so Run keeps retrying and keeps calling the
	// fake sleep after the five backoffs we assert above. That sleep func is a
	// select operand in Manager.wait, so Go evaluates it — performing its send
	// to durs — before the select can observe ctx.Done(). Once durs fills, Run
	// wedges on that send and never sees the cancellation, hanging the test
	// under scheduler pressure. Keep draining durs until Run exits so the send
	// always has room and the loop can reach its ctx.Err() check.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-durs:
			case <-done:
				return
			}
		}
	}()

	cancel()
	<-done
	<-drained
	if m.State() != StateClosed {
		t.Errorf("final state = %q, want closed", m.State())
	}
}

func TestReconnectSuccessThenDrop(t *testing.T) {
	durs := make(chan time.Duration, 100)
	states := make(chan ConnectionState, 64)
	conn := &fakeConn{connectFn: func() error { return nil }}
	m := NewManager(conn, ReconnectConfig{
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     time.Second,
		LogThrottle:    time.Hour,
		sleep:          readySleep(durs),
		OnState:        func(s ConnectionState) { states <- s },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()

	waitFor := func(target ConnectionState) {
		deadline := time.After(2 * time.Second)
		for {
			select {
			case s := <-states:
				if s == target {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for state %q", target)
			}
		}
	}
	waitFor(StateConnected)
	conn.triggerDrop()
	waitFor(StateReconnecting)
	waitFor(StateConnected) // reconnected

	cancel()
	if conn.closesCount() == 0 {
		t.Error("Close should have been called on drop/shutdown")
	}
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
	durs := make(chan time.Duration, 10)
	conn := &fakeConn{connectFn: func() error { return nil }}
	blocking := &blockingConn{inner: conn}
	m := NewManager(blocking, ReconnectConfig{
		ConnectTimeout: 50 * time.Millisecond,
		InitialBackoff: time.Millisecond,
		LogThrottle:    time.Hour,
		sleep:          readySleep(durs),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()
	select {
	case <-durs: // reached the offline backoff path => timeout fired
	case <-time.After(2 * time.Second):
		t.Fatal("connect timeout was not applied")
	}
}

// blockingConn blocks in Connect until the supplied context is done.
type blockingConn struct{ inner *fakeConn }

func (b *blockingConn) Connect(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (b *blockingConn) Disconnected() <-chan struct{} { return b.inner.Disconnected() }
func (b *blockingConn) Close() error                  { return b.inner.Close() }
