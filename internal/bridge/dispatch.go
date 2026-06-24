// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// Dispatch errors, mapped to the web error taxonomy (docs/09 §5).
var (
	ErrDeviceNotFound  = errors.New("device_not_found")
	ErrFeatureNotFound = errors.New("feature_not_found")
	ErrNotWritable     = errors.New("not_writable")
)

// deviceByName returns a configured device worker by its name.
func (b *Bridge) deviceByName(name string) (*Device, bool) {
	for _, d := range b.devices {
		if d.name == name {
			return d, true
		}
	}
	return nil, false
}

// Dispatch performs a synchronous write for the web API. It mirrors the
// MQTT command routing (handleSet) but returns a typed error so the HTTP
// layer can map it to the correct status (docs/09 §5).
func (b *Bridge) Dispatch(ctx context.Context, device, feature string, value any) error {
	d, ok := b.deviceByName(device)
	if !ok {
		return ErrDeviceNotFound
	}
	entity, ok := d.app.EntityByName(feature)
	if !ok {
		return fmt.Errorf("%w: %s", ErrFeatureNotFound, feature)
	}
	switch entity.Desc.Kind {
	case profile.KindProgram:
		if isStopValue(fmt.Sprint(value)) {
			_, err := d.app.StopActiveProgram(ctx)
			return err
		}
		_, err := d.app.StartProgram(ctx, entity.UID(), nil, b.startStrategy(d))
		return err
	case profile.KindSelectedProgram:
		uid, ok := b.resolveProgramUID(d, fmt.Sprint(value))
		if !ok {
			return fmt.Errorf("%w: program %v", ErrFeatureNotFound, value)
		}
		_, err := d.app.SelectProgram(ctx, uid, nil)
		return err
	case profile.KindActiveProgram:
		if isStopValue(fmt.Sprint(value)) {
			_, err := d.app.StopActiveProgram(ctx)
			return err
		}
		uid, ok := b.resolveProgramUID(d, fmt.Sprint(value))
		if !ok {
			return fmt.Errorf("%w: program %v", ErrFeatureNotFound, value)
		}
		_, err := d.app.StartProgram(ctx, uid, nil, b.startStrategy(d))
		return err
	default:
		if !entity.Writable() {
			return fmt.Errorf("%w: %s", ErrNotWritable, feature)
		}
		return d.app.WriteValue(ctx, entity.UID(), value)
	}
}

// DeviceErrorCode extracts an appliance error code from err, if present
// (docs/01 §9), so the HTTP layer can surface device_code.
func DeviceErrorCode(err error) (int, bool) {
	var ce *homeconnect.CodeResponseError
	if errors.As(err, &ce) {
		return ce.Code, true
	}
	return 0, false
}
