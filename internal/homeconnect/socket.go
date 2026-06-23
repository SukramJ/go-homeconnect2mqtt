// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// ConnectionType selects the transport, derived from the profile's
// connectionType field (docs/03-profil-format.md §3).
type ConnectionType string

// Connection types.
const (
	ConnectionAES ConnectionType = "AES"
	ConnectionTLS ConnectionType = "TLS"
)

// wsPath is the fixed WebSocket endpoint path on every appliance.
const wsPath = "/homeconnect"

// readLimit caps a single inbound frame. Appliance messages are small
// JSON blobs; this only guards against a misbehaving peer.
const readLimit = 1 << 20 // 1 MiB

// Socket is the transport contract the session layer talks to. Both the
// AES and TLS-PSK transports satisfy it; Send/Receive exchange already
// decoded UTF-8 message text.
type Socket interface {
	Connect(ctx context.Context) error
	Send(ctx context.Context, message string) error
	Receive(ctx context.Context) (string, error)
	Ping(ctx context.Context) error
	Close() error
}

// applianceURL builds the WebSocket URL for host, fixing the upstream
// IPv6 double-bracket bug (#409): existing brackets are stripped before
// wrapping, and a link-local zone id is percent-encoded per RFC 6874.
func applianceURL(host string, secure bool) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", cryptoErrf("empty host")
	}
	scheme, port := "ws", "80"
	if secure {
		scheme, port = "wss", "443"
	}
	h := host
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if strings.Contains(h, ":") { // IPv6 literal
		if i := strings.IndexByte(h, '%'); i >= 0 {
			h = h[:i] + "%25" + h[i+1:] // zone id
		}
		h = "[" + h + "]"
	}
	return fmt.Sprintf("%s://%s:%s%s", scheme, h, port, wsPath), nil
}

// AESSocket is the ws://host:80 transport with app-layer AES-CBC + HMAC
// crypto. mirrors hc_socket.AesSocket.
type AESSocket struct {
	url    string
	crypto *AESCrypto

	mu   sync.Mutex
	conn *websocket.Conn
}

var _ Socket = (*AESSocket)(nil)

// NewAESSocket builds an AES transport for host using the given psk/iv.
func NewAESSocket(host string, psk, iv []byte) (*AESSocket, error) {
	u, err := applianceURL(host, false)
	if err != nil {
		return nil, err
	}
	crypto, err := NewAESCrypto(psk, iv)
	if err != nil {
		return nil, err
	}
	return &AESSocket{url: u, crypto: crypto}, nil
}

// Connect dials the WebSocket and resets the crypto chains so a reconnect
// always starts from the static iv with zeroed HMAC chains.
func (s *AESSocket) Connect(ctx context.Context) error {
	conn, resp, err := websocket.Dial(ctx, s.url, &websocket.DialOptions{})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("homeconnect: dial %s: %w", s.url, err)
	}
	conn.SetReadLimit(readLimit)
	s.crypto.Reset()
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	return nil
}

func (s *AESSocket) currentConn() (*websocket.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, cryptoErrf("not connected")
	}
	return s.conn, nil
}

// Send encrypts message and writes it as a binary frame.
func (s *AESSocket) Send(ctx context.Context, message string) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	frame, err := s.crypto.Encrypt([]byte(message))
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageBinary, frame)
}

// Receive reads one frame and decrypts it. A non-binary frame is treated
// as a fatal desync (CryptoError) so the caller reconnects rather than
// reading on a chain that can no longer line up (docs/01 §3.4).
func (s *AESSocket) Receive(ctx context.Context) (string, error) {
	conn, err := s.currentConn()
	if err != nil {
		return "", err
	}
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("homeconnect: read: %w", err)
	}
	if typ != websocket.MessageBinary {
		return "", cryptoErrf("non-binary frame (%v)", typ)
	}
	plain, err := s.crypto.Decrypt(data)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Ping sends a WebSocket ping and waits for the pong, the heartbeat used
// to detect a dead peer (docs/01 §1). The pong is processed by the
// connection's read path, so a Receive loop must be running concurrently
// (as it always is in the session layer) for Ping to complete.
func (s *AESSocket) Ping(ctx context.Context) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	return conn.Ping(ctx)
}

// Close tears down the WebSocket.
func (s *AESSocket) Close() error {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close(websocket.StatusNormalClosure, "")
}
