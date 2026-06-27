// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

//go:build tlspsk

package homeconnect

import (
	"context"
	"errors"
	"testing"
	"time"
)

// In the cgo `tlspsk` build the OpenSSL transport is wired in at init, so
// Connect must attempt a real dial rather than returning ErrTLSPSKUnsupported.
// Pointed at a closed port it fails with a dial error — proof the override is
// live (a full handshake is exercised by hc-util against real hardware).
func TestTLSSocketWiredInTLSPSKBuild(t *testing.T) {
	s, err := NewTLSSocket("127.0.0.1", testPSK) // nothing listening on :443
	if err != nil {
		t.Fatalf("NewTLSSocket: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Connect(ctx); err == nil {
		_ = s.Close()
		t.Fatal("Connect to a closed port unexpectedly succeeded")
	} else if errors.Is(err, ErrTLSPSKUnsupported) {
		t.Error("tlspsk build still returns ErrTLSPSKUnsupported; init() override not active")
	}
}
