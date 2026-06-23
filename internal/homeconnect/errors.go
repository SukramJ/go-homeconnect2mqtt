// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package homeconnect implements the local Home Connect WebSocket
// protocol: AES app-layer crypto and TLS-PSK transports, the message
// session/handshake, the entity model and the reconnect state machine.
// It is a Go port of chris-mc1/homeconnect_websocket (Python, v1.5.3);
// where behaviour is mirrored 1:1 the source file is cited in a comment.
package homeconnect

import (
	"errors"
	"fmt"
)

// CryptoError signals a fatal desynchronisation of the AES stream:
// an HMAC mismatch, a malformed frame or a padding/decode failure. The
// rolling HMAC and CBC chains cannot recover in-stream (see
// docs/01-protokoll.md §3.4, the root cause of upstream bug #62), so the
// only correct response is to close the socket and fully reconnect.
type CryptoError struct {
	Reason string
	Err    error
}

// Error implements error.
func (e *CryptoError) Error() string {
	if e.Err != nil {
		return "homeconnect: crypto: " + e.Reason + ": " + e.Err.Error()
	}
	return "homeconnect: crypto: " + e.Reason
}

// Unwrap exposes the wrapped cause.
func (e *CryptoError) Unwrap() error { return e.Err }

// IsCryptoError reports whether err (or anything it wraps) is a
// *CryptoError, i.e. the caller must force a full reconnect.
func IsCryptoError(err error) bool {
	var ce *CryptoError
	return errors.As(err, &ce)
}

func cryptoErrf(format string, args ...any) *CryptoError {
	return &CryptoError{Reason: fmt.Sprintf(format, args...)}
}
