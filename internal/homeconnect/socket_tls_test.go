// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"testing"
)

func TestNewSocketSelectsAES(t *testing.T) {
	s, err := NewSocket(ConnectionAES, "192.168.1.5", testPSK, testIV)
	if err != nil {
		t.Fatalf("NewSocket AES: %v", err)
	}
	if _, ok := s.(*AESSocket); !ok {
		t.Errorf("expected *AESSocket, got %T", s)
	}
}

func TestNewSocketSelectsTLS(t *testing.T) {
	s, err := NewSocket(ConnectionTLS, "192.168.1.5", testPSK, nil)
	if err != nil {
		t.Fatalf("NewSocket TLS: %v", err)
	}
	if _, ok := s.(*TLSSocket); !ok {
		t.Errorf("expected *TLSSocket, got %T", s)
	}
}

func TestNewSocketUnknownType(t *testing.T) {
	if _, err := NewSocket(ConnectionType("FOO"), "h", testPSK, testIV); err == nil {
		t.Error("expected error for unknown connection type")
	}
}

func TestNewTLSSocketEmptyHost(t *testing.T) {
	if _, err := NewTLSSocket("", testPSK); err == nil {
		t.Error("expected error for empty host")
	}
}

// TestTLSSocketWithInjectedConn exercises the connected paths by overriding
// the dialer with a fake tunnel (what the cgo build would supply).
func TestTLSSocketWithInjectedConn(t *testing.T) {
	orig := tlsPSKConnect
	t.Cleanup(func() { tlsPSKConnect = orig })
	fake := &fakeTLSConn{}
	tlsPSKConnect = func(_ context.Context, _ string, _ []byte) (tlsConn, error) { return fake, nil }

	s, _ := NewTLSSocket("h", testPSK)
	if err := s.Connect(t.Context()); err != nil {
		t.Fatalf("Connect with injected dialer: %v", err)
	}
	if err := s.Send(t.Context(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := s.Receive(t.Context())
	if err != nil || got != "hello" {
		t.Errorf("Receive = %q, %v", got, err)
	}
	if err := s.Ping(t.Context()); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

type fakeTLSConn struct{ last string }

func (f *fakeTLSConn) send(_ context.Context, m string) error    { f.last = m; return nil }
func (f *fakeTLSConn) receive(_ context.Context) (string, error) { return f.last, nil }
func (f *fakeTLSConn) ping(_ context.Context) error              { return nil }
func (f *fakeTLSConn) close() error                              { return nil }
