// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

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
	dev := &Device{name: "d", haID: haIDFor("d"), app: app, topics: newDeviceTopics(testLayout("homeconnect"), haIDFor("d"))}
	return dev, sock
}

func TestHandleSetWritesScalar(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	// Mark the setting writable (post-init left it from the static parse).
	sendSet(t, b, dev, "BSH/Common/Setting/PowerState", []byte("true"))
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
	sendSet(t, b, dev, "Dishcare/Dishwasher/Program/Eco50", []byte("start"))
	if len(sock.sentTo("/ro/activeProgram")) != 1 {
		t.Errorf("expected standard activeProgram start, sent: %v", sock.sentTo("/ro/activeProgram"))
	}
}

func TestHandleSetStartProgramHob(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Hob")
	sendSet(t, b, dev, "Dishcare/Dishwasher/Program/Eco50", []byte("start"))
	// Hob uses the direct selectedProgram path.
	if len(sock.sentTo("/ro/selectedProgram")) != 1 || len(sock.sentTo("/ro/activeProgram")) != 0 {
		t.Errorf("hob should start via selectedProgram; selected=%d active=%d",
			len(sock.sentTo("/ro/selectedProgram")), len(sock.sentTo("/ro/activeProgram")))
	}
}

func TestHandleSetSelectProgramByName(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	sendSet(t, b, dev, "BSH/Common/Root/SelectedProgram", []byte("Dishcare.Dishwasher.Program.Eco50"))
	sel := sock.sentTo("/ro/selectedProgram")
	if len(sel) != 1 || dataInt(sel[0].Data[0]["program"]) != 0x1015 {
		t.Errorf("select-by-name wrong: %+v", sel)
	}
}

func TestHandleSetActiveProgramOffDeletes(t *testing.T) {
	b := testBridge()
	dev, sock := connectedDevice(t, "Dishwasher")
	sendSet(t, b, dev, "BSH/Common/Root/ActiveProgram", []byte("off"))
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
	sendSet(t, b, dev, "Nope/Missing", []byte("x"))
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
	b.handleSet(context.Background(), dev, setRequest{
		topic: "homeconnect/status/" + dev.haID + "/BSH/Common/Setting/PowerState", payload: "x",
		value: publisher.SetValue{Text: "x"},
	})
}

// sendSet drives handleSet the way the router does: the payload
// normalised by publisher.ParseSet, on the device's own set topic.
func sendSet(t *testing.T, b *Bridge, dev *Device, rel string, payload []byte) {
	t.Helper()
	v, err := publisher.ParseSet(payload)
	if err != nil {
		t.Fatalf("ParseSet(%q): %v", payload, err)
	}
	b.handleSet(context.Background(), dev, setRequest{
		topic: "homeconnect/set/" + dev.haID + "/" + rel, payload: string(payload), value: v,
	})
}

// TestSetConvertsPerTheConvention pins mqtt-smarthome 2.0 §5.3 on the
// write path: `{"val": …}` and a plain value are the same request, and a
// boolean is read from true/false, 1/0, on/off and yes/no in any case —
// before 0.15.0 everything but a literal "true" wrote false.
func TestSetConvertsPerTheConvention(t *testing.T) {
	cases := []struct {
		payload string
		want    bool
	}{
		{"true", true},
		{"ON", true},
		{"1", true},
		{"yes", true},
		{`{"val":true}`, true},
		{`{"val":"on"}`, true},
		{"false", false},
		{"off", false},
		{"0", false},
		{"No", false},
		{`{"val":false}`, false},
	}
	for _, c := range cases {
		b := testBridge()
		dev, sock := connectedDevice(t, "Dishwasher")
		sendSet(t, b, dev, "BSH/Common/Setting/PowerState", []byte(c.payload))
		writes := sock.sentTo("/ro/values")
		if len(writes) != 1 {
			t.Fatalf("%s: %d writes, want 1", c.payload, len(writes))
		}
		if got := writes[0].Data[0]["value"]; got != c.want {
			t.Errorf("%s: wrote %v, want %v", c.payload, got, c.want)
		}
	}
}

// TestARejectedSetIsLoggedWithTopicAndPayload is §3.3's MUST: a request the
// daemon will not carry out is logged at warn, naming the topic and the
// payload, and nothing is written.
func TestARejectedSetIsLoggedWithTopicAndPayload(t *testing.T) {
	var buf bytes.Buffer
	b := testBridge()
	b.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	dev, sock := connectedDevice(t, "Dishwasher")
	for _, tc := range []struct{ rel, payload string }{
		{"BSH/Common/Setting/PowerState", "maybe"},   // not a boolean
		{"Nope/Missing", "1"},                        // no such feature
		{"BSH/Common/Setting/PowerState", `{"a":1}`}, // structured, on a scalar
	} {
		buf.Reset()
		sendSet(t, b, dev, tc.rel, []byte(tc.payload))
		line := buf.String()
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "/set/"+dev.haID+"/"+tc.rel) ||
			!strings.Contains(line, "payload="+strconvQuote(tc.payload)) {
			t.Errorf("%s %s: logged %q, want a warning with topic and payload", tc.rel, tc.payload, line)
		}
	}
	if n := len(sock.sentTo("/ro/values")); n != 0 {
		t.Errorf("%d writes reached the appliance", n)
	}
}

// strconvQuote is how slog's text handler renders a value that needs
// quoting, and the bare value otherwise.
func strconvQuote(s string) string {
	if strings.ContainsAny(s, " \"={}") {
		return strconv.Quote(s)
	}
	return s
}

// TestNumbersAreRoundedAndClamped: a number is rounded to the feature's
// step and clamped to its range where the appliance reported them.
func TestNumbersAreRoundedAndClamped(t *testing.T) {
	b := testBridge()
	e := homeconnect.NewAppliance(nil, &profile.Description{Entries: []*profile.Entry{{
		UID: 1, Name: "X.Temp", Kind: profile.KindSetting, ProtocolType: profile.ProtocolInteger,
		Access: "readwrite", Available: true,
	}}}, nil)
	e.ApplyValues([]map[string]any{{"uid": 1, "min": 30, "max": 90, "stepSize": 5}})
	ent, _ := e.Entity(1)
	for in, want := range map[string]float64{"42": 40, "43": 45, "200": 90, "-4": 30} {
		v, err := b.writeValue(ent, publisher.SetValue{Text: in})
		if err != nil || v != want {
			t.Errorf("writeValue(%s) = (%v, %v), want %v", in, v, err, want)
		}
	}
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
	sendSet(t, b, dev, "BSH/Common/Setting/PowerState", []byte("true"))
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
