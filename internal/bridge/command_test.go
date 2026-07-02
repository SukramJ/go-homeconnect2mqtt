// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// fakeSocket implements homeconnect.Socket at the message (JSON) level. It
// plays the appliance handshake and records every request the client sends;
// a per-resource override lets a test inject error codes.
type fakeSocket struct {
	mu       sync.Mutex
	inbound  chan string
	done     chan struct{}
	sent     []*homeconnect.Message
	override func(req *homeconnect.Message, call int) (*homeconnect.Message, bool)
	calls    map[string]int
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{inbound: make(chan string, 64), done: make(chan struct{}), calls: map[string]int{}}
}

func (f *fakeSocket) Connect(context.Context) error {
	f.enqueue(&homeconnect.Message{
		SID: 1, MsgID: 100, Resource: "/ei/initialValues", Version: 2, Action: homeconnect.ActionPost,
		Data: []map[string]any{{"edMsgID": 1}},
	})
	return nil
}

func (f *fakeSocket) enqueue(m *homeconnect.Message) {
	b, _ := m.Encode()
	f.inbound <- string(b)
}

func (f *fakeSocket) Send(_ context.Context, message string) error {
	req, err := homeconnect.DecodeMessage([]byte(message))
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.sent = append(f.sent, req)
	f.calls[req.Resource]++
	call := f.calls[req.Resource]
	override := f.override
	f.mu.Unlock()

	resp := &homeconnect.Message{SID: req.SID, MsgID: req.MsgID, Resource: req.Resource, Version: req.Version, Action: homeconnect.ActionResponse}
	switch req.Resource {
	case "/ei/initialValues":
		return nil // client RESPONSE
	case "/ci/services":
		resp.Data = []map[string]any{{"service": "ci", "version": 3}, {"service": "ro", "version": 1}}
	case "/ro/allDescriptionChanges", "/ro/allMandatoryValues":
		resp.Data = nil
	default:
		if override != nil {
			if r, ok := override(req, call); ok {
				f.enqueue(r)
				return nil
			}
		}
	}
	f.enqueue(resp)
	return nil
}

func (f *fakeSocket) Receive(ctx context.Context) (string, error) {
	select {
	case t := <-f.inbound:
		return t, nil
	case <-f.done:
		return "", errors.New("closed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeSocket) Ping(context.Context) error { return nil }
func (f *fakeSocket) Close() error {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
	return nil
}

func (f *fakeSocket) sentTo(resource string) []*homeconnect.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*homeconnect.Message
	for _, m := range f.sent {
		if m.Resource == resource {
			out = append(out, m)
		}
	}
	return out
}

// commandDescription has a writable bool setting, a program, plus the
// active/selected program roots.
func commandDescription(t *testing.T, deviceType string) *profile.Description {
	t.Helper()
	dd := `<?xml version="1.0"?><device>
      <description><type>` + deviceType + `</type><brand>BOSCH</brand><model>M</model><version>2</version></description>
      <settingList uid="0003">
        <setting access="readWrite" available="true" refCID="01" uid="1005"/>
      </settingList>
      <programGroup uid="000B"><program available="true" execution="selectandstart" uid="1015"/></programGroup>
      <activeProgram access="readWrite" uid="1019"/>
      <selectedProgram access="readWrite" uid="101A"/>
    </device>`
	fm := `<featureMappingFile><featureDescription>
        <feature refUID="1005">BSH.Common.Setting.PowerState</feature>
        <feature refUID="1015">Dishcare.Dishwasher.Program.Eco50</feature>
        <feature refUID="1019">BSH.Common.Root.ActiveProgram</feature>
        <feature refUID="101A">BSH.Common.Root.SelectedProgram</feature>
      </featureDescription></featureMappingFile>`
	d, err := profile.ParseDescription([]byte(dd), []byte(fm), nil)
	if err != nil {
		t.Fatalf("ParseDescription: %v", err)
	}
	return d
}

func testBridge() *Bridge {
	return &Bridge{
		cfg:           testCfg(),
		mqtt:          newStubMQTT(),
		logger:        slog.New(slog.NewTextHandler(noopWriter{}, nil)),
		qos:           mqtt.QoS(1),
		cmdRetries:    2,
		cmdRetryDelay: time.Millisecond,
	}
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

func connectedDevice(t *testing.T, deviceType string) (*Device, *fakeSocket) {
	t.Helper()
	sock := newFakeSocket()
	sess := homeconnect.NewSession(sock, homeconnect.SessionConfig{SendTimeout: time.Second, HandshakeTimeout: time.Second})
	app := homeconnect.NewAppliance(sess, commandDescription(t, deviceType), nil)
	if err := app.Connect(t.Context()); err != nil {
		t.Fatalf("appliance Connect: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	dev := &Device{name: "d", app: app, topics: newDeviceTopics("homeconnect", "d")}
	return dev, sock
}

func TestHandleSetWritesScalar(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	// Mark the setting writable (post-init left it from the static parse).
	b.handleSet(context.Background(), dev, "homeconnect/d/BSH/Common/Setting/PowerState/set", []byte("true"))
	writes := sock.sentTo("/ro/values")
	if len(writes) != 1 {
		t.Fatalf("expected 1 /ro/values write, got %d", len(writes))
	}
	if dataInt(writes[0].Data[0]["uid"]) != 0x1005 {
		t.Errorf("wrong uid: %+v", writes[0].Data)
	}
}

// dataInt coerces a JSON-decoded numeric (float64) to int for assertions.
func dataInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return -1
	}
}

func TestHandleSetStartProgramStandard(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	b.handleSet(context.Background(), dev, "homeconnect/d/Dishcare/Dishwasher/Program/Eco50/set", []byte("start"))
	if len(sock.sentTo("/ro/activeProgram")) != 1 {
		t.Errorf("expected standard activeProgram start, sent: %v", sock.sentTo("/ro/activeProgram"))
	}
}

func TestHandleSetStartProgramHob(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Hob")
	b.handleSet(context.Background(), dev, "homeconnect/d/Dishcare/Dishwasher/Program/Eco50/set", []byte("start"))
	// Hob uses the direct selectedProgram path.
	if len(sock.sentTo("/ro/selectedProgram")) != 1 || len(sock.sentTo("/ro/activeProgram")) != 0 {
		t.Errorf("hob should start via selectedProgram; selected=%d active=%d",
			len(sock.sentTo("/ro/selectedProgram")), len(sock.sentTo("/ro/activeProgram")))
	}
}

func TestHandleSetSelectProgramByName(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	b.handleSet(context.Background(), dev, "homeconnect/d/BSH/Common/Root/SelectedProgram/set", []byte("Dishcare.Dishwasher.Program.Eco50"))
	sel := sock.sentTo("/ro/selectedProgram")
	if len(sel) != 1 || dataInt(sel[0].Data[0]["program"]) != 0x1015 {
		t.Errorf("select-by-name wrong: %+v", sel)
	}
}

func TestHandleSetActiveProgramOffDeletes(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	b.handleSet(context.Background(), dev, "homeconnect/d/BSH/Common/Root/ActiveProgram/set", []byte("off"))
	del := sock.sentTo("/ro/activeProgram")
	if len(del) != 1 || del[0].Action != homeconnect.ActionDelete {
		t.Errorf("active off should DELETE activeProgram: %+v", del)
	}
}

func TestHandleSetUnknownFeature(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	sock.mu.Lock()
	before := len(sock.sent)
	sock.mu.Unlock()
	b.handleSet(context.Background(), dev, "homeconnect/d/Nope/Missing/set", []byte("x"))
	sock.mu.Lock()
	after := len(sock.sent)
	sock.mu.Unlock()
	if after != before {
		t.Errorf("unknown feature should not send anything: before=%d after=%d", before, after)
	}
}

func TestHandleSetIgnoresNonSet(t *testing.T) {
	b := testBridge()
	dev, _ := connectedDevice(t, "Dishwasher")
	// Should be a no-op (no panic) for a non-/set topic.
	b.handleSet(context.Background(), dev, "homeconnect/d/BSH/Common/Setting/PowerState/state", []byte("x"))
}

func TestWriteWindowRetryThenSucceed(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	// First /ro/values write returns 541; the second succeeds (FK-5).
	sock.override = func(req *homeconnect.Message, call int) (*homeconnect.Message, bool) {
		if req.Resource == "/ro/values" && call == 1 {
			code := 541
			return &homeconnect.Message{SID: req.SID, MsgID: req.MsgID, Resource: req.Resource, Action: homeconnect.ActionResponse, Code: &code}, true
		}
		return nil, false
	}
	b.handleSet(context.Background(), dev, "homeconnect/d/BSH/Common/Setting/PowerState/set", []byte("true"))
	if n := len(sock.sentTo("/ro/values")); n < 2 {
		t.Errorf("expected a retry after 541, got %d writes", n)
	}
}

func TestIsStopValue(t *testing.T) {
	for _, v := range []string{"off", "OFF", "stop", "0", "", "false"} {
		if !isStopValue(v) {
			t.Errorf("isStopValue(%q) = false, want true", v)
		}
	}
	if isStopValue("on") {
		t.Error("isStopValue(on) = true")
	}
}

func TestIsWriteWindowError(t *testing.T) {
	if !isWriteWindowError(&homeconnect.CodeResponseError{Code: 541}) {
		t.Error("541 should be a write-window error")
	}
	if isWriteWindowError(&homeconnect.CodeResponseError{Code: 400}) {
		t.Error("400 should not be a write-window error")
	}
	if isWriteWindowError(errors.New("x")) {
		t.Error("plain error should not be a write-window error")
	}
}
