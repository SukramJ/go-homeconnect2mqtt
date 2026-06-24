// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"testing"
)

// Synthetic fixtures authored for the parser tests. The element/attribute
// schema (device/statusList/refCID/enumerationType/…) is the factual Home
// Connect XML structure; the brand/model, feature names and enum keys are
// our own — no third-party fixture is reproduced.
const deviceDescriptionShort = `<?xml version="1.0" encoding="UTF-8"?>
<device>
    <description>
        <type>HomeAppliance</type>
        <brand>DemoBrand</brand>
        <model>DemoModel</model>
        <version>2</version>
        <revision>0</revision>
    </description>
    <statusList access="read" available="true" uid="0001">
        <status access="read" available="true" refCID="01" refDID="00" uid="1001" />
        <statusList access="read" available="true" uid="0002">
            <status access="read" available="true" enumerationType="3002" refCID="03" refDID="00" uid="1002" />
        </statusList>
    </statusList>
    <settingList access="readWrite" available="true" uid="0003">
        <setting access="readWrite" available="true" refCID="01" uid="1005" max="10" min="0" stepSize="1" initValue="1" default="0" refDID="00" passwordProtected="false" notifyOnChange="false" />
        <settingList access="readWrite" available="true" uid="0004">
            <setting access="readWrite" available="true" refCID="01" refDID="00" uid="1006" />
        </settingList>
    </settingList>
    <eventList uid="0005">
        <event enumerationType="3001" handling="acknowledge" level="hint" refCID="03" refDID="80" uid="1009" />
        <eventList uid="0006">
            <event enumerationType="3001" handling="acknowledge" level="hint" refCID="03" refDID="80" uid="100A" />
            <event enumerationType="3003" handling="acknowledge" level="hint" refCID="03" refDID="80" uid="100B" />
        </eventList>
    </eventList>
    <commandList access="writeOnly" available="true" uid="0007">
        <command access="writeOnly" available="true" refCID="01" refDID="00" uid="100D" />
    </commandList>
    <optionList access="readWrite" available="true" uid="0009">
        <option access="read" available="true" refCID="11" refDID="A0" uid="1011" liveUpdate="true" />
    </optionList>
    <programGroup available="true" uid="000B">
        <program available="true" execution="selectOnly" uid="1015">
            <option access="readWrite" available="true" liveUpdate="false" default="true" refUID="1011" />
        </program>
    </programGroup>
    <activeProgram access="readWrite" validate="true" uid="1019" />
    <selectedProgram access="readWrite" fullOptionSet="false" uid="101A" />
    <protectionPort access="readWrite" available="true" uid="101B" />
    <enumerationTypeList>
        <enumerationType enid="3001">
            <enumeration value="0" />
            <enumeration value="1" />
            <enumeration value="2" />
        </enumerationType>
        <enumerationType enid="3003" subsetOf="3001">
            <enumeration value="1" />
        </enumerationType>
    </enumerationTypeList>
</device>`

const featureMappingShort = `<?xml version="1.0" encoding="utf-8"?>
<featureMappingFile>
  <featureDescription>
    <feature refUID="1001">Demo.Status.Alpha</feature>
    <feature refUID="1002">Demo.Status.Door</feature>
    <feature refUID="1005">Demo.Setting.Power</feature>
    <feature refUID="1006">Demo.Setting.Beta</feature>
    <feature refUID="1009">Demo.Event.One</feature>
    <feature refUID="100A">Demo.Event.Two</feature>
    <feature refUID="100B">Demo.Event.Three</feature>
    <feature refUID="100D">Demo.Command.One</feature>
    <feature refUID="1011">Demo.Option.One</feature>
    <feature refUID="1015">Demo.Program.Alpha</feature>
    <feature refUID="1019">Demo.Root.ActiveProgram</feature>
    <feature refUID="101A">Demo.Root.SelectedProgram</feature>
    <feature refUID="101B">Demo.Root.ProtectionPort</feature>
  </featureDescription>
  <errorDescription>
    <error refEID="2001">Demo.Error.One</error>
  </errorDescription>
  <enumDescriptionList>
    <enumDescription refENID="3001" enumKey="EventState">
      <enumMember refValue="0">Off</enumMember>
      <enumMember refValue="1">Present</enumMember>
      <enumMember refValue="2">Confirmed</enumMember>
    </enumDescription>
    <enumDescription refENID="3002" enumKey="DoorState">
      <enumMember refValue="0">Open</enumMember>
      <enumMember refValue="1">Closed</enumMember>
    </enumDescription>
  </enumDescriptionList>
</featureMappingFile>`

func mustParse(t *testing.T) *Description {
	t.Helper()
	d, err := ParseDescription([]byte(deviceDescriptionShort), []byte(featureMappingShort), nil)
	if err != nil {
		t.Fatalf("ParseDescription: %v", err)
	}
	return d
}

func TestParseDeviceInfo(t *testing.T) {
	d := mustParse(t)
	if d.Info.Brand != "DemoBrand" || d.Info.Version != 2 {
		t.Errorf("info = %+v", d.Info)
	}
}

func TestParseEnumStatus(t *testing.T) {
	d := mustParse(t)
	e, ok := d.ByUID(0x1002)
	if !ok {
		t.Fatal("uid 0x1002 not found")
	}
	if e.Name != "Demo.Status.Door" {
		t.Errorf("name = %q, want Demo.Status.Door", e.Name)
	}
	if e.ContentType != "enumeration" {
		t.Errorf("contentType = %q, want enumeration", e.ContentType)
	}
	if e.ProtocolType != ProtocolInteger {
		t.Errorf("protocolType = %q, want Integer", e.ProtocolType)
	}
	if !e.IsEnum() || e.Enumeration[0] != "Open" || e.Enumeration[1] != "Closed" {
		t.Errorf("enumeration = %v, want {0:Open,1:Closed}", e.Enumeration)
	}
}

func TestParseBooleanSetting(t *testing.T) {
	d := mustParse(t)
	e, ok := d.ByName("Demo.Setting.Power")
	if !ok {
		t.Fatal("Demo.Setting.Power not found")
	}
	if e.ProtocolType != ProtocolBoolean || e.ContentType != "boolean" {
		t.Errorf("types = %q/%q", e.ProtocolType, e.ContentType)
	}
	if e.Access != "readwrite" {
		t.Errorf("access = %q, want readwrite", e.Access)
	}
	if !e.Writable() {
		t.Error("Demo.Setting.Power should be writable")
	}
	if !e.HasMin || e.Min != 0 || !e.HasMax || e.Max != 10 || !e.HasStep || e.StepSize != 1 {
		t.Errorf("min/max/step = %v/%v/%v", e.Min, e.Max, e.StepSize)
	}
}

func TestParseEnumSubset(t *testing.T) {
	d := mustParse(t)
	subset, ok := d.Enumerations[0x3003]
	if !ok {
		t.Fatal("enid 0x3003 subset not built")
	}
	if len(subset) != 1 || subset[1] != "Present" {
		t.Errorf("subset = %v, want {1:Present}", subset)
	}
	// The event referencing 3003 must carry the subset enum.
	e, _ := d.ByUID(0x100B)
	if e.Enumeration[1] != "Present" || len(e.Enumeration) != 1 {
		t.Errorf("event 0x100B enum = %v", e.Enumeration)
	}
}

func TestParseProgram(t *testing.T) {
	d := mustParse(t)
	e, ok := d.ByUID(0x1015)
	if !ok {
		t.Fatal("program 0x1015 not found")
	}
	if e.Kind != KindProgram || e.Name != "Demo.Program.Alpha" {
		t.Errorf("program = %+v", e)
	}
	if e.Execution != "selectonly" {
		t.Errorf("execution = %q, want selectonly (lowercased)", e.Execution)
	}
	if len(e.Options) != 1 || e.Options[0].RefUID != 0x1011 || !e.Options[0].Default {
		t.Errorf("program options = %+v", e.Options)
	}
}

func TestParseNestedListsFlattened(t *testing.T) {
	d := mustParse(t)
	// 0x1001 and 0x1002 are in nested statusLists; 0x1006 in nested settingList.
	for _, uid := range []int{0x1001, 0x1002, 0x1006, 0x100A, 0x100B} {
		if _, ok := d.ByUID(uid); !ok {
			t.Errorf("nested uid 0x%X not flattened", uid)
		}
	}
}

func TestParseSingleElements(t *testing.T) {
	d := mustParse(t)
	for _, name := range []string{"Demo.Root.ActiveProgram", "Demo.Root.SelectedProgram", "Demo.Root.ProtectionPort"} {
		if _, ok := d.ByName(name); !ok {
			t.Errorf("single element %q not parsed", name)
		}
	}
}

// TestParseSkipsBadUID confirms per-entity isolation: an element with a
// non-hex uid is skipped, the rest of the device still parses (FK-3).
func TestParseSkipsBadUID(t *testing.T) {
	broken := `<?xml version="1.0"?><device>
      <description><type>X</type><brand>B</brand><model>M</model><version>1</version></description>
      <statusList uid="0001">
        <status access="read" refCID="01" uid="GARBAGE" />
        <status access="read" refCID="01" uid="1001" />
      </statusList>
    </device>`
	fm := `<featureMappingFile><featureDescription><feature refUID="1001">Demo.Status.Alpha</feature></featureDescription></featureMappingFile>`
	d, err := ParseDescription([]byte(broken), []byte(fm), nil)
	if err != nil {
		t.Fatalf("ParseDescription should tolerate bad element: %v", err)
	}
	if _, ok := d.ByUID(0x1001); !ok {
		t.Error("valid sibling 0x1001 should still be parsed")
	}
	if len(d.Entries) != 1 {
		t.Errorf("expected 1 entry (garbage skipped), got %d", len(d.Entries))
	}
}

func TestParseUppercaseExecution(t *testing.T) {
	// Live data sometimes arrives uppercased (#70); the parser lowercases.
	dd := `<?xml version="1.0"?><device>
      <description><type>X</type><brand>B</brand><model>M</model><version>1</version></description>
      <programGroup uid="000B"><program execution="SELECTANDSTART" uid="1015"/></programGroup>
    </device>`
	fm := `<featureMappingFile><featureDescription><feature refUID="1015">Demo.Program.Alpha</feature></featureDescription></featureMappingFile>`
	d, _ := ParseDescription([]byte(dd), []byte(fm), nil)
	e, _ := d.ByUID(0x1015)
	if e.Execution != "selectandstart" {
		t.Errorf("execution = %q, want selectandstart", e.Execution)
	}
}

func TestProtocolAndContentTypeTables(t *testing.T) {
	if ProtocolTypeFor(1) != ProtocolBoolean || ProtocolTypeFor(4) != ProtocolFloat {
		t.Error("protocol type table wrong")
	}
	if ProtocolTypeFor(130) != ProtocolObject {
		t.Error("list types (129..194) must be Object")
	}
	if ProtocolTypeFor(9999) != ProtocolString {
		t.Error("unknown refCID must default to String")
	}
	if ContentTypeFor(7) != "temperatureCelsius" || ContentTypeFor(17) != "percent" {
		t.Error("content type table wrong")
	}
}
