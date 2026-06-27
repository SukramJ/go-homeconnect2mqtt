// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// connectTimeout bounds a connection test (docs/03 §5).
const connectTimeout = 20 * time.Second

// fprintf / fprintln centralise the ignored-error CLI writes.
func fprintf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func fprintln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }

// parseCmd parses a profile ZIP (or a directory of ZIPs) into cached
// description JSON files. By default it prints a devices.yaml snippet for the
// operator to complete; with --inventory it instead writes the keys to a
// machine-readable inventory file (0600) and prints only a non-secret summary
// — that mode is what the add-on entrypoint uses.
func parseCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "./profiles", "output directory for parsed descriptions")
	inventory := fs.String("inventory", "", "write an inventory JSON (incl. keys, 0600) here for the add-on; suppresses the secret stdout snippet")
	// Go's flag package stops at the first positional, so parse in a loop to
	// also honour flags that follow the path (e.g. `parse foo.zip --out x`).
	var positionals []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positionals) == 0 {
		return errors.New("parse: missing <profile.zip|dir>")
	}
	src := positionals[0]
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{}))

	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	var profiles []*profile.DeviceProfile
	if info.IsDir() {
		profiles, err = profile.ParseArchiveDir(src, logger)
	} else {
		profiles, err = profile.ParseArchiveFile(src, logger)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil { //nolint:gosec // operator dir
		return fmt.Errorf("parse: mkdir %s: %w", *out, err)
	}
	for _, p := range profiles {
		descPath := filepath.Join(*out, p.HaID+".json")
		if err := profile.SaveDescriptionJSON(descPath, p.Description); err != nil {
			return err
		}
	}

	if *inventory != "" {
		if err := profile.WriteInventory(*inventory, profiles); err != nil {
			return err
		}
		// Non-secret summary only — keys must not reach the add-on log.
		fprintf(stdout, "# parsed %d appliance(s); keys written to %s (not shown):\n", len(profiles), *inventory)
		for _, p := range profiles {
			fprintf(stdout, "#   %s  %s %s %s  %s\n", p.HaID, p.Brand, p.Type, p.Vib, p.ConnectionType)
		}
	} else {
		fprintln(stdout, "# Add these entries to devices.yaml (secrets included — keep local):")
		fprintln(stdout, "devices:")
		for _, p := range profiles {
			fprintf(stdout, "  - name: %s\n", p.HaID)
			fprintf(stdout, "    host: \"\"            # %s (mDNS) or set a manual IP\n", p.DefaultHost())
			fprintf(stdout, "    connection_type: %s\n", p.ConnectionType)
			fprintf(stdout, "    psk64: %q\n", p.PSK64)
			if p.ConnectionType == profile.ConnectionAES {
				fprintf(stdout, "    iv64: %q\n", p.IV64)
			}
			fprintf(stdout, "    description: %s\n", filepath.Join(*out, p.HaID+".json"))
		}
	}
	fprintf(stderr, "parsed %d device(s) into %s\n", len(profiles), *out)
	return nil
}

// dumpCmd lists every feature of a parsed description.
func dumpCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("dump: missing <device.json>")
	}
	desc, err := profile.LoadDescriptionJSON(args[0], slog.New(slog.NewTextHandler(stderr, nil)))
	if err != nil {
		return err
	}
	fprintf(stdout, "# %s %s %s (version %d)\n", desc.Info.Brand, desc.Info.Type, desc.Info.Model, desc.Info.Version)
	fprintf(stdout, "%-6s  %-10s  %-10s  %-9s  %s\n", "UID", "KIND", "PROTOCOL", "ACCESS", "NAME")
	entries := append([]*profile.Entry(nil), desc.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, e := range entries {
		fprintf(stdout, "0x%04X  %-10s  %-10s  %-9s  %s\n", e.UID, e.Kind, e.ProtocolType, e.Access, e.Name)
	}
	fprintf(stderr, "%d feature(s)\n", len(entries))
	return nil
}

// connTestCmd connects to each device in a devices file and reports the
// outcome with a categorized error and a manual-IP hint on failure (FK-7).
func connTestCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("connection-test: missing <devices.yaml>")
	}
	devices, err := profile.LoadDevices(args[0])
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{}))
	var failures int
	for _, dc := range devices {
		if err := testOne(dc, logger, stdout); err != nil {
			failures++
			fprintf(stdout, "✗ %s: %v\n", dc.Name, err)
			fprintf(stdout, "    hint: verify the device is on and reachable; set a manual IP in host:\n")
		} else {
			fprintf(stdout, "✓ %s: connected\n", dc.Name)
		}
	}
	if failures > 0 {
		return fmt.Errorf("connection-test: %d/%d device(s) failed", failures, len(devices))
	}
	return nil
}

func testOne(dc profile.DeviceConfig, logger *slog.Logger, _ io.Writer) error {
	if dc.Host == "" {
		return errors.New("no host configured")
	}
	psk, err := homeconnect.DecodeKey(dc.PSK64)
	if err != nil {
		return fmt.Errorf("bad psk64: %w", err)
	}
	var iv []byte
	if dc.IV64 != "" { // AES only; TLS-PSK has no IV (docs/01-protocol.md §4)
		if iv, err = homeconnect.DecodeKey(dc.IV64); err != nil {
			return fmt.Errorf("bad iv64: %w", err)
		}
	}
	// NewSocket dispatches on the connection type; the TLS-PSK transport fails
	// with ErrTLSPSKUnsupported unless this is the cgo `tlspsk` build.
	sock, err := homeconnect.NewSocket(homeconnect.ConnectionType(dc.ConnectionType), dc.Host, psk, iv)
	if err != nil {
		return err
	}
	sess := homeconnect.NewSession(sock, homeconnect.SessionConfig{
		HandshakeTimeout: connectTimeout, SendTimeout: connectTimeout, Logger: logger,
	})
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := sess.Connect(ctx); err != nil {
		_ = sess.Close()
		return err
	}
	_ = sess.Close()
	return nil
}
