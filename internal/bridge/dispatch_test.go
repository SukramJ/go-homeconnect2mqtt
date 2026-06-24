// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
)

func dispatchBridge(t *testing.T, deviceType string) (*Bridge, *fakeSocket) {
	t.Helper()
	b := testBridge()
	dev, sock := connectedDevice(t, deviceType)
	b.devices = []*Device{dev}
	return b, sock
}

func TestDispatchUnknownDevice(t *testing.T) {
	b, _ := dispatchBridge(t, "Dishwasher")
	err := b.Dispatch(context.Background(), "missing", "x", "1")
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("err = %v, want ErrDeviceNotFound", err)
	}
}

func TestDispatchUnknownFeature(t *testing.T) {
	b, _ := dispatchBridge(t, "Dishwasher")
	err := b.Dispatch(context.Background(), "d", "Nope.Feature", "1")
	if !errors.Is(err, ErrFeatureNotFound) {
		t.Errorf("err = %v, want ErrFeatureNotFound", err)
	}
}

func TestDispatchScalarWrite(t *testing.T) {
	b, sock := dispatchBridge(t, "Dishwasher")
	if err := b.Dispatch(context.Background(), "d", "BSH.Common.Setting.PowerState", "true"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(sock.sentTo("/ro/values")) != 1 {
		t.Errorf("expected one /ro/values write")
	}
}

func TestDispatchProgramStart(t *testing.T) {
	b, sock := dispatchBridge(t, "Dishwasher")
	if err := b.Dispatch(context.Background(), "d", "Dishcare.Dishwasher.Program.Eco50", "start"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(sock.sentTo("/ro/activeProgram")) != 1 {
		t.Errorf("expected program start via activeProgram")
	}
}

func TestDispatchSelectedProgramByName(t *testing.T) {
	b, sock := dispatchBridge(t, "Dishwasher")
	if err := b.Dispatch(context.Background(), "d", "BSH.Common.Root.SelectedProgram", "Dishcare.Dishwasher.Program.Eco50"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(sock.sentTo("/ro/selectedProgram")) != 1 {
		t.Errorf("expected selectedProgram post")
	}
}

func TestDispatchActiveProgramOff(t *testing.T) {
	b, sock := dispatchBridge(t, "Dishwasher")
	if err := b.Dispatch(context.Background(), "d", "BSH.Common.Root.ActiveProgram", "off"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	del := sock.sentTo("/ro/activeProgram")
	if len(del) != 1 || del[0].Action != homeconnect.ActionDelete {
		t.Errorf("expected DELETE activeProgram, got %+v", del)
	}
}

func TestDispatchDeviceErrorCode(t *testing.T) {
	b, sock := dispatchBridge(t, "Dishwasher")
	sock.override = func(req *homeconnect.Message, _ int) (*homeconnect.Message, bool) {
		if req.Resource == "/ro/values" {
			code := 400
			return &homeconnect.Message{SID: req.SID, MsgID: req.MsgID, Resource: req.Resource, Action: homeconnect.ActionResponse, Code: &code}, true
		}
		return nil, false
	}
	err := b.Dispatch(context.Background(), "d", "BSH.Common.Setting.PowerState", "true")
	if err == nil {
		t.Fatal("expected device error")
	}
	code, ok := DeviceErrorCode(err)
	if !ok || code != 400 {
		t.Errorf("DeviceErrorCode = %d, %v; want 400", code, ok)
	}
}

func TestDispatchNotWritable(t *testing.T) {
	// A read-only feature: add one to the description via a fresh device.
	b := testBridge()
	dev, _ := connectedDevice(t, "Dishwasher")
	b.devices = []*Device{dev}
	// PowerState is writable; mark it unavailable so it is no longer writable.
	e, _ := dev.app.EntityByName("BSH.Common.Setting.PowerState")
	dev.app.ApplyValues([]map[string]any{{"uid": e.UID(), "available": false}})
	if err := b.Dispatch(context.Background(), "d", "BSH.Common.Setting.PowerState", "true"); !errors.Is(err, ErrNotWritable) {
		t.Errorf("err = %v, want ErrNotWritable", err)
	}
}
