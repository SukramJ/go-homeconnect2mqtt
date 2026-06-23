// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var errBuf bytes.Buffer
	if code := run([]string{"--version"}, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(errBuf.String(), "go-homeconnect2mqtt") {
		t.Errorf("version output = %q", errBuf.String())
	}
}

func TestRunBadFlag(t *testing.T) {
	var errBuf bytes.Buffer
	if code := run([]string{"--nope"}, &errBuf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunStartsAndExits(t *testing.T) {
	// Until the bridge is wired (P6) the daemon logs and returns 0.
	var errBuf bytes.Buffer
	if code := run(nil, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}
