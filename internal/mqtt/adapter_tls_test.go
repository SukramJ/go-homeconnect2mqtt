// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package mqtt

import (
	"crypto/tls"
	"testing"
)

func TestTLSConfigFor(t *testing.T) {
	// nil base: ServerName defaulted to the host, TLS 1.2 floor.
	got := tlsConfigFor(nil, "broker.local")
	if got.ServerName != "broker.local" {
		t.Errorf("nil base ServerName = %q, want broker.local", got.ServerName)
	}
	if got.MinVersion != tls.VersionTLS12 {
		t.Errorf("nil base MinVersion = %x, want TLS 1.2", got.MinVersion)
	}

	// An explicit ServerName is preserved.
	if got := tlsConfigFor(&tls.Config{ServerName: "set.example"}, "broker.local"); got.ServerName != "set.example" {
		t.Errorf("explicit ServerName overwritten: %q", got.ServerName)
	}

	// InsecureSkipVerify means no ServerName is forced.
	if got := tlsConfigFor(&tls.Config{InsecureSkipVerify: true}, "broker.local"); got.ServerName != "" {
		t.Errorf("ServerName forced despite InsecureSkipVerify: %q", got.ServerName)
	}

	// The caller's config is never mutated.
	base := &tls.Config{}
	tlsConfigFor(base, "broker.local")
	if base.ServerName != "" {
		t.Error("input config was mutated")
	}
}
