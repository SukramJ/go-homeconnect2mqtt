// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// Entity is a live appliance feature: the static description plus the
// dynamic state delivered by NOTIFY/RESPONSE. per docs/02-data-model.md §6.
type Entity struct {
	Desc *profile.Entry

	mu          sync.RWMutex
	hasValue    bool
	valueRaw    any
	valueShadow any
	access      string
	available   bool
	hasMin      bool
	min         float64
	hasMax      bool
	max         float64
	hasStep     bool
	stepSize    float64
	execution   string

	revEnum map[string]int
}

// newEntity builds an entity from a parsed description entry, seeding the
// dynamic fields from the static ones.
func newEntity(d *profile.Entry) *Entity {
	e := &Entity{
		Desc:      d,
		access:    d.Access,
		available: d.Available,
		hasMin:    d.HasMin, min: d.Min,
		hasMax: d.HasMax, max: d.Max,
		hasStep: d.HasStep, stepSize: d.StepSize,
		execution: d.Execution,
	}
	if d.IsEnum() {
		e.revEnum = make(map[string]int, len(d.Enumeration))
		for v, name := range d.Enumeration {
			e.revEnum[strings.ToLower(name)] = v
		}
	}
	return e
}

// UID returns the entity uid.
func (e *Entity) UID() int { return e.Desc.UID }

// Name returns the feature name (may be "").
func (e *Entity) Name() string { return e.Desc.Name }

// Access returns the current (possibly NOTIFY-updated) access mode.
func (e *Entity) Access() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.access
}

// Available returns the current availability.
func (e *Entity) Available() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.available
}

// Writable reports whether a write is currently allowed: the dynamic
// access is read/write or write-only and the feature is available (FK-5).
func (e *Entity) Writable() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return (e.access == "readwrite" || e.access == "writeonly") && e.available
}

// HasValue reports whether a value has been received.
func (e *Entity) HasValue() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.hasValue
}

// ValueRaw returns the cast raw value (before enum resolution).
func (e *Entity) ValueRaw() any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.valueRaw
}

// Value returns the display value: the enum name when applicable, else the
// raw value. An enum miss returns the raw value rather than failing (#56).
func (e *Entity) Value() any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.hasValue {
		return nil
	}
	if e.Desc.IsEnum() {
		if iv, ok := asInt(e.valueRaw); ok {
			if name, ok := e.Desc.Enumeration[iv]; ok {
				return name
			}
		}
		return e.valueRaw
	}
	return e.valueRaw
}

// Bounds carries the numeric bounds of an entity, each with a presence flag.
type Bounds struct {
	Min     float64
	HasMin  bool
	Max     float64
	HasMax  bool
	Step    float64
	HasStep bool
}

// Bounds returns the current numeric bounds.
func (e *Entity) Bounds() Bounds {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Bounds{
		Min: e.min, HasMin: e.hasMin,
		Max: e.max, HasMax: e.hasMax,
		Step: e.stepSize, HasStep: e.hasStep,
	}
}

// update applies a NOTIFY/RESPONSE data item. per docs/02-data-model.md §6.3.
func (e *Entity) update(item map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := item["value"]; ok {
		e.valueRaw = castValue(e.Desc.ProtocolType, v)
		e.valueShadow = e.valueRaw
		e.hasValue = true
	}
	if v, ok := item["access"]; ok {
		if s, ok := v.(string); ok {
			e.access = strings.ToLower(s)
		}
	}
	if v, ok := item["available"]; ok {
		e.available = convertBool(v)
	}
	if v, ok := item["min"]; ok {
		if f, ok := asFloat(v); ok {
			e.hasMin, e.min = true, f
		}
	}
	if v, ok := item["max"]; ok {
		if f, ok := asFloat(v); ok {
			e.hasMax, e.max = true, f
		}
	}
	if v, ok := item["stepSize"]; ok {
		if f, ok := asFloat(v); ok {
			e.hasStep, e.stepSize = true, f
		}
	}
	// Live execution often arrives uppercased (#70): always lowercase.
	if v, ok := item["execution"]; ok {
		if s, ok := v.(string); ok {
			e.execution = strings.ToLower(s)
		}
	}
}

// resolveWriteValue maps a user-supplied value to the raw value to send.
// Enum names are resolved case-insensitively; floats with an integer step
// are written as integers (#68).
func (e *Entity) resolveWriteValue(v any) (any, error) {
	if e.Desc.IsEnum() {
		if s, ok := v.(string); ok {
			if iv, ok := e.revEnum[strings.ToLower(s)]; ok {
				return iv, nil
			}
			// Allow a raw numeric string too.
			if iv, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				return iv, nil
			}
			return nil, fmt.Errorf("homeconnect: %q is not a valid enum value", s)
		}
	}
	switch e.Desc.ProtocolType {
	case profile.ProtocolBoolean:
		return convertBool(v), nil
	case profile.ProtocolInteger:
		iv, ok := asInt(v)
		if !ok {
			return nil, fmt.Errorf("homeconnect: value %v is not an integer", v)
		}
		return iv, nil
	case profile.ProtocolFloat:
		f, ok := asFloat(v)
		if !ok {
			return nil, fmt.Errorf("homeconnect: value %v is not a number", v)
		}
		// Float setting with integer step expects an int on the wire (#68).
		if e.hasStep && e.stepSize == 1 && f == float64(int64(f)) {
			return int64(f), nil
		}
		return f, nil
	case profile.ProtocolString, profile.ProtocolObject:
		return v, nil
	default:
		return v, nil
	}
}

// ---- value cast helpers (docs/02-data-model.md §6.5) ----

func castValue(pt profile.ProtocolType, v any) any {
	switch pt {
	case profile.ProtocolBoolean:
		return convertBool(v)
	case profile.ProtocolInteger:
		if iv, ok := asInt(v); ok {
			return iv
		}
		return v
	case profile.ProtocolFloat:
		if f, ok := asFloat(v); ok {
			return f
		}
		return v
	case profile.ProtocolString:
		return toString(v)
	case profile.ProtocolObject:
		return v
	default:
		return v
	}
}

// convertBool accepts bools, case-insensitive "true"/"false" and numbers.
func convertBool(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case string:
		return strings.EqualFold(strings.TrimSpace(n), "true")
	case float64:
		return n != 0
	case int:
		return n != 0
	case int64:
		return n != 0
	default:
		return false
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		if iv, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int(iv), true
		}
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f, true
		}
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
