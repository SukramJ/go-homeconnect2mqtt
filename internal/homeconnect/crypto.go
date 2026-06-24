// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"sync"
)

// Direction bytes mix into the HMAC so the two chains cannot be confused.
// They are taken from the sender's point of view: the client always signs
// what it sends with 'E' and verifies what it receives with 'C'.
// per docs/01-protocol.md §3.1.
const (
	encryptDirection = 0x45 // 'E'
	decryptDirection = 0x43 // 'C'
	minMessageLength = 32
	hmacTrunc        = 16
)

// DecodeKey decodes a url-safe base64 value without padding
// (docs/01-protocol.md §2: psk64/iv64 are URL-safe base64, padding optional).
func DecodeKey(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// deriveKeys runs the KDF: a single HMAC-SHA256 per label, no HKDF, no
// salt, no expand round (docs/01-protocol.md §2).
func deriveKeys(psk []byte) (encKey, macKey []byte) {
	encKey = hmacSHA256(psk, []byte("ENC"))
	macKey = hmacSHA256(psk, []byte("MAC"))
	return encKey, macKey
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// AESCrypto holds the per-appliance keys plus the per-connection rolling
// crypto state. The TX and RX chains are independent and each guarded by
// its own mutex; two concurrent sends would corrupt last_tx_hmac and the
// CBC chain (docs/01-protocol.md §3.4 obligation 3).
type AESCrypto struct {
	encKey []byte
	macKey []byte
	iv     []byte
	block  cipher.Block
	rand   io.Reader // injectable for deterministic tests

	txMu       sync.Mutex
	txCBC      cipher.BlockMode
	lastTXHMAC []byte

	rxMu       sync.Mutex
	rxCBC      cipher.BlockMode
	lastRXHMAC []byte
}

// NewAESCrypto derives the encryption/MAC keys from psk and prepares the
// cipher. iv must be the 16-byte AES IV from the profile. Call Reset
// before the first message and on every reconnect.
func NewAESCrypto(psk, iv []byte) (*AESCrypto, error) {
	if len(iv) != aes.BlockSize {
		return nil, cryptoErrf("iv must be %d bytes, got %d", aes.BlockSize, len(iv))
	}
	encKey, macKey := deriveKeys(psk)
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, &CryptoError{Reason: "new cipher", Err: err}
	}
	c := &AESCrypto{
		encKey: encKey,
		macKey: macKey,
		iv:     append([]byte(nil), iv...),
		block:  block,
		rand:   rand.Reader,
	}
	c.Reset()
	return c, nil
}

// Reset re-initialises the rolling state for a fresh connection: both
// HMAC chains start at 16 zero bytes and the CBC chains restart from the
// static iv (docs/01-protocol.md §10). Must be called whenever a new
// socket is established (docs/01-protocol.md §10).
func (c *AESCrypto) Reset() {
	c.txMu.Lock()
	c.txCBC = cipher.NewCBCEncrypter(c.block, c.iv)
	c.lastTXHMAC = make([]byte, hmacTrunc)
	c.txMu.Unlock()

	c.rxMu.Lock()
	c.rxCBC = cipher.NewCBCDecrypter(c.block, c.iv)
	c.lastRXHMAC = make([]byte, hmacTrunc)
	c.rxMu.Unlock()
}

// Encrypt pads + CBC-encrypts clear (continuing the TX stream), appends
// the rolling truncated HMAC and returns the wire frame (ct‖mac).
// Per docs/01-protocol.md §3.2.
func (c *AESCrypto) Encrypt(clearMsg []byte) ([]byte, error) {
	return c.seal(encryptDirection, clearMsg)
}

// Decrypt verifies the rolling HMAC (constant time), advances the chain
// only on success, CBC-decrypts and unpads. Any failure returns a
// *CryptoError that obliges the caller to reconnect
// (docs/01-protocol.md §3.3).
func (c *AESCrypto) Decrypt(buf []byte) ([]byte, error) {
	return c.open(decryptDirection, buf)
}

// seal is the direction-parameterised encrypt path. Production code uses
// encryptDirection; tests reuse it with decryptDirection to play the
// server side of the conversation.
func (c *AESCrypto) seal(dir byte, clearMsg []byte) ([]byte, error) {
	padded, err := c.pad(clearMsg)
	if err != nil {
		return nil, err
	}
	c.txMu.Lock()
	defer c.txMu.Unlock()

	ct := make([]byte, len(padded))
	c.txCBC.CryptBlocks(ct, padded)

	mac := c.chainHMAC(dir, c.lastTXHMAC, ct)
	c.lastTXHMAC = mac

	frame := make([]byte, 0, len(ct)+len(mac))
	frame = append(frame, ct...)
	frame = append(frame, mac...)
	return frame, nil
}

// open is the direction-parameterised decrypt path.
func (c *AESCrypto) open(dir byte, buf []byte) ([]byte, error) {
	if len(buf) < minMessageLength {
		return nil, cryptoErrf("frame too short: %d < %d", len(buf), minMessageLength)
	}
	if len(buf)%aes.BlockSize != 0 {
		return nil, cryptoErrf("frame not block-aligned: %d", len(buf))
	}
	ct := buf[:len(buf)-hmacTrunc]
	recvHMAC := buf[len(buf)-hmacTrunc:]

	c.rxMu.Lock()
	defer c.rxMu.Unlock()

	calc := c.chainHMAC(dir, c.lastRXHMAC, ct)
	if !hmac.Equal(recvHMAC, calc) {
		return nil, cryptoErrf("HMAC mismatch")
	}
	// Advance the chain only after a successful verification.
	c.lastRXHMAC = append([]byte(nil), recvHMAC...)

	plain := make([]byte, len(ct))
	c.rxCBC.CryptBlocks(plain, ct)
	return unpad(plain)
}

// chainHMAC computes HMAC-SHA256(macKey, iv‖dir‖last‖ct)[:16].
func (c *AESCrypto) chainHMAC(dir byte, last, ct []byte) []byte {
	h := hmac.New(sha256.New, c.macKey)
	h.Write(c.iv)
	h.Write([]byte{dir})
	h.Write(last)
	h.Write(ct)
	return h.Sum(nil)[:hmacTrunc]
}

// pad applies the custom (non-PKCS#7) scheme: first pad byte is 0x00,
// last byte is the pad length, the middle is random. A pad length of 1 is
// bumped by 16 so there are always at least two pad bytes.
// Per docs/01-protocol.md §3.2 (custom padding).
func (c *AESCrypto) pad(clearMsg []byte) ([]byte, error) {
	padLen := aes.BlockSize - (len(clearMsg) % aes.BlockSize)
	if padLen == 1 {
		padLen += aes.BlockSize
	}
	out := make([]byte, 0, len(clearMsg)+padLen)
	out = append(out, clearMsg...)
	out = append(out, 0x00)
	mid := make([]byte, padLen-2)
	if _, err := io.ReadFull(c.rand, mid); err != nil {
		return nil, &CryptoError{Reason: "read random padding", Err: err}
	}
	out = append(out, mid...)
	out = append(out, byte(padLen))
	return out, nil
}

// unpad strips the custom padding using the trailing length byte.
func unpad(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, cryptoErrf("empty plaintext")
	}
	padLen := int(plain[len(plain)-1])
	if padLen < 1 || padLen > len(plain) {
		return nil, cryptoErrf("invalid padding length %d for %d bytes", padLen, len(plain))
	}
	return plain[:len(plain)-padLen], nil
}
