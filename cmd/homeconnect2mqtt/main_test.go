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

func TestRunNoConfig(t *testing.T) {
	// With no config file found, the daemon fails fast with a non-zero code.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var errBuf bytes.Buffer
	if code := run([]string{"--config", "/nonexistent/config.yaml"}, &errBuf); code == 0 {
		t.Fatalf("expected non-zero exit for missing config")
	}
}
