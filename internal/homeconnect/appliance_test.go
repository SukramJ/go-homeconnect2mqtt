// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"errors"
	"testing"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// fakeSession implements sessionAPI for appliance tests.
type fakeSession struct {
	notify             func(*Message)
	descResp, mandResp *Message
	connectErr         error
	postErr            error
	written            [][]map[string]any
	raw                []*Message
	writeResp          *Message
	writeErr           error
}

func (f *fakeSession) OnNotify(fn func(*Message)) { f.notify = fn }
func (f *fakeSession) Connect(context.Context) error {
	return f.connectErr
}

func (f *fakeSession) PostConnectInit(context.Context) (descChanges, mandatory *Message, err error) {
	return f.descResp, f.mandResp, f.postErr
}
func (f *fakeSession) Close() error                  { return nil }
func (f *fakeSession) Disconnected() <-chan struct{} { return make(chan struct{}) }
func (f *fakeSession) WriteValues(_ context.Context, data []map[string]any) (*Message, error) {
	f.written = append(f.written, data)
	return f.writeResp, f.writeErr
}

func (f *fakeSession) SendRaw(_ context.Context, m *Message) (*Message, error) {
	f.raw = append(f.raw, m)
	return f.writeResp, f.writeErr
}

// testDescription builds a small parsed description with an enum status, a
// writable boolean setting and a writable float (step 1).
func testDescription(t *testing.T) *profile.Description {
	t.Helper()
	dd := `<?xml version="1.0"?><device>
      <description><type>Dishwasher</type><brand>BOSCH</brand><model>M</model><version>2</version></description>
      <statusList uid="0001">
        <status access="read" available="true" enumerationType="3000" refCID="03" uid="1002"/>
      </statusList>
      <settingList uid="0003">
        <setting access="readWrite" available="true" refCID="01" uid="1005"/>
        <setting access="readWrite" available="true" refCID="04" uid="1006" min="0" max="90" stepSize="1"/>
      </settingList>
      <enumerationTypeList>
        <enumerationType enid="3000"><enumeration value="0"/><enumeration value="3"/></enumerationType>
      </enumerationTypeList>
    </device>`
	fm := `<featureMappingFile><featureDescription>
        <feature refUID="1002">BSH.Common.Status.OperationState</feature>
        <feature refUID="1005">BSH.Common.Setting.PowerState</feature>
        <feature refUID="1006">BSH.Common.Option.Duration</feature>
      </featureDescription>
      <enumDescriptionList><enumDescription refENID="3000">
        <enumMember refValue="0">Inactive</enumMember>
        <enumMember refValue="3">Run</enumMember>
      </enumDescription></enumDescriptionList></featureMappingFile>`
	d, err := profile.ParseDescription([]byte(dd), []byte(fm), nil)
	if err != nil {
		t.Fatalf("ParseDescription: %v", err)
	}
	return d
}

func TestApplianceBuildsEntities(t *testing.T) {
	a := NewAppliance(&fakeSession{}, testDescription(t), nil)
	if len(a.Entities()) != 3 {
		t.Fatalf("expected 3 entities, got %d", len(a.Entities()))
	}
	if _, ok := a.EntityByName("BSH.Common.Status.OperationState"); !ok {
		t.Error("entity not indexed by name")
	}
	if _, ok := a.Entity(0x1005); !ok {
		t.Error("entity not indexed by uid")
	}
}

func TestApplianceConnectAppliesMandatory(t *testing.T) {
	fs := &fakeSession{
		mandResp: &Message{
			Resource: "/ro/allMandatoryValues", Action: ActionResponse,
			Data: []map[string]any{{"uid": 0x1002, "value": 3}, {"uid": 0x1005, "value": true}},
		},
	}
	a := NewAppliance(fs, testDescription(t), nil)
	if err := a.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	op, _ := a.Entity(0x1002)
	if op.Value() != "Run" {
		t.Errorf("OperationState = %v, want Run", op.Value())
	}
	pw, _ := a.Entity(0x1005)
	if pw.Value() != true {
		t.Errorf("PowerState = %v, want true", pw.Value())
	}
}

func TestApplianceConnectPropagatesError(t *testing.T) {
	a := NewAppliance(&fakeSession{connectErr: errors.New("boom")}, testDescription(t), nil)
	if err := a.Connect(context.Background()); err == nil {
		t.Fatal("expected connect error")
	}
}

func TestApplianceNotifyRouting(t *testing.T) {
	fs := &fakeSession{}
	a := NewAppliance(fs, testDescription(t), nil)
	updates := 0
	a.OnUpdate(func(*Entity) { updates++ })
	_ = a.Connect(context.Background())

	// Simulate a NOTIFY /ro/values from the device.
	fs.notify(&Message{
		Resource: "/ro/values", Action: ActionNotify,
		Data: []map[string]any{{"uid": 0x1002, "value": 3}},
	})
	if op, _ := a.Entity(0x1002); op.Value() != "Run" {
		t.Errorf("notify not applied: %v", op.Value())
	}
	if updates == 0 {
		t.Error("OnUpdate callback not fired")
	}

	// Unknown uid must be ignored, not crash.
	fs.notify(&Message{Resource: "/ro/values", Action: ActionNotify, Data: []map[string]any{{"uid": 0x9999, "value": 1}}})
}

func TestApplianceWriteValue(t *testing.T) {
	fs := &fakeSession{writeResp: &Message{Action: ActionResponse}}
	a := NewAppliance(fs, testDescription(t), nil)
	_ = a.Connect(context.Background())

	if err := a.WriteValue(context.Background(), 0x1005, "true"); err != nil {
		t.Fatalf("WriteValue: %v", err)
	}
	if len(fs.written) != 1 || fs.written[0][0]["uid"] != 0x1005 {
		t.Errorf("write not dispatched: %+v", fs.written)
	}

	// Float setting with step 1 must be written as an int (#68).
	if err := a.WriteValue(context.Background(), 0x1006, 30.0); err != nil {
		t.Fatalf("WriteValue float: %v", err)
	}
	last := fs.written[len(fs.written)-1][0]["value"]
	if _, ok := last.(int64); !ok {
		t.Errorf("float-step1 value not int64: %T", last)
	}
}

func TestApplianceWriteNotWritable(t *testing.T) {
	a := NewAppliance(&fakeSession{}, testDescription(t), nil)
	// 0x1002 is read-only.
	if err := a.WriteValue(context.Background(), 0x1002, "x"); err == nil {
		t.Fatal("expected not-writable error")
	}
}

func TestStartProgramStandard(t *testing.T) {
	fs := &fakeSession{writeResp: &Message{Action: ActionResponse}}
	a := NewAppliance(fs, testDescription(t), nil)
	if _, err := a.StartProgram(context.Background(), 0x1015, nil, StartStandard); err != nil {
		t.Fatalf("StartProgram: %v", err)
	}
	if len(fs.raw) != 1 || fs.raw[0].Resource != "/ro/activeProgram" || fs.raw[0].Action != ActionPost {
		t.Errorf("standard start message wrong: %+v", fs.raw)
	}
	if fs.raw[0].Data[0]["program"] != 0x1015 {
		t.Errorf("program uid not set: %+v", fs.raw[0].Data)
	}
}

func TestStartProgramHobUsesSelected(t *testing.T) {
	fs := &fakeSession{writeResp: &Message{Action: ActionResponse}}
	a := NewAppliance(fs, testDescription(t), nil)
	if _, err := a.StartProgram(context.Background(), 0x1015, nil, StartHob); err != nil {
		t.Fatalf("StartProgram hob: %v", err)
	}
	if len(fs.raw) != 1 || fs.raw[0].Resource != "/ro/selectedProgram" {
		t.Errorf("hob start should post selectedProgram: %+v", fs.raw)
	}
}

func TestStartProgramCommand(t *testing.T) {
	// Add a StartProgram command to the description.
	dd := `<?xml version="1.0"?><device>
      <description><type>Oven</type><brand>B</brand><model>M</model><version>2</version></description>
      <commandList uid="0007"><command access="writeOnly" available="true" refCID="01" uid="2001"/></commandList>
    </device>`
	fm := `<featureMappingFile><featureDescription>
        <feature refUID="2001">BSH.Common.Command.StartProgram</feature>
      </featureDescription></featureMappingFile>`
	d, _ := profile.ParseDescription([]byte(dd), []byte(fm), nil)
	fs := &fakeSession{writeResp: &Message{Action: ActionResponse}}
	a := NewAppliance(fs, d, nil)
	if _, err := a.StartProgram(context.Background(), 0, nil, StartCommand); err != nil {
		t.Fatalf("StartProgram command: %v", err)
	}
	if len(fs.written) != 1 || fs.written[0][0]["uid"] != 0x2001 {
		t.Errorf("command start should write command uid: %+v", fs.written)
	}
}

func TestStopActiveProgramDeletes(t *testing.T) {
	fs := &fakeSession{writeResp: &Message{Action: ActionResponse}}
	a := NewAppliance(fs, testDescription(t), nil)
	if _, err := a.StopActiveProgram(context.Background()); err != nil {
		t.Fatalf("StopActiveProgram: %v", err)
	}
	if len(fs.raw) != 1 || fs.raw[0].Action != ActionDelete || fs.raw[0].Resource != "/ro/activeProgram" {
		t.Errorf("stop should DELETE activeProgram: %+v", fs.raw)
	}
}

func TestSelectProgram(t *testing.T) {
	fs := &fakeSession{writeResp: &Message{Action: ActionResponse}}
	a := NewAppliance(fs, testDescription(t), nil)
	opts := []map[string]any{{"uid": 5, "value": 1}}
	if _, err := a.SelectProgram(context.Background(), 0x1015, opts); err != nil {
		t.Fatalf("SelectProgram: %v", err)
	}
	if fs.raw[0].Resource != "/ro/selectedProgram" || fs.raw[0].Data[0]["options"] == nil {
		t.Errorf("select message wrong: %+v", fs.raw)
	}
}
