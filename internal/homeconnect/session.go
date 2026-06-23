// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// SessionConfig parameterises a session.
type SessionConfig struct {
	AppName          string
	AppID            string
	SendTimeout      time.Duration
	HandshakeTimeout time.Duration
	Logger           *slog.Logger
}

// Session drives the message layer on top of a Socket: it performs the
// handshake, assigns msgIDs, correlates RESPONSEs and routes NOTIFYs.
// mirrors session.py (HCSession).
type Session struct {
	socket Socket
	cfg    SessionConfig
	logger *slog.Logger

	mu              sync.Mutex
	sID             int
	lastMsgID       int
	serviceVersions map[string]int

	pendingMu sync.Mutex
	pending   map[int]chan *Message

	initCh  chan *Message
	recvErr chan error

	notifyMu      sync.RWMutex
	notifyHandler func(*Message)
}

// NewSession builds a session around socket.
func NewSession(socket Socket, cfg SessionConfig) *Session {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SendTimeout == 0 {
		cfg.SendTimeout = 20 * time.Second
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 60 * time.Second
	}
	if cfg.AppName == "" {
		cfg.AppName = "go-homeconnect2mqtt"
	}
	return &Session{
		socket:          socket,
		cfg:             cfg,
		logger:          cfg.Logger,
		serviceVersions: map[string]int{},
		pending:         map[int]chan *Message{},
	}
}

// OnNotify registers the handler invoked for every non-response message
// once the session is connected (NOTIFY /ro/values etc.).
func (s *Session) OnNotify(fn func(*Message)) {
	s.notifyMu.Lock()
	s.notifyHandler = fn
	s.notifyMu.Unlock()
}

// ServiceVersion returns the negotiated version of a service ("ci", "ro").
func (s *Session) ServiceVersion(service string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.serviceVersions[service]
	return v, ok
}

// Connect dials the socket, starts the receive loop and runs the full
// handshake. It returns once the appliance is CONNECTED and post-init has
// been attempted.
func (s *Session) Connect(ctx context.Context) error {
	if err := s.socket.Connect(ctx); err != nil {
		return err
	}
	s.resetState()
	go s.receiveLoop(ctx)
	return s.handshake(ctx)
}

// Close tears down the socket.
func (s *Session) Close() error { return s.socket.Close() }

func (s *Session) resetState() {
	s.mu.Lock()
	s.sID = 0
	s.lastMsgID = 0
	s.serviceVersions = map[string]int{}
	s.mu.Unlock()

	s.pendingMu.Lock()
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.pending = map[int]chan *Message{}
	s.pendingMu.Unlock()

	s.initCh = make(chan *Message, 1)
	s.recvErr = make(chan error, 1)
}

// receiveLoop reads frames and dispatches them until the socket errors or
// the context is cancelled.
func (s *Session) receiveLoop(ctx context.Context) {
	for {
		text, err := s.socket.Receive(ctx)
		if err != nil {
			select {
			case s.recvErr <- err:
			default:
			}
			s.failPending(err)
			return
		}
		msg, err := DecodeMessage([]byte(text))
		if err != nil {
			s.logger.Warn("homeconnect.decode", slog.String("err", err.Error()))
			continue
		}
		s.dispatch(msg)
	}
}

func (s *Session) dispatch(msg *Message) {
	if msg.Action == ActionResponse {
		if ch := s.takePending(msg.MsgID); ch != nil {
			ch <- msg
			return
		}
	}
	if msg.Resource == "/ei/initialValues" && msg.Action == ActionPost {
		select {
		case s.initCh <- msg:
		default:
		}
		return
	}
	s.notifyMu.RLock()
	h := s.notifyHandler
	s.notifyMu.RUnlock()
	if h != nil {
		h(msg)
	}
}

func (s *Session) takePending(msgID int) chan *Message {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	ch, ok := s.pending[msgID]
	if ok {
		delete(s.pending, msgID)
	}
	return ch
}

func (s *Session) failPending(err error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	_ = err
}

// handshake runs the exact sequence from docs/01-protokoll.md §6.
func (s *Session) handshake(ctx context.Context) error {
	// 1. Await the device-initiated /ei/initialValues.
	var initMsg *Message
	select {
	case initMsg = <-s.initCh:
	case err := <-s.recvErr:
		return fmt.Errorf("homeconnect: handshake await initialValues: %w", err)
	case <-time.After(s.cfg.HandshakeTimeout):
		return errors.New("homeconnect: handshake timeout awaiting initialValues")
	case <-ctx.Done():
		return ctx.Err()
	}

	s.mu.Lock()
	s.sID = initMsg.SID
	s.lastMsgID = firstInt(initMsg.Data, "edMsgID")
	eiVersion := initMsg.Version
	s.mu.Unlock()

	// 2. RESPONSE to initialValues, reusing the server msgID.
	deviceType := any("Application")
	if eiVersion == 1 {
		deviceType = 2
	}
	resp := &Message{
		SID:      initMsg.SID,
		MsgID:    initMsg.MsgID,
		Resource: "/ei/initialValues",
		Version:  eiVersion,
		Action:   ActionResponse,
		Data: []map[string]any{{
			"deviceType": deviceType,
			"deviceName": s.cfg.AppName,
			"deviceID":   s.appID(),
		}},
	}
	if err := s.send(ctx, resp); err != nil {
		return fmt.Errorf("homeconnect: send initialValues response: %w", err)
	}

	// 3. GET /ci/services.
	servicesResp, err := s.sendSync(ctx, &Message{Resource: "/ci/services", Version: 1, Action: ActionGet})
	if err != nil {
		return fmt.Errorf("homeconnect: get services: %w", err)
	}
	s.storeServices(servicesResp)

	ciVersion, _ := s.ServiceVersion("ci")

	// 4. ci < 3: authentication + info.
	if ciVersion < 3 {
		nonce, nerr := randomNonce()
		if nerr != nil {
			return nerr
		}
		if _, aerr := s.sendSync(ctx, &Message{
			Resource: "/ci/authentication", Version: ciVersion, Action: ActionGet,
			Data: []map[string]any{{"nonce": nonce}},
		}); aerr != nil {
			return fmt.Errorf("homeconnect: authentication: %w", aerr)
		}
		// /ci/info errors are tolerated.
		if _, ierr := s.sendSync(ctx, &Message{Resource: "/ci/info", Action: ActionGet}); ierr != nil {
			s.logger.Debug("homeconnect.ci_info", slog.String("err", ierr.Error()))
		}
	}

	// 5. iz/info if present.
	if _, ok := s.ServiceVersion("iz"); ok {
		if _, ierr := s.sendSync(ctx, &Message{Resource: "/iz/info", Action: ActionGet}); ierr != nil {
			s.logger.Debug("homeconnect.iz_info", slog.String("err", ierr.Error()))
		}
	}

	// 6. ei v2: deviceReady (fire-and-forget).
	if eiVersion == 2 {
		if err := s.send(ctx, &Message{Resource: "/ei/deviceReady", Version: eiVersion, Action: ActionNotify}); err != nil {
			s.logger.Debug("homeconnect.device_ready", slog.String("err", err.Error()))
		}
	}

	// 7. ni/info if present.
	if _, ok := s.ServiceVersion("ni"); ok {
		if _, ierr := s.sendSync(ctx, &Message{Resource: "/ni/info", Action: ActionGet}); ierr != nil {
			s.logger.Debug("homeconnect.ni_info", slog.String("err", ierr.Error()))
		}
	}
	return nil
}

// PostConnectInit fetches the bulk description + mandatory values. A 500
// is tolerated (docs/01 §6.3) and surfaced to the caller as nil so the
// connection stays up. Returns the two response messages (either may be
// nil on a tolerated error).
func (s *Session) PostConnectInit(ctx context.Context) (descChanges, mandatory *Message, err error) {
	descChanges, err = s.sendSync(ctx, &Message{Resource: "/ro/allDescriptionChanges", Action: ActionGet})
	if err != nil {
		if tolerableInit(err) {
			s.logger.Warn("homeconnect.allDescriptionChanges", slog.String("err", err.Error()))
			descChanges = nil
		} else {
			return nil, nil, err
		}
	}
	mandatory, err = s.sendSync(ctx, &Message{Resource: "/ro/allMandatoryValues", Action: ActionGet})
	if err != nil {
		if tolerableInit(err) {
			s.logger.Warn("homeconnect.allMandatoryValues", slog.String("err", err.Error()))
			mandatory = nil
		} else {
			return descChanges, nil, err
		}
	}
	return descChanges, mandatory, nil
}

// tolerableInit reports whether a post-init error should be logged and
// swallowed rather than failing the connection (500 InternalServerError).
func tolerableInit(err error) bool {
	var ce *CodeResponseError
	return errors.As(err, &ce) && ce.Code >= 500
}

// WriteValues issues a POST /ro/values with the given data items and
// returns the correlated response (docs/01-protokoll.md §7).
func (s *Session) WriteValues(ctx context.Context, data []map[string]any) (*Message, error) {
	return s.sendSync(ctx, &Message{Resource: "/ro/values", Action: ActionPost, Data: data})
}

// SendRaw issues an arbitrary request and returns the correlated response;
// used by the command layer for program-start paths (P7).
func (s *Session) SendRaw(ctx context.Context, m *Message) (*Message, error) {
	return s.sendSync(ctx, m)
}

// send resolves defaults for zero-valued fields and writes the message.
func (s *Session) send(ctx context.Context, m *Message) error {
	s.resolveDefaults(m, true)
	b, err := m.Encode()
	if err != nil {
		return err
	}
	return s.socket.Send(ctx, string(b))
}

// sendSync sends a request and waits for the correlated RESPONSE, applying
// the send timeout. A non-nil response code becomes a *CodeResponseError.
func (s *Session) sendSync(ctx context.Context, m *Message) (*Message, error) {
	s.resolveDefaults(m, true)
	ch := make(chan *Message, 1)
	s.pendingMu.Lock()
	s.pending[m.MsgID] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, m.MsgID)
		s.pendingMu.Unlock()
	}()

	b, err := m.Encode()
	if err != nil {
		return nil, err
	}
	if err := s.socket.Send(ctx, string(b)); err != nil {
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("homeconnect: connection closed awaiting response")
		}
		if cerr := codeError(resp); cerr != nil {
			return resp, cerr
		}
		return resp, nil
	case <-time.After(s.cfg.SendTimeout):
		return nil, fmt.Errorf("homeconnect: response timeout for %s (msgID %d)", m.Resource, m.MsgID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// resolveDefaults fills version/sID/msgID for zero-valued fields
// (docs/01 §6.4). assignMsgID controls whether a fresh, monotonic msgID is
// allocated when one is not already set.
func (s *Session) resolveDefaults(m *Message, assignMsgID bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Version == 0 {
		if v, ok := s.serviceVersions[serviceKey(m.Resource)]; ok {
			m.Version = v
		} else {
			m.Version = 1
		}
	}
	if m.SID == 0 {
		m.SID = s.sID
	}
	if m.MsgID == 0 && assignMsgID {
		m.MsgID = s.lastMsgID
		s.lastMsgID++
	}
}

func (s *Session) storeServices(resp *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range resp.Data {
		name, _ := item["service"].(string)
		if name == "" {
			continue
		}
		version := 1
		if v, ok := item["version"]; ok {
			version = anyToInt(v)
		}
		s.serviceVersions[name] = version
	}
}

func (s *Session) appID() string {
	if s.cfg.AppID != "" {
		return s.cfg.AppID
	}
	id, err := randomHexID()
	if err != nil {
		return "0000000000000000"
	}
	return id
}

// serviceKey extracts the 2-char service key from a resource path
// ("/ci/services" -> "ci").
func serviceKey(resource string) string {
	r := strings.TrimPrefix(resource, "/")
	if len(r) >= 2 {
		return r[:2]
	}
	return r
}

func firstInt(data []map[string]any, key string) int {
	if len(data) == 0 {
		return 0
	}
	return anyToInt(data[0][key])
}

func anyToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		return coerceInt([]byte(n))
	default:
		return 0
	}
}

func randomNonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("homeconnect: nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomHexID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	const hexdigits = "0123456789ABCDEF"
	out := make([]byte, 16)
	for i, b := range buf {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0F]
	}
	return string(out), nil
}
