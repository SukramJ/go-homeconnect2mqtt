// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"encoding/json"
	"testing"
)

func TestEncodeCompact(t *testing.T) {
	m := &Message{
		SID: 1, MsgID: 2, Resource: "/ro/values", Version: 1, Action: ActionPost,
		Data: []map[string]any{{"uid": 256, "value": 7}},
	}
	b, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Compact: no spaces after separators.
	if json.Valid(b) == false {
		t.Fatal("encoded output is not valid JSON")
	}
	for i := 0; i+1 < len(b); i++ {
		if (b[i] == ',' || b[i] == ':') && b[i+1] == ' ' {
			t.Fatalf("non-compact separator in %s", b)
		}
	}
}

func TestEncodeOmitsCodeAndEmptyData(t *testing.T) {
	m := &Message{SID: 1, MsgID: 2, Resource: "/ci/services", Version: 1, Action: ActionGet}
	b, _ := m.Encode()
	s := string(b)
	if containsSubstr(s, "\"code\"") {
		t.Errorf("code should be omitted: %s", s)
	}
	if containsSubstr(s, "\"data\"") {
		t.Errorf("empty data should be omitted: %s", s)
	}
}

func TestDecodeStringNumbers(t *testing.T) {
	// sID/msgID/version delivered as quoted strings.
	in := `{"sID":"42","msgID":"7","resource":"/ro/values","version":"2","action":"NOTIFY","data":[{"uid":1}]}`
	m, err := DecodeMessage([]byte(in))
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if m.SID != 42 || m.MsgID != 7 || m.Version != 2 {
		t.Errorf("coerced ints = %d/%d/%d, want 42/7/2", m.SID, m.MsgID, m.Version)
	}
	if m.Action != ActionNotify {
		t.Errorf("action = %q", m.Action)
	}
}

func TestDecodeSingleObjectData(t *testing.T) {
	// data is a single object instead of a list.
	in := `{"sID":1,"msgID":1,"resource":"/x","version":1,"action":"RESPONSE","data":{"uid":9,"value":3}}`
	m, err := DecodeMessage([]byte(in))
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if len(m.Data) != 1 || anyToInt(m.Data[0]["uid"]) != 9 {
		t.Errorf("single-object data not normalised: %+v", m.Data)
	}
}

func TestDecodeCodePresent(t *testing.T) {
	in := `{"sID":1,"msgID":1,"resource":"/x","version":1,"action":"RESPONSE","code":541}`
	m, err := DecodeMessage([]byte(in))
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if m.Code == nil || *m.Code != 541 {
		t.Fatalf("code = %v, want 541", m.Code)
	}
	if err := codeError(m); err == nil {
		t.Error("codeError should be non-nil for code 541")
	}
}

func TestDecodeNoCode(t *testing.T) {
	in := `{"sID":1,"msgID":1,"resource":"/x","version":1,"action":"RESPONSE"}`
	m, _ := DecodeMessage([]byte(in))
	if m.Code != nil {
		t.Errorf("code should be nil, got %v", *m.Code)
	}
	if err := codeError(m); err != nil {
		t.Errorf("codeError should be nil, got %v", err)
	}
}

func TestCodeName(t *testing.T) {
	if CodeName(541) != "ProcessStateNotCompliant" {
		t.Errorf("CodeName(541) = %q", CodeName(541))
	}
	if CodeName(999) != "Unknown" {
		t.Errorf("CodeName(999) = %q", CodeName(999))
	}
}

func TestServiceKey(t *testing.T) {
	cases := map[string]string{"/ci/services": "ci", "/ro/values": "ro", "/x": "x"}
	for res, want := range cases {
		if got := serviceKey(res); got != want {
			t.Errorf("serviceKey(%q) = %q, want %q", res, got, want)
		}
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
