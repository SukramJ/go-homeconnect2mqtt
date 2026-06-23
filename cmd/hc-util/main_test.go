// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"version"}, &out, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "go-homeconnect2mqtt") {
		t.Errorf("version output = %q", out.String())
	}
}

func TestRunHelp(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"help"}, &out, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Errorf("help output = %q", out.String())
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run(nil, &out, &errBuf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunUnknown(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown subcommand") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestRunNotImplemented(t *testing.T) {
	for _, sub := range []string{"parse", "dump", "connection-test"} {
		var out, errBuf bytes.Buffer
		if code := run([]string{sub}, &out, &errBuf); code != 1 {
			t.Errorf("%s: exit code = %d, want 1", sub, code)
		}
	}
}
