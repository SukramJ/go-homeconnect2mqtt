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
	"testing/synctest"
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

// waitForCount blocks until a topic has entered Publish want times.
//
// It is the counterpart to waitFor for a sequence that revisits a value:
// waiting on the payload cannot distinguish "the run has finished" from
// "the run happens to be passing through this value again".
//
// It counts entries, not completions — gatedMQTT raises the counter before
// it publishes — so a caller that also cares about the delivered payload
// needs waitFor after this, not instead of it.
func waitForCount(t *testing.T, g *gatedMQTT, topic string, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if n := g.callCount(topic); n >= want {
			if n > want {
				t.Fatalf("publish calls = %d, want %d", n, want)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("publish calls = %d, want %d (every transition delivered)", g.callCount(topic), want)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func buildGatedBridge(t *testing.T) (*Bridge, *gatedMQTT) {
	t.Helper()
	g := newGatedMQTT()
	b, err := New(Deps{
		Config: testCfg(),
		MQTT:   g,
		Plane:  testPlane(g),
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
	synctest.Test(t, func(t *testing.T) {
		b, stub := buildTestBridge(t)
		dev := b.devices[0]
		// Pretend the device had connected: retained availability is "online".
		b.onState(dev, homeconnect.StateConnected)

		attempts := 0 // only touched from the run goroutine and after synctest.Wait
		dev.runFn = func(ctx context.Context) error {
			attempts++
			if attempts <= 2 {
				panic("boom")
			}
			<-ctx.Done()
			return ctx.Err()
		}

		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- dev.run(ctx, b) }()

		// Two panics: the backoffs between the three attempts are 1s and 2s.
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if attempts != 3 {
			t.Fatalf("worker not restarted after panic, attempts = %d, want 3", attempts)
		}
		if got, want := time.Since(start), 3*time.Second; got != want {
			t.Errorf("third attempt started after %v, want %v of backoff", got, want)
		}
		if got := stub.get("homeconnect/dishwasher/availability"); got != availOffline {
			t.Errorf("availability between attempts = %q, want %q", got, availOffline)
		}
		if got := stub.get("homeconnect/dishwasher/connection_state"); got != string(homeconnect.StateOffline) {
			t.Errorf("connection_state between attempts = %q, want offline", got)
		}

		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("run returned %v, want context.Canceled", err)
			}
		default:
			t.Fatal("run did not stop after cancel")
		}
	})
}

// TestDeviceRunPanicBackoff asserts the restart backoff doubles up to the
// cap and resets after a run that survived long enough. The backoffs are
// read off the clock: every attempt records when it started, and the gap
// to the next start is the run's own length plus the backoff in between.
func TestDeviceRunPanicBackoff(t *testing.T) {
	sec := time.Second
	cases := []struct {
		name   string
		runFor time.Duration // how long each run lives before it panics
		want   []time.Duration
	}{
		{
			name:   "doubles to cap",
			runFor: 0, // every run is short: never stable, keep doubling
			want:   []time.Duration{sec, 2 * sec, 4 * sec, 8 * sec, 16 * sec, 30 * sec, 30 * sec},
		},
		{
			name:   "resets after stable run",
			runFor: 2 * time.Minute, // every run is stable: reset each time
			want:   []time.Duration{sec, sec, sec},
		},
		{
			name:   "run of exactly the stable length resets",
			runFor: time.Minute, // the documented stable length, not the constant: a test reading the constant follows it
			want:   []time.Duration{sec, sec, sec},
		},
		{
			name:   "run one nanosecond short of stable keeps doubling",
			runFor: time.Minute - time.Nanosecond,
			want:   []time.Duration{sec, 2 * sec, 4 * sec},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, _ := buildTestBridge(t)
				dev := b.devices[0]

				var starts []time.Time // only touched from the run goroutine and after synctest.Wait
				dev.runFn = func(ctx context.Context) error {
					starts = append(starts, time.Now())
					if len(starts) > len(tc.want) {
						<-ctx.Done()
						return ctx.Err()
					}
					time.Sleep(tc.runFor)
					panic("boom")
				}

				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- dev.run(ctx, b) }()
				var total time.Duration
				for _, w := range tc.want {
					total += w + tc.runFor
				}
				time.Sleep(total)
				synctest.Wait()
				cancel()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("run returned %v, want context.Canceled", err)
				}

				if len(starts) != len(tc.want)+1 {
					t.Fatalf("attempts = %d, want %d", len(starts), len(tc.want)+1)
				}
				for i, w := range tc.want {
					if got := starts[i+1].Sub(starts[i]) - tc.runFor; got != w {
						t.Errorf("backoff[%d] = %v, want %v", i, got, w)
					}
				}
			})
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

	// Two waits, because the two assertions are about different moments and
	// neither implies the other.
	//
	// The count first: no transition was lost. Waiting on the payload
	// cannot carry that, because the pulse ends on the same value it passes
	// through on the way — true, false, true, false — and waitFor polls the
	// topic's *last* payload, so it is satisfied the moment the second
	// publish lands. That is the failure CI saw on a loaded runner: three
	// delivered, a fourth still in flight, and a report of a lost
	// transition that never happened.
	waitForCount(t, g, topic, 4)
	// Then the payload: all four have also *completed*. callCount is raised
	// on entry to Publish, so the count reaching four says the fourth was
	// started, not that the broker has it.
	waitFor(t, g.stubMQTT, topic, "false")
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
