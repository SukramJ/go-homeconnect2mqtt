// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

//go:build !tlspsk

package homeconnect

import (
	"errors"
	"testing"
)

// In the default (CGo-free) build the TLS-PSK transport is unavailable, so
// Connect fails cleanly with ErrTLSPSKUnsupported and the device worker treats
// it as "offline" (docs/01-protocol.md §4).
func TestTLSSocketUnsupportedByDefault(t *testing.T) {
	s, err := NewTLSSocket("192.168.1.5", testPSK)
	if err != nil {
		t.Fatalf("NewTLSSocket: %v", err)
	}
	if err := s.Connect(t.Context()); !errors.Is(err, ErrTLSPSKUnsupported) {
		t.Errorf("Connect err = %v, want ErrTLSPSKUnsupported", err)
	}
	// Operations before a successful connect fail cleanly.
	if _, err := s.Receive(t.Context()); err == nil {
		t.Error("Receive before connect should fail")
	}
	if err := s.Send(t.Context(), "x"); err == nil {
		t.Error("Send before connect should fail")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close on unconnected TLS socket: %v", err)
	}
}
