# Changelog

All notable changes to this project are documented here. The format loosely
follows Keep a Changelog; versions track `internal/version/version.go`.

## [Unreleased]

### Added
- Initial implementation of the local Home Connect to MQTT bridge:
  AES app-layer crypto transport, WebSocket protocol session/handshake,
  tolerant profile (DeviceDescription/FeatureMapping) parser, reconnect
  state machine, entity model, MQTT publish/command bridge, Home Assistant
  discovery, optional TLS-PSK transport (cgo `tlspsk` build), `hc-util` CLI
  and an optional status/health web UI.

### Quality
- All packages tested under `go test -race` (≈78% total statement coverage);
  `go vet`, `gofumpt` and the strict `golangci-lint` config pass with zero
  findings. Cross-compiles for linux/amd64+arm64 and darwin/arm64.
