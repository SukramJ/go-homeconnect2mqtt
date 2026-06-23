// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"context"
	"errors"
	"sync"
)

// ErrTLSPSKUnsupported is returned when a TLS-PSK connection is attempted
// in a build without TLS-PSK support. Go's crypto/tls has no external
// TLS-1.2 PSK ciphers, so this transport requires the cgo `tlspsk` build
// (OpenSSL-backed); see docs/01-protokoll.md §4 and the Makefile.
var ErrTLSPSKUnsupported = errors.New("homeconnect: TLS-PSK transport requires the 'tlspsk' (cgo) build")

// tlsConn is a connected TLS-PSK tunnel with the WebSocket upgrade already
// performed. Messages are plain UTF-8 text (TLS protects everything;
// docs/01-protokoll.md §4).
type tlsConn interface {
	send(ctx context.Context, message string) error
	receive(ctx context.Context) (string, error)
	ping(ctx context.Context) error
	close() error
}

// tlsPSKConnect establishes the TLS-PSK tunnel + WebSocket upgrade to a
// Home Connect appliance. The default build returns ErrTLSPSKUnsupported;
// the cgo build (build tag tlspsk) overrides this at init with an
// OpenSSL-backed implementation.
var tlsPSKConnect = func(_ context.Context, _ string, _ []byte) (tlsConn, error) {
	return nil, ErrTLSPSKUnsupported
}

// TLSSocket is the wss://host:443 transport for older appliances.
// mirrors hc_socket.TlsSocket.
type TLSSocket struct {
	host string
	psk  []byte

	mu   sync.Mutex
	conn tlsConn
}

var _ Socket = (*TLSSocket)(nil)

// NewTLSSocket builds a TLS-PSK transport for host with the given psk.
func NewTLSSocket(host string, psk []byte) (*TLSSocket, error) {
	if host == "" {
		return nil, cryptoErrf("empty host")
	}
	return &TLSSocket{host: host, psk: psk}, nil
}

// Connect establishes the TLS-PSK tunnel. In the default build this fails
// fast with ErrTLSPSKUnsupported, which a device worker treats as
// "offline" — it never aborts the other (AES) devices.
func (s *TLSSocket) Connect(ctx context.Context) error {
	conn, err := tlsPSKConnect(ctx, s.host, s.psk)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
	return nil
}

func (s *TLSSocket) current() (tlsConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, cryptoErrf("not connected")
	}
	return s.conn, nil
}

// Send writes a message as a text frame over the tunnel.
func (s *TLSSocket) Send(ctx context.Context, message string) error {
	c, err := s.current()
	if err != nil {
		return err
	}
	return c.send(ctx, message)
}

// Receive reads one text frame from the tunnel.
func (s *TLSSocket) Receive(ctx context.Context) (string, error) {
	c, err := s.current()
	if err != nil {
		return "", err
	}
	return c.receive(ctx)
}

// Ping sends a WebSocket ping over the tunnel.
func (s *TLSSocket) Ping(ctx context.Context) error {
	c, err := s.current()
	if err != nil {
		return err
	}
	return c.ping(ctx)
}

// Close tears down the tunnel.
func (s *TLSSocket) Close() error {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.close()
}

// NewSocket selects the transport for a device: AES app-layer crypto on
// port 80 (iv required), or TLS-PSK on port 443.
func NewSocket(connType ConnectionType, host string, psk, iv []byte) (Socket, error) {
	switch connType {
	case ConnectionAES:
		return NewAESSocket(host, psk, iv)
	case ConnectionTLS:
		return NewTLSSocket(host, psk)
	default:
		return nil, cryptoErrf("unknown connection type %q", connType)
	}
}
