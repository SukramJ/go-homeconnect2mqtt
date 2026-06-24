// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

func enumEntry() *profile.Entry {
	return &profile.Entry{
		UID: 0x1002, Name: "BSH.Common.Status.OperationState", Kind: profile.KindStatus,
		ProtocolType: profile.ProtocolInteger, Access: "read", Available: true,
		Enumeration: map[int]string{0: "Inactive", 3: "Run", 5: "Finished"},
	}
}

func TestEntityEnumValue(t *testing.T) {
	e := newEntity(enumEntry())
	e.update(map[string]any{"value": float64(3)})
	if e.Value() != "Run" {
		t.Errorf("Value = %v, want Run", e.Value())
	}
	if v := e.ValueRaw(); v != 3 {
		t.Errorf("ValueRaw = %v, want 3", v)
	}
}

func TestEntityEnumMissReturnsRaw(t *testing.T) {
	e := newEntity(enumEntry())
	e.update(map[string]any{"value": float64(99)}) // not in enum (#56)
	if e.Value() != 99 {
		t.Errorf("enum miss Value = %v, want raw 99", e.Value())
	}
}

func TestEntityUpdateAccessAvailable(t *testing.T) {
	e := newEntity(&profile.Entry{UID: 256, ProtocolType: profile.ProtocolInteger, Access: "read", Available: true})
	if e.Writable() {
		t.Error("read entity should not be writable")
	}
	e.update(map[string]any{"access": "READWRITE"}) // arrives uppercased
	if e.Access() != "readwrite" {
		t.Errorf("access = %q, want readwrite (lowercased)", e.Access())
	}
	if !e.Writable() {
		t.Error("entity should be writable after access update")
	}
	e.update(map[string]any{"available": false})
	if e.Writable() {
		t.Error("unavailable entity should not be writable (FK-5)")
	}
}

func TestEntityUpdateExecutionLowercased(t *testing.T) {
	e := newEntity(&profile.Entry{UID: 1, Kind: profile.KindProgram, ProtocolType: profile.ProtocolString})
	e.update(map[string]any{"execution": "SELECTANDSTART"}) // #70
	e.mu.RLock()
	exec := e.execution
	e.mu.RUnlock()
	if exec != "selectandstart" {
		t.Errorf("execution = %q, want selectandstart", exec)
	}
}

func TestEntityUpdateMinMaxStep(t *testing.T) {
	e := newEntity(&profile.Entry{UID: 1, ProtocolType: profile.ProtocolFloat})
	e.update(map[string]any{"min": float64(0), "max": float64(90), "stepSize": float64(10)})
	b := e.Bounds()
	if !b.HasMin || b.Min != 0 || !b.HasMax || b.Max != 90 || !b.HasStep || b.Step != 10 {
		t.Errorf("min/max/step = %v/%v/%v", b.Min, b.Max, b.Step)
	}
}

func TestResolveWriteEnum(t *testing.T) {
	e := newEntity(&profile.Entry{
		UID: 1, ProtocolType: profile.ProtocolInteger, Access: "readwrite", Available: true,
		Enumeration: map[int]string{0: "Off", 1: "On"},
	})
	v, err := e.resolveWriteValue("on") // case-insensitive
	if err != nil || v != 1 {
		t.Errorf("resolveWriteValue(on) = %v, %v; want 1", v, err)
	}
	if _, err := e.resolveWriteValue("bogus"); err == nil {
		t.Error("expected error for invalid enum value")
	}
}

func TestResolveWriteFloatToInt(t *testing.T) {
	// Float setting with integer step must be written as an int (#68).
	e := newEntity(&profile.Entry{UID: 1, ProtocolType: profile.ProtocolFloat, Access: "readwrite", Available: true, HasStep: true, StepSize: 1})
	v, err := e.resolveWriteValue(4.0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(int64); !ok {
		t.Errorf("expected int64 on the wire, got %T (%v)", v, v)
	}
}

func TestResolveWriteFloatKeepsFraction(t *testing.T) {
	e := newEntity(&profile.Entry{UID: 1, ProtocolType: profile.ProtocolFloat, Access: "readwrite", Available: true, HasStep: true, StepSize: 0.5})
	v, err := e.resolveWriteValue(4.5)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := v.(float64); !ok || f != 4.5 {
		t.Errorf("expected 4.5 float, got %T %v", v, v)
	}
}

func TestConvertBool(t *testing.T) {
	cases := map[any]bool{true: true, "true": true, "TRUE": true, "false": false, float64(1): true, float64(0): false, 0: false}
	for in, want := range cases {
		if got := convertBool(in); got != want {
			t.Errorf("convertBool(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestCastValue(t *testing.T) {
	if v := castValue(profile.ProtocolInteger, float64(7)); v != 7 {
		t.Errorf("int cast = %v", v)
	}
	if v := castValue(profile.ProtocolFloat, "3.5"); v != 3.5 {
		t.Errorf("float cast = %v", v)
	}
	if v := castValue(profile.ProtocolString, float64(5)); v != "5" {
		t.Errorf("string cast = %v", v)
	}
	if v := castValue(profile.ProtocolBoolean, "TRUE"); v != true {
		t.Errorf("bool cast = %v", v)
	}
}
