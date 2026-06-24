// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Action is the message verb. mirrors message.py.
type Action string

// Action values.
const (
	ActionGet      Action = "GET"
	ActionPost     Action = "POST"
	ActionResponse Action = "RESPONSE"
	ActionNotify   Action = "NOTIFY"
	// ActionDelete clears the active program (hood fan-off, #386).
	ActionDelete Action = "DELETE"
)

// Message is one protocol frame, JSON-encoded over the WebSocket with
// compact separators (docs/01-protocol.md §5).
type Message struct {
	SID      int              `json:"sID"`
	MsgID    int              `json:"msgID"`
	Resource string           `json:"resource"`
	Version  int              `json:"version"`
	Action   Action           `json:"action"`
	Data     []map[string]any `json:"data,omitempty"`
	Code     *int             `json:"code,omitempty"`
}

// rawMessage mirrors Message but keeps the numeric fields as raw JSON so
// the defensive decoder can accept both numbers and quoted strings (some
// appliances send sID/msgID/version as strings).
type rawMessage struct {
	SID      json.RawMessage `json:"sID"`
	MsgID    json.RawMessage `json:"msgID"`
	Resource string          `json:"resource"`
	Version  json.RawMessage `json:"version"`
	Action   Action          `json:"action"`
	Data     json.RawMessage `json:"data"`
	Code     json.RawMessage `json:"code"`
}

// Encode serialises the message to compact JSON for the wire.
func (m *Message) Encode() ([]byte, error) {
	return json.Marshal(m)
}

// DecodeMessage parses a wire frame defensively: numeric fields are
// coerced via int(...), data is normalised to a list (a single object is
// wrapped), and a malformed object payload is retried after the upstream
// `]"`→`]` workaround.
func DecodeMessage(b []byte) (*Message, error) {
	var raw rawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		// Workaround for the known broken-object payload (docs/01 §5).
		fixed := bytes.ReplaceAll(b, []byte(`]"`), []byte(`]`))
		if err2 := json.Unmarshal(fixed, &raw); err2 != nil {
			return nil, fmt.Errorf("homeconnect: decode message: %w", err)
		}
	}
	m := &Message{
		SID:      coerceInt(raw.SID),
		MsgID:    coerceInt(raw.MsgID),
		Resource: raw.Resource,
		Version:  coerceInt(raw.Version),
		Action:   raw.Action,
	}
	if data, err := decodeData(raw.Data); err == nil {
		m.Data = data
	}
	if code, ok := optionalInt(raw.Code); ok {
		m.Code = &code
	}
	return m, nil
}

// decodeData normalises the data field to a slice of objects.
func decodeData(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	// A single object instead of a list.
	var single map[string]any
	if err := json.Unmarshal(raw, &single); err == nil {
		return []map[string]any{single}, nil
	}
	// Retry after the broken-object workaround.
	fixed := bytes.ReplaceAll(raw, []byte(`]"`), []byte(`]`))
	if err := json.Unmarshal(fixed, &list); err == nil {
		return list, nil
	}
	return nil, fmt.Errorf("homeconnect: decode data")
}

// coerceInt parses a JSON number or quoted-number into an int, returning 0
// on anything unparseable.
func coerceInt(raw json.RawMessage) int {
	v, _ := optionalInt(raw)
	return v
}

// optionalInt reports whether raw held a usable integer and its value.
func optionalInt(raw json.RawMessage) (int, bool) {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f), true
	}
	return 0, false
}
