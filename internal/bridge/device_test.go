// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// gatedMQTT blocks every publish until gate is closed, recording per-topic
// call counts; it simulates a broker brownout for the async publish path.
type gatedMQTT struct {
	*stubMQTT
	mu      sync.Mutex
	calls   map[string]int
	entered chan string
	gate    chan struct{}
}

func newGatedMQTT() *gatedMQTT {
	return &gatedMQTT{
		stubMQTT: newStubMQTT(),
		calls:    map[string]int{},
		entered:  make(chan string, 1),
		gate:     make(chan struct{}),
	}
}

func (g *gatedMQTT) Publish(ctx context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	g.mu.Lock()
	g.calls[topic]++
	g.mu.Unlock()
	select {
	case g.entered <- topic:
	default:
	}
	select {
	case <-g.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.stubMQTT.Publish(ctx, topic, payload, qos, retain)
}

func (g *gatedMQTT) callCount(topic string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[topic]
}

func buildGatedBridge(t *testing.T) (*Bridge, *gatedMQTT) {
	t.Helper()
	g := newGatedMQTT()
	b, err := New(Deps{
		Config: testCfg(),
		MQTT:   g,
		Devices: []DeviceSpec{{
			Config: profile.DeviceConfig{
				Name: "dishwasher", Host: "192.168.1.50",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, g
}

// TestDeviceRunRestartsAfterPanic asserts a panicking worker run is
// restarted (docs/05-resilience.md: only ctx cancel stops a worker) and
// that availability is forced offline between attempts so retained state
// never advertises a dead device as online.
func TestDeviceRunRestartsAfterPanic(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	// Pretend the device had connected: retained availability is "online".
	b.onState(dev, homeconnect.StateConnected)

	var mu sync.Mutex
	attempts := 0
	dev.runFn = func(ctx context.Context) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 2 {
			panic("boom")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	// Injected sleep: fire immediately so the restart loop is fast.
	dev.sleep = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dev.run(ctx, b) }()

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker not restarted after panic, attempts = %d", n)
		case <-time.After(2 * time.Millisecond):
		}
	}
	if got := stub.get("homeconnect/dishwasher/availability"); got != availOffline {
		t.Errorf("availability between attempts = %q, want %q", got, availOffline)
	}
	if got := stub.get("homeconnect/dishwasher/connection_state"); got != string(homeconnect.StateOffline) {
		t.Errorf("connection_state between attempts = %q, want offline", got)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

// TestDeviceRunPanicBackoff asserts the restart backoff doubles up to the
// cap and resets after a run that survived long enough.
func TestDeviceRunPanicBackoff(t *testing.T) {
	sec := time.Second
	cases := []struct {
		name    string
		advance time.Duration // fake clock step per now() call
		want    []time.Duration
	}{
		{
			name:    "doubles to cap",
			advance: 0, // every run "lasts" 0s: never stable, keep doubling
			want:    []time.Duration{sec, 2 * sec, 4 * sec, 8 * sec, 16 * sec, 30 * sec, 30 * sec},
		},
		{
			name:    "resets after stable run",
			advance: 2 * time.Minute, // every run "lasts" 2m: reset each time
			want:    []time.Duration{sec, sec, sec},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := buildTestBridge(t)
			dev := b.devices[0]
			dev.runFn = func(context.Context) error { panic("boom") }

			cur := time.Unix(0, 0) // only touched from the run goroutine
			dev.now = func() time.Time {
				cur = cur.Add(tc.advance)
				return cur
			}

			var mu sync.Mutex
			var got []time.Duration
			collected := make(chan struct{})
			dev.sleep = func(d time.Duration) <-chan time.Time {
				mu.Lock()
				got = append(got, d)
				n := len(got)
				mu.Unlock()
				if n >= len(tc.want) {
					close(collected)
					return nil // block; the loop then ends via ctx cancel
				}
				ch := make(chan time.Time, 1)
				ch <- time.Time{}
				return ch
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- dev.run(ctx, b) }()
			select {
			case <-collected:
			case <-time.After(5 * time.Second):
				t.Fatal("restart loop did not reach the expected attempt count")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not stop after cancel")
			}

			mu.Lock()
			defer mu.Unlock()
			if len(got) != len(tc.want) {
				t.Fatalf("backoffs = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("backoff[%d] = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestOnUpdateDoesNotBlockOnSlowPublish asserts the appliance receive-side
// callback stays non-blocking while the broker is wedged: publishes are
// decoupled through the per-device queue (docs/05-resilience.md).
func TestOnUpdateDoesNotBlockOnSlowPublish(t *testing.T) {
	b, g := buildGatedBridge(t)
	dev := b.devices[0]
	startPublisher(t, b, dev)

	// First update: the drain goroutine enters Publish and stalls there.
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": true}})
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher never reached Publish")
	}

	floodDone := make(chan struct{})
	go func() {
		defer close(floodDone)
		for i := range 50 {
			dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": i%2 == 0}})
		}
	}()
	select {
	case <-floodDone:
	case <-time.After(2 * time.Second):
		t.Fatal("onUpdate blocked behind a stalled publish")
	}
	close(g.gate) // release the wedged publish so cleanup is prompt
	waitFor(t, g.stubMQTT, "homeconnect/dishwasher/BSH/Common/Setting/PowerState/state", "false")
}

// TestPublisherPreservesEveryUpdate asserts updates queued while the
// publisher is stalled are all delivered in order once it resumes: event
// entities pulse (Present -> Off) and edge-triggered consumers must see
// both transitions, so nothing is coalesced away.
func TestPublisherPreservesEveryUpdate(t *testing.T) {
	b, g := buildGatedBridge(t)
	dev := b.devices[0]
	topic := "homeconnect/dishwasher/BSH/Common/Setting/PowerState/state"
	startPublisher(t, b, dev)

	// Stall the drain goroutine inside its first Publish.
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": true}})
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher never reached Publish")
	}

	// A short pulse while stalled: both edges must survive the backlog.
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": false}})
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": true}})
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": false}})
	close(g.gate)
	waitFor(t, g.stubMQTT, topic, "false")
	// The stalled publish plus all three queued transitions.
	if n := g.callCount(topic); n != 4 {
		t.Errorf("publish calls = %d, want 4 (every transition delivered)", n)
	}
}

// TestPublisherDropsOldestWhenFull asserts the backlog cap drops the oldest
// update (keeping the newest state) instead of blocking or growing without
// bound.
func TestPublisherDropsOldestWhenFull(t *testing.T) {
	p := newDevicePublisher(slog.New(slog.DiscardHandler))
	for i := range maxQueuedPublishes + 10 {
		p.enqueue("t", []byte(strconv.Itoa(i)))
	}
	p.mu.Lock()
	n := len(p.queue)
	first, last := string(p.queue[0].payload), string(p.queue[n-1].payload)
	p.mu.Unlock()
	if n != maxQueuedPublishes {
		t.Fatalf("queue length = %d, want %d", n, maxQueuedPublishes)
	}
	if first != "10" || last != strconv.Itoa(maxQueuedPublishes+9) {
		t.Errorf("queue window = [%s..%s], want oldest dropped [10..%d]", first, last, maxQueuedPublishes+9)
	}
}

// TestDevicePublisherFlushesNothingAfterCancel asserts pending payloads are
// dropped once the context is cancelled (shutdown must not hang on MQTT).
func TestDevicePublisherFlushesNothingAfterCancel(t *testing.T) {
	p := newDevicePublisher(slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.enqueue("t", []byte("v"))
	var published int
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.run(ctx, func(string, []byte) { published++ })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not exit on cancelled context")
	}
	if published != 0 {
		t.Errorf("published %d payloads after cancel, want 0", published)
	}
}
