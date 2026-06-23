// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command homeconnect2mqtt is the daemon entry point: it bridges local
// Home Connect appliances to MQTT. The full wiring (config -> profile ->
// MQTT -> bridge) is assembled in run; this file owns flag parsing,
// logging setup and process lifecycle only.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("homeconnect2mqtt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath  = fs.String("config", "", "path to config.yaml (auto-located when empty)")
		devicesPath = fs.String("devices", "devices.yaml", "path to the device inventory file")
		mappingPath = fs.String("mapping", "mapping.yaml", "path to the optional enrichment catalogue")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stderr, version.String())
		return 0
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{}))
	logger.Info("starting", slog.String("version", version.Version),
		slog.String("config", *configPath), slog.String("devices", *devicesPath),
		slog.String("mapping", *mappingPath))

	// The bridge is wired in during phase P6; until then the daemon only
	// validates that it can start up. See docs/10-implementation-plan.md.
	logger.Warn("bridge not yet wired; exiting")
	return 0
}
