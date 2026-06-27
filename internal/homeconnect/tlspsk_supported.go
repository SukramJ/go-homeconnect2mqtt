// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

//go:build tlspsk

package homeconnect

// TLSPSKSupported reports whether this build can talk to TLS-PSK appliances.
// True here: the cgo `tlspsk` build links the OpenSSL transport (socket_tls_psk.go).
const TLSPSKSupported = true
