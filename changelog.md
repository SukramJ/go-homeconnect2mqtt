# Changelog

All notable changes to this project are documented here. The format loosely
follows Keep a Changelog; versions track `internal/version/version.go`.

## [Unreleased]

## [0.1.1] - 2026-06-27

### Added
- Home Assistant add-on manifest (`addon/config.yaml`) so the Supervisor can
  discover, install and configure the add-on (options/schema, per-arch GHCR
  image, `map: share:rw`, `services: mqtt:want`, Ingress web UI).
- `/share/homeconnect/` drop folder, created on add-on start, as the place to
  copy the profile ZIP or pre-parsed `<haId>.json` files.

### Fixed
- `.gitignore` no longer swallows `addon/config.yaml`: the `config.yaml` /
  `devices.yaml` rules are anchored to the repo root.
- Dockerfile `InvalidDefaultArgInFrom` warning — `BUILD_FROM` now has a default
  (overridden by the Supervisor / CI per arch).

## [0.1.0] - 2026-06-27

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
