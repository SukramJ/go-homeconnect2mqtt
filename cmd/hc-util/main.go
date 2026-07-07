// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command hc-util is the operator CLI: parse a Home Connect profile
// archive, dump a device's feature list and run a connection test. The
// subcommands are implemented across later phases; this file owns the
// top-level dispatch.
package main

import (
	"io"
	"log/slog"
	"os"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// newLogger builds the CLI's text logger with the mandatory secret
// redaction guard installed: string attrs keyed like secrets
// (psk/iv/serialNumber/mac/shipSki/deviceID, docs/03-profile-format.md
// §6) are masked before they reach the output.
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{ReplaceAttr: profile.RedactAttr}))
}

func run(args []string, stdout, stderr io.Writer) int {
	// Route any default-logger fallback through the redacting handler.
	slog.SetDefault(newLogger(stderr))
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fprintln(stdout, version.String())
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	case "parse":
		return cmdExit(parseCmd(args[1:], stdout, stderr), stderr)
	case "dump":
		return cmdExit(dumpCmd(args[1:], stdout, stderr), stderr)
	case "connection-test":
		return cmdExit(connTestCmd(args[1:], stdout, stderr), stderr)
	default:
		fprintf(stderr, "hc-util: unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
}

// cmdExit maps a subcommand error to an exit code.
func cmdExit(err error, stderr io.Writer) int {
	if err != nil {
		fprintln(stderr, "hc-util:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fprintln(w, `hc-util — go-homeconnect2mqtt operator CLI

usage:
  hc-util parse <profile.zip> [--out <dir>]   parse a profile archive into device descriptions
  hc-util dump <device.json>                  list all features of a parsed device
  hc-util connection-test <device.json>       connect + handshake against a device
  hc-util version                             print version`)
}
