// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command hc-util is the operator CLI: parse a Home Connect profile
// archive, dump a device's feature list and run a connection test. The
// subcommands are implemented across later phases; this file owns the
// top-level dispatch.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.String())
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
		fmt.Fprintf(stderr, "hc-util: unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
}

// cmdExit maps a subcommand error to an exit code.
func cmdExit(err error, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintln(stderr, "hc-util:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `hc-util — go-homeconnect2mqtt operator CLI

usage:
  hc-util parse <profile.zip> [--out <dir>]   parse a profile archive into device descriptions
  hc-util dump <device.json>                  list all features of a parsed device
  hc-util connection-test <device.json>       connect + handshake against a device
  hc-util version                             print version`)
}
