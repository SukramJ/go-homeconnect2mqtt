// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

//go:build !tlspsk

package homeconnect

// TLSPSKSupported reports whether this build can talk to TLS-PSK appliances.
// False here: the default CGo-free build only has the stub, so a TLS device
// fails with ErrTLSPSKUnsupported and reports offline.
const TLSPSKSupported = false
