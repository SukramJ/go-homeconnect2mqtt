// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package mapping

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("expected empty catalogue, got %d", c.Len())
	}
}

func TestLoadAndLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mapping.yaml")
	content := `features:
  BSH.Common.Status.OperationState:
    device_class: enum
  BSH.Common.Option.RemainingProgramTime:
    device_class: duration
    unit: s
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Len() != 2 {
		t.Errorf("len = %d, want 2", c.Len())
	}
	if dc, ok := c.DeviceClass("BSH.Common.Status.OperationState"); !ok || dc != "enum" {
		t.Errorf("DeviceClass = %q, %v", dc, ok)
	}
	if u, ok := c.Unit("BSH.Common.Option.RemainingProgramTime"); !ok || u != "s" {
		t.Errorf("Unit = %q, %v", u, ok)
	}
	if _, ok := c.DeviceClass("Unknown"); ok {
		t.Error("unknown feature should have no device_class")
	}
	if _, ok := c.Unit("BSH.Common.Status.OperationState"); ok {
		t.Error("feature without unit should report none")
	}
}

func TestLoadMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(path, []byte("features: [this is not a map"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("expected error for malformed yaml")
	}
}

func TestEmpty(t *testing.T) {
	if Empty().Len() != 0 {
		t.Error("Empty should have zero features")
	}
}
