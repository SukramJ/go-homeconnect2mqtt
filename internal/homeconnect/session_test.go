// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedSocket is a message-level fake Socket: it plays the appliance
// side by enqueuing server frames on connect and reacting to each client
// message via responder.
type scriptedSocket struct {
	onConnect func() []*Message
	responder func(*Message) []*Message

	inbound chan string
	done    chan struct{}

	mu   sync.Mutex
	sent []*Message
}

func newScriptedSocket(onConnect func() []*Message, responder func(*Message) []*Message) *scriptedSocket {
	return &scriptedSocket{
		onConnect: onConnect,
		responder: responder,
		inbound:   make(chan string, 64),
		done:      make(chan struct{}),
	}
}

func (f *scriptedSocket) enqueue(msgs []*Message) {
	for _, m := range msgs {
		b, _ := m.Encode()
		f.inbound <- string(b)
	}
}

func (f *scriptedSocket) Connect(_ context.Context) error {
	if f.onConnect != nil {
		f.enqueue(f.onConnect())
	}
	return nil
}

func (f *scriptedSocket) Send(_ context.Context, message string) error {
	msg, err := DecodeMessage([]byte(message))
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	if f.responder != nil {
		f.enqueue(f.responder(msg))
	}
	return nil
}

func (f *scriptedSocket) Receive(ctx context.Context) (string, error) {
	select {
	case t := <-f.inbound:
		return t, nil
	case <-f.done:
		return "", errors.New("socket closed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *scriptedSocket) Ping(context.Context) error { return nil }

func (f *scriptedSocket) Close() error {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
	return nil
}

func (f *scriptedSocket) sentResources() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	for i, m := range f.sent {
		out[i] = m.Resource
	}
	return out
}

func respondOK(req *Message, data []map[string]any) *Message {
	return &Message{SID: req.SID, MsgID: req.MsgID, Resource: req.Resource, Version: req.Version, Action: ActionResponse, Data: data}
}

// applianceScript builds a server that completes the handshake. ciVersion
// controls whether the authentication branch is exercised; services lists
// the advertised services.
func applianceScript(ciVersion int, services []map[string]any) (func() []*Message, func(*Message) []*Message) {
	onConnect := func() []*Message {
		return []*Message{{
			SID: 42, MsgID: 1000, Resource: "/ei/initialValues", Version: 2, Action: ActionPost,
			Data: []map[string]any{{"edMsgID": 500}},
		}}
	}
	responder := func(req *Message) []*Message {
		switch req.Resource {
		case "/ei/initialValues":
			return nil // client RESPONSE, no reply
		case "/ci/services":
			return []*Message{respondOK(req, services)}
		case "/ci/authentication":
			return []*Message{respondOK(req, []map[string]any{{"nonce": "server-nonce"}})}
		case "/ci/info", "/iz/info", "/ni/info":
			return []*Message{respondOK(req, []map[string]any{{"deviceID": "X"}})}
		case "/ro/allDescriptionChanges", "/ro/allMandatoryValues":
			return []*Message{respondOK(req, []map[string]any{{"uid": 4133, "value": 1}})}
		default:
			return nil
		}
	}
	_ = ciVersion
	return onConnect, responder
}

func newTestSession(t *testing.T, sock Socket) *Session {
	t.Helper()
	return NewSession(sock, SessionConfig{
		AppName:          "test-app",
		AppID:            "DEADBEEF",
		SendTimeout:      2 * time.Second,
		HandshakeTimeout: 2 * time.Second,
	})
}

func TestHandshakeCIv1(t *testing.T) {
	services := []map[string]any{
		{"service": "ci", "version": 1},
		{"service": "ro", "version": 1},
		{"service": "ni", "version": 1},
	}
	onConnect, responder := applianceScript(1, services)
	sock := newScriptedSocket(onConnect, responder)
	s := newTestSession(t, sock)

	if err := s.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	if v, _ := s.ServiceVersion("ci"); v != 1 {
		t.Errorf("ci version = %d, want 1", v)
	}
	s.mu.Lock()
	sid := s.sID
	s.mu.Unlock()
	if sid != 42 {
		t.Errorf("sID = %d, want 42", sid)
	}

	res := sock.sentResources()
	// ci v1 must exercise authentication + ci/info.
	if !contains(res, "/ci/authentication") || !contains(res, "/ci/info") {
		t.Errorf("ci<3 path not taken: %v", res)
	}
	if !contains(res, "/ni/info") {
		t.Errorf("ni/info not requested: %v", res)
	}
}

func TestHandshakeCIv3SkipsAuth(t *testing.T) {
	services := []map[string]any{
		{"service": "ci", "version": 3},
		{"service": "ro", "version": 1},
	}
	onConnect, responder := applianceScript(3, services)
	sock := newScriptedSocket(onConnect, responder)
	s := newTestSession(t, sock)

	if err := s.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	if contains(sock.sentResources(), "/ci/authentication") {
		t.Errorf("ci>=3 must skip authentication: %v", sock.sentResources())
	}
}

func TestPostConnectInitTolerates500(t *testing.T) {
	services := []map[string]any{{"service": "ci", "version": 3}, {"service": "ro", "version": 1}}
	onConnect, base := applianceScript(3, services)
	responder := func(req *Message) []*Message {
		if req.Resource == "/ro/allMandatoryValues" {
			code := 500
			return []*Message{{SID: req.SID, MsgID: req.MsgID, Resource: req.Resource, Action: ActionResponse, Code: &code}}
		}
		return base(req)
	}
	sock := newScriptedSocket(onConnect, responder)
	s := newTestSession(t, sock)
	if err := s.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	desc, mand, err := s.PostConnectInit(t.Context())
	if err != nil {
		t.Fatalf("PostConnectInit must tolerate 500, got %v", err)
	}
	if desc == nil {
		t.Error("allDescriptionChanges should have succeeded")
	}
	if mand != nil {
		t.Error("allMandatoryValues returned 500 -> should be nil")
	}
}

func TestSendSyncTimeout(t *testing.T) {
	services := []map[string]any{{"service": "ci", "version": 3}, {"service": "ro", "version": 1}}
	onConnect, base := applianceScript(3, services)
	responder := func(req *Message) []*Message {
		if req.Resource == "/ro/values" {
			return nil // never answer
		}
		return base(req)
	}
	sock := newScriptedSocket(onConnect, responder)
	s := NewSession(sock, SessionConfig{SendTimeout: 150 * time.Millisecond, HandshakeTimeout: 2 * time.Second})
	if err := s.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, err := s.sendSync(t.Context(), &Message{Resource: "/ro/values", Action: ActionGet})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestNotifyRouting(t *testing.T) {
	services := []map[string]any{{"service": "ci", "version": 3}, {"service": "ro", "version": 1}}
	onConnect, responder := applianceScript(3, services)
	sock := newScriptedSocket(onConnect, responder)
	s := newTestSession(t, sock)

	got := make(chan *Message, 1)
	s.OnNotify(func(m *Message) {
		if m.Resource == "/ro/values" {
			got <- m
		}
	})
	if err := s.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Server pushes an unsolicited NOTIFY.
	sock.enqueue([]*Message{{Resource: "/ro/values", Action: ActionNotify, Data: []map[string]any{{"uid": 1, "value": 7}}}})
	select {
	case m := <-got:
		if m.Data[0]["value"].(float64) != 7 {
			t.Errorf("notify value = %v", m.Data[0]["value"])
		}
	case <-time.After(time.Second):
		t.Fatal("notify not routed to handler")
	}
}

func TestHandshakeTimeoutNoInitialValues(t *testing.T) {
	sock := newScriptedSocket(func() []*Message { return nil }, func(*Message) []*Message { return nil })
	s := NewSession(sock, SessionConfig{HandshakeTimeout: 120 * time.Millisecond, SendTimeout: time.Second})
	err := s.Connect(t.Context())
	if err == nil {
		t.Fatal("expected handshake timeout")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
