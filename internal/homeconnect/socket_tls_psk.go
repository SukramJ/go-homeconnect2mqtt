// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

//go:build tlspsk

// OpenSSL-backed TLS-PSK transport for older appliances (connectionType
// "TLS", wss://host:443). Go's crypto/tls has no external-PSK cipher suites,
// so this lives behind the cgo `tlspsk` build tag and overrides
// tlsPSKConnect at init (see socket_tls.go and docs/01-protocol.md §4).
//
// Per spec §4: TLS 1.2 only, PSK cipher suites, no server certificate, empty
// PSK identity, the psk returned verbatim. The appliance negotiates an
// ECDHE-PSK suite (verified: ECDHE-PSK-CHACHA20-POLY1305). OpenSSL 3 hides
// these behind security level 0.
//
// OpenSSL is driven through memory BIOs so the surrounding net.Conn (the TCP
// socket) keeps full deadline/context support; the SSL object itself is
// serialized by a single mutex (the record stream must stay ordered), while
// the one blocking network read happens outside that mutex.

package homeconnect

/*
#cgo darwin CFLAGS:  -I/opt/homebrew/opt/openssl@3/include
#cgo darwin LDFLAGS: -L/opt/homebrew/opt/openssl@3/lib -lssl -lcrypto
#cgo linux  pkg-config: openssl
#include <openssl/ssl.h>
#include <openssl/err.h>
#include <stdlib.h>
#include <string.h>

typedef struct { unsigned char* psk; int len; } psk_ctx;

static int g_psk_idx = -1;

// Per spec §4: empty identity, psk returned verbatim. The psk is read from
// per-SSL ex_data (C-owned memory; never a Go pointer held by C).
static unsigned int hc_psk_client_cb(SSL* ssl, const char* hint,
        char* identity, unsigned int max_id,
        unsigned char* out, unsigned int max_psk) {
    (void)hint;
    if (max_id > 0) identity[0] = '\0';
    psk_ctx* pc = (psk_ctx*) SSL_get_ex_data(ssl, g_psk_idx);
    if (!pc || pc->len <= 0 || (unsigned int)pc->len > max_psk) return 0;
    memcpy(out, pc->psk, pc->len);
    return (unsigned int)pc->len;
}

// Build a connect-state SSL with memory BIOs and the psk attached. Returns 0
// on success. The caller drives the handshake/IO via hc_* below and frees with
// hc_free.
static int hc_new(const unsigned char* psk, int psklen,
        SSL_CTX** out_ctx, SSL** out_ssl, BIO** out_rbio, BIO** out_wbio) {
    if (g_psk_idx < 0) {
        g_psk_idx = SSL_get_ex_new_index(0, NULL, NULL, NULL, NULL);
        if (g_psk_idx < 0) return -1;
    }
    SSL_CTX* ctx = SSL_CTX_new(TLS_client_method());
    if (!ctx) return -2;
    SSL_CTX_set_min_proto_version(ctx, TLS1_2_VERSION);
    SSL_CTX_set_max_proto_version(ctx, TLS1_2_VERSION);
    SSL_CTX_set_security_level(ctx, 0);
    if (SSL_CTX_set_cipher_list(ctx, "PSK") != 1) { SSL_CTX_free(ctx); return -3; }
    SSL_CTX_set_psk_client_callback(ctx, hc_psk_client_cb);
    SSL_CTX_set_verify(ctx, SSL_VERIFY_NONE, NULL);

    SSL* ssl = SSL_new(ctx);
    if (!ssl) { SSL_CTX_free(ctx); return -4; }

    psk_ctx* pc = (psk_ctx*) malloc(sizeof(psk_ctx));
    if (!pc) { SSL_free(ssl); SSL_CTX_free(ctx); return -5; }
    pc->psk = (unsigned char*) malloc(psklen);
    if (!pc->psk) { free(pc); SSL_free(ssl); SSL_CTX_free(ctx); return -6; }
    memcpy(pc->psk, psk, psklen);
    pc->len = psklen;
    SSL_set_ex_data(ssl, g_psk_idx, pc);

    BIO* rb = BIO_new(BIO_s_mem());
    BIO* wb = BIO_new(BIO_s_mem());
    if (!rb || !wb) { free(pc->psk); free(pc); SSL_free(ssl); SSL_CTX_free(ctx); return -7; }
    SSL_set_bio(ssl, rb, wb); // SSL owns the BIOs from here
    SSL_set_connect_state(ssl);

    *out_ctx = ctx; *out_ssl = ssl; *out_rbio = rb; *out_wbio = wb;
    return 0;
}

static void hc_free(SSL_CTX* ctx, SSL* ssl) {
    if (ssl) {
        psk_ctx* pc = (psk_ctx*) SSL_get_ex_data(ssl, g_psk_idx);
        if (pc) { if (pc->psk) free(pc->psk); free(pc); }
        SSL_free(ssl); // also frees the BIOs
    }
    if (ctx) SSL_CTX_free(ctx);
}

static int  hc_do_handshake(SSL* ssl)               { return SSL_do_handshake(ssl); }
static int  hc_read(SSL* ssl, void* b, int n)       { return SSL_read(ssl, b, n); }
static int  hc_write(SSL* ssl, const void* b, int n){ return SSL_write(ssl, b, n); }
static int  hc_err(SSL* ssl, int ret)               { return SSL_get_error(ssl, ret); }
// Drain pending outbound TLS bytes from the write-BIO. >0 = bytes, 0 = empty.
static int  hc_bio_read(BIO* wb, void* b, int n)    { return BIO_read(wb, b, n); }
// Feed received TLS bytes into the read-BIO.
static int  hc_bio_write(BIO* rb, const void* b, int n) { return BIO_write(rb, b, n); }
static const char* hc_version(SSL* ssl)             { return SSL_get_version(ssl); }
static const char* hc_cipher(SSL* ssl)              { return SSL_get_cipher(ssl); }
static const char* hc_last_err(void) {
    static char buf[256];
    ERR_error_string_n(ERR_get_error(), buf, sizeof(buf));
    return buf;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/coder/websocket"
)

const (
	cSSLErrorWantRead   = C.SSL_ERROR_WANT_READ
	cSSLErrorWantWrite  = C.SSL_ERROR_WANT_WRITE
	cSSLErrorZeroReturn = C.SSL_ERROR_ZERO_RETURN
)

// Wire the OpenSSL implementation into the transport-agnostic TLSSocket.
func init() { tlsPSKConnect = openSSLPSKConnect }

// sslConn is a net.Conn that runs OpenSSL over memory BIOs on top of raw.
// All SSL_* calls plus draining the write-BIO and the matching raw write are
// serialized by mu; the single blocking raw.Read happens outside mu.
type sslConn struct {
	raw net.Conn
	// inMu serializes feedIn's raw.Read + read-BIO append: the receive
	// loop and a concurrent Ping/Write can both need inbound TLS bytes
	// (SSL_ERROR_WANT_READ), and interleaved reads into the BIO would
	// corrupt the record stream order.
	inMu   sync.Mutex
	mu     sync.Mutex
	ctx    *C.SSL_CTX
	ssl    *C.SSL
	rbio   *C.BIO
	wbio   *C.BIO
	in     []byte // scratch for raw.Read, guarded by inMu
	closed bool
}

func sslErr(stage string) error {
	return fmt.Errorf("homeconnect: tls-psk %s: %s", stage, C.GoString(C.hc_last_err()))
}

// flushOutLocked drains queued outbound TLS bytes and writes them to raw.
// Must be called with mu held; raw.Write is ordered by mu so records stay
// in sequence.
func (c *sslConn) flushOutLocked() error {
	var buf [4096]byte
	for {
		n := C.hc_bio_read(c.wbio, unsafe.Pointer(&buf[0]), C.int(len(buf)))
		if n <= 0 {
			return nil
		}
		if _, err := c.raw.Write(buf[:int(n)]); err != nil {
			return err
		}
	}
}

// feedIn reads one chunk from raw (no mu held) and pushes it into the
// read-BIO under mu. inMu keeps read + BIO append atomic across the two
// goroutines that may need inbound bytes concurrently.
func (c *sslConn) feedIn() error {
	c.inMu.Lock()
	defer c.inMu.Unlock()
	n, err := c.raw.Read(c.in)
	if n > 0 {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return net.ErrClosed
		}
		C.hc_bio_write(c.rbio, unsafe.Pointer(&c.in[0]), C.int(n))
		c.mu.Unlock()
	}
	if err != nil {
		return err
	}
	return nil
}

// pump runs op (an SSL_* call) to completion, shuttling TLS bytes between the
// BIOs and raw until op makes progress or fails. A concurrent Close (the
// standard way an in-flight blocking Read is unblocked) frees the SSL
// objects under mu, so every iteration re-checks closed before touching them.
func (c *sslConn) pump(op func() C.int) (C.int, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return 0, net.ErrClosed
		}
		ret := op()
		var sslErrCode C.int
		if ret <= 0 {
			sslErrCode = C.hc_err(c.ssl, ret)
		}
		flushErr := c.flushOutLocked()
		c.mu.Unlock()
		if flushErr != nil {
			return ret, flushErr
		}
		if ret > 0 {
			return ret, nil
		}
		switch sslErrCode {
		case cSSLErrorWantRead:
			if err := c.feedIn(); err != nil {
				return ret, err
			}
		case cSSLErrorWantWrite:
			// outbound already flushed; retry.
		case cSSLErrorZeroReturn:
			return ret, io.EOF
		default:
			return ret, sslErr("io")
		}
	}
}

func (c *sslConn) handshake() error {
	_, err := c.pump(func() C.int { return C.hc_do_handshake(c.ssl) })
	return err
}

func (c *sslConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ret, err := c.pump(func() C.int { return C.hc_read(c.ssl, unsafe.Pointer(&p[0]), C.int(len(p))) })
	if err != nil {
		return 0, err
	}
	return int(ret), nil
}

func (c *sslConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ret, err := c.pump(func() C.int { return C.hc_write(c.ssl, unsafe.Pointer(&p[0]), C.int(len(p))) })
	if err != nil {
		return 0, err
	}
	return int(ret), nil
}

func (c *sslConn) Close() error {
	// Close raw first, without waiting on mu: a pump may hold mu across a
	// raw.Write blocked on a dead peer (no write deadline in steady state),
	// and closing the TCP conn is what unblocks it. Waiting for mu here
	// would wedge session teardown for the kernel retransmit timeout.
	err := c.raw.Close()
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		C.hc_free(c.ctx, c.ssl)
		c.ssl, c.ctx, c.rbio, c.wbio = nil, nil, nil, nil
	}
	c.mu.Unlock()
	return err
}

func (c *sslConn) LocalAddr() net.Addr                { return c.raw.LocalAddr() }
func (c *sslConn) RemoteAddr() net.Addr               { return c.raw.RemoteAddr() }
func (c *sslConn) SetDeadline(t time.Time) error      { return c.raw.SetDeadline(t) }
func (c *sslConn) SetReadDeadline(t time.Time) error  { return c.raw.SetReadDeadline(t) }
func (c *sslConn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }

// dialSSLPSK opens the TCP connection and completes the TLS-PSK handshake.
func dialSSLPSK(ctx context.Context, hostPort string, psk []byte) (*sslConn, error) {
	if len(psk) == 0 {
		return nil, cryptoErrf("empty psk")
	}
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("homeconnect: tls-psk dial %s: %w", hostPort, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}

	c := &sslConn{raw: raw, in: make([]byte, 16<<10)}
	rc := C.hc_new(
		(*C.uchar)(unsafe.Pointer(&psk[0])), C.int(len(psk)),
		&c.ctx, &c.ssl, &c.rbio, &c.wbio,
	)
	if rc != 0 {
		_ = raw.Close()
		return nil, fmt.Errorf("homeconnect: tls-psk init failed (stage %d)", int(rc))
	}
	if err := c.handshake(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("homeconnect: tls-psk handshake: %w", err)
	}
	// Clear the dial deadline; the session layer manages liveness via ping.
	_ = raw.SetDeadline(time.Time{})
	return c, nil
}

// wsTLSConn carries the WebSocket over the TLS-PSK tunnel. Messages are plain
// UTF-8 text (TLS protects everything; docs/01-protocol.md §4).
type wsTLSConn struct {
	conn *websocket.Conn
}

func (w *wsTLSConn) send(ctx context.Context, message string) error {
	return w.conn.Write(ctx, websocket.MessageText, []byte(message))
}

func (w *wsTLSConn) receive(ctx context.Context) (string, error) {
	typ, data, err := w.conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("homeconnect: read: %w", err)
	}
	if typ != websocket.MessageText {
		return "", cryptoErrf("non-text frame (%v)", typ)
	}
	return string(data), nil
}

func (w *wsTLSConn) ping(ctx context.Context) error { return w.conn.Ping(ctx) }

func (w *wsTLSConn) close() error {
	return w.conn.Close(websocket.StatusNormalClosure, "")
}

// openSSLPSKConnect establishes the TLS-PSK tunnel and the WebSocket upgrade.
// The TLS handshake is already done by dialSSLPSK, so coder/websocket dials a
// plain ws:// over the established (encrypted) conn via a one-shot transport.
func openSSLPSKConnect(ctx context.Context, host string, psk []byte) (tlsConn, error) {
	wssURL, err := applianceURL(host, true) // wss://[host]:443/homeconnect (IPv6-safe)
	if err != nil {
		return nil, err
	}
	hostPort := strings.TrimSuffix(strings.TrimPrefix(wssURL, "wss://"), wsPath)
	wsURL := "ws://" + hostPort + wsPath // already-TLS conn → plain ws upgrade

	sc, err := dialSSLPSK(ctx, hostPort, psk)
	if err != nil {
		return nil, err
	}

	used := false
	transport := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used {
				return nil, errors.New("homeconnect: tls-psk conn already used")
			}
			used = true
			return sc, nil
		},
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		_ = sc.Close()
		return nil, fmt.Errorf("homeconnect: tls-psk ws upgrade: %w", err)
	}
	wsConn.SetReadLimit(readLimit)
	return &wsTLSConn{conn: wsConn}, nil
}
