// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// fakeAppliance is an httptest server that speaks the AES wire protocol as
// the appliance side: it decrypts what the client signs with 'E' and
// replies signed with 'C', echoing the payload back.
func fakeAppliance(t *testing.T) (wsURL string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wsPath {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		sc, _ := NewAESCrypto(testPSK, testIV)
		ctx := r.Context()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary {
				return
			}
			msg, err := sc.open(encryptDirection, data)
			if err != nil {
				return
			}
			reply, err := sc.seal(decryptDirection, []byte("echo:"+string(msg)))
			if err != nil {
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, reply); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + wsPath
}

func newClientSocket(t *testing.T, url string) *AESSocket {
	t.Helper()
	c, err := NewAESCrypto(testPSK, testIV)
	if err != nil {
		t.Fatalf("NewAESCrypto: %v", err)
	}
	return &AESSocket{url: url, crypto: c}
}

func TestAESSocketRoundTrip(t *testing.T) {
	url := fakeAppliance(t)
	s := newClientSocket(t, url)
	ctx := t.Context()
	if err := s.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, msg := range []string{"hello", strings.Repeat("Z", 33), "{\"action\":\"GET\"}"} {
		if err := s.Send(ctx, msg); err != nil {
			t.Fatalf("Send(%q): %v", msg, err)
		}
		got, err := s.Receive(ctx)
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if got != "echo:"+msg {
			t.Errorf("Receive = %q, want %q", got, "echo:"+msg)
		}
	}
}

func TestAESSocketPing(t *testing.T) {
	url := fakeAppliance(t)
	s := newClientSocket(t, url)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	// A concurrent reader is required so the pong control frame is
	// processed; the session layer always has one running.
	go func() { _, _ = s.Receive(ctx) }()

	if err := s.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestAESSocketConnectFailure(t *testing.T) {
	s := newClientSocket(t, "ws://127.0.0.1:1/homeconnect")
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // already-cancelled context -> dial fails fast
	if err := s.Connect(ctx); err == nil {
		t.Fatal("expected Connect to fail")
	} else if errors.Is(err, context.Canceled) {
		return // acceptable
	}
}

func TestAESSocketResetOnReconnect(t *testing.T) {
	url := fakeAppliance(t)
	s := newClientSocket(t, url)
	ctx := t.Context()
	if err := s.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := s.Send(ctx, "first"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := s.Receive(ctx); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	_ = s.Close()

	// A second connect must reset crypto so the fresh server (zeroed
	// chains) stays in sync.
	url2 := fakeAppliance(t)
	s.url = url2
	if err := s.Connect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Send(ctx, "after-reset"); err != nil {
		t.Fatalf("Send after reset: %v", err)
	}
	got, err := s.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive after reset: %v", err)
	}
	if got != "echo:after-reset" {
		t.Errorf("got %q", got)
	}
}
