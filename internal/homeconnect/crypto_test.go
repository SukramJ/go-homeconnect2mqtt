// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"testing"
)

// testPSK / testIV are fixed, non-secret vectors for deterministic tests.
var (
	testPSK = bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 8) // 32 bytes
	testIV  = bytes.Repeat([]byte{0xAA, 0xBB}, 8)             // 16 bytes
)

// zeroReader yields an endless stream of 0x00, making padding deterministic.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func newDetCrypto(t *testing.T) *AESCrypto {
	t.Helper()
	c, err := NewAESCrypto(testPSK, testIV)
	if err != nil {
		t.Fatalf("NewAESCrypto: %v", err)
	}
	c.rand = zeroReader{}
	return c
}

func TestDeriveKeysMatchHMAC(t *testing.T) {
	enc, mac := deriveKeys(testPSK)
	wantEnc := hmac.New(sha256.New, testPSK)
	wantEnc.Write([]byte("ENC"))
	if !bytes.Equal(enc, wantEnc.Sum(nil)) {
		t.Error("enckey != HMAC-SHA256(psk, ENC)")
	}
	wantMac := hmac.New(sha256.New, testPSK)
	wantMac.Write([]byte("MAC"))
	if !bytes.Equal(mac, wantMac.Sum(nil)) {
		t.Error("mackey != HMAC-SHA256(psk, MAC)")
	}
	if len(enc) != 32 || len(mac) != 32 {
		t.Errorf("key lengths = %d/%d, want 32/32", len(enc), len(mac))
	}
}

// TestPaddingVectors verifies the exact pad-length byte + total length for
// the four cases documented in docs/01-protocol.md §3.2.
func TestPaddingVectors(t *testing.T) {
	c := newDetCrypto(t)
	cases := []struct {
		clearLen   int
		wantPadLen byte
		wantTotal  int
	}{
		{0, 16, 16},
		{1, 15, 16},
		{15, 17, 32},
		{16, 16, 32},
	}
	for _, tc := range cases {
		padded, err := c.pad(bytes.Repeat([]byte{'x'}, tc.clearLen))
		if err != nil {
			t.Fatalf("pad(%d): %v", tc.clearLen, err)
		}
		if len(padded) != tc.wantTotal {
			t.Errorf("pad(%d) total = %d, want %d", tc.clearLen, len(padded), tc.wantTotal)
		}
		if got := padded[len(padded)-1]; got != tc.wantPadLen {
			t.Errorf("pad(%d) pad_len = %d, want %d", tc.clearLen, got, tc.wantPadLen)
		}
		if padded[tc.clearLen] != 0x00 {
			t.Errorf("pad(%d) first pad byte = %d, want 0", tc.clearLen, padded[tc.clearLen])
		}
	}
}

// TestRoundTripMultiFrame exercises both directions over several frames so
// the CBC stream chaining and the rolling HMAC chain are both verified.
func TestRoundTripMultiFrame(t *testing.T) {
	client, _ := NewAESCrypto(testPSK, testIV)
	server, _ := NewAESCrypto(testPSK, testIV)

	clientMsgs := []string{"hello", strings.Repeat("A", 31), "{\"sID\":1}", ""}
	for i, msg := range clientMsgs {
		frame, err := client.Encrypt([]byte(msg))
		if err != nil {
			t.Fatalf("client.Encrypt[%d]: %v", i, err)
		}
		// Server verifies what the client signed with 'E'.
		got, err := server.open(encryptDirection, frame)
		if err != nil {
			t.Fatalf("server.open[%d]: %v", i, err)
		}
		if string(got) != msg {
			t.Errorf("frame %d round-trip = %q, want %q", i, got, msg)
		}
	}

	// Server -> client direction ('C').
	serverMsgs := []string{"resp1", strings.Repeat("z", 40)}
	for i, msg := range serverMsgs {
		frame, err := server.seal(decryptDirection, []byte(msg))
		if err != nil {
			t.Fatalf("server.seal[%d]: %v", i, err)
		}
		got, err := client.Decrypt(frame)
		if err != nil {
			t.Fatalf("client.Decrypt[%d]: %v", i, err)
		}
		if string(got) != msg {
			t.Errorf("server frame %d = %q, want %q", i, got, msg)
		}
	}
}

// TestTamperedFrameRejected confirms a flipped byte fails the HMAC check.
func TestTamperedFrameRejected(t *testing.T) {
	client, _ := NewAESCrypto(testPSK, testIV)
	server, _ := NewAESCrypto(testPSK, testIV)
	frame, _ := client.Encrypt([]byte("payload"))
	frame[0] ^= 0xFF
	_, err := server.open(encryptDirection, frame)
	if err == nil {
		t.Fatal("tampered frame accepted")
	}
	if !IsCryptoError(err) {
		t.Errorf("err is not *CryptoError: %v", err)
	}
}

// TestDesyncOnSkippedFrame shows that a lost frame permanently desyncs the
// chain: after decoding f1, decoding f3 (skipping f2) fails — the root of
// upstream bug #62 and the reason a desync forces a full reconnect.
func TestDesyncOnSkippedFrame(t *testing.T) {
	client, _ := NewAESCrypto(testPSK, testIV)
	server, _ := NewAESCrypto(testPSK, testIV)
	f1, _ := client.Encrypt([]byte("frame-one"))
	_, _ = client.Encrypt([]byte("frame-two")) // f2 never delivered
	f3, _ := client.Encrypt([]byte("frame-three"))

	if _, err := server.open(encryptDirection, f1); err != nil {
		t.Fatalf("f1 should decode: %v", err)
	}
	if _, err := server.open(encryptDirection, f3); err == nil {
		t.Fatal("f3 decoded despite skipped f2 (chain not desynced)")
	}
}

func TestFrameTooShortAndUnaligned(t *testing.T) {
	c, _ := NewAESCrypto(testPSK, testIV)
	if _, err := c.Decrypt(make([]byte, 16)); err == nil {
		t.Error("16-byte frame accepted (< minimum 32)")
	}
	if _, err := c.Decrypt(make([]byte, 33)); err == nil {
		t.Error("unaligned frame accepted")
	}
}

func TestResetRestartsChain(t *testing.T) {
	c := newDetCrypto(t)
	first, _ := c.Encrypt([]byte("repeatable"))
	_, _ = c.Encrypt([]byte("advance the chain"))
	c.Reset()
	again, _ := c.Encrypt([]byte("repeatable"))
	if !bytes.Equal(first, again) {
		t.Error("Reset did not restart the TX chain to the initial state")
	}
}

func TestNewAESCryptoBadIV(t *testing.T) {
	if _, err := NewAESCrypto(testPSK, []byte{0x01}); err == nil {
		t.Error("expected error for short iv")
	}
}

func TestUnpadInvalid(t *testing.T) {
	if _, err := unpad(nil); err == nil {
		t.Error("empty plaintext accepted")
	}
	if _, err := unpad([]byte{0x00, 0xFF}); err == nil {
		t.Error("over-long pad length accepted")
	}
}

func TestDecodeKey(t *testing.T) {
	// "AAAA" url-safe base64 (no padding) decodes to 3 zero bytes.
	got, err := DecodeKey("AAAA")
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	if !bytes.Equal(got, []byte{0, 0, 0}) {
		t.Errorf("DecodeKey = %v", got)
	}
}

func TestApplianceURL(t *testing.T) {
	cases := []struct {
		host   string
		secure bool
		want   string
	}{
		{"192.168.1.50", false, "ws://192.168.1.50:80/homeconnect"},
		{"myhost", true, "wss://myhost:443/homeconnect"},
		{"2a0a::1", false, "ws://[2a0a::1]:80/homeconnect"},
		{"[2a0a::1]", false, "ws://[2a0a::1]:80/homeconnect"}, // no double bracket (#409)
		{"fe80::1%eth0", false, "ws://[fe80::1%25eth0]:80/homeconnect"},
	}
	for _, tc := range cases {
		got, err := applianceURL(tc.host, tc.secure)
		if err != nil {
			t.Fatalf("applianceURL(%q): %v", tc.host, err)
		}
		if got != tc.want {
			t.Errorf("applianceURL(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
	if _, err := applianceURL("  ", false); err == nil {
		t.Error("empty host accepted")
	}
}

func TestNewAESSocketBuildsURL(t *testing.T) {
	s, err := NewAESSocket("192.168.1.50", testPSK, testIV)
	if err != nil {
		t.Fatalf("NewAESSocket: %v", err)
	}
	if s.url != "ws://192.168.1.50:80/homeconnect" {
		t.Errorf("url = %q", s.url)
	}
	// Operations before Connect must fail cleanly, not panic.
	if err := s.Send(t.Context(), "x"); err == nil {
		t.Error("Send before Connect should fail")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close on unconnected socket: %v", err)
	}
}
