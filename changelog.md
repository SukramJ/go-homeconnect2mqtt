# Changelog

All notable changes to this project are documented here. The format loosely
follows Keep a Changelog; versions track `internal/version/version.go`.

## [Unreleased]

## [0.2.1] - 2026-06-27

### Fixed
- Add-on MQTT auto-discovery used the wrong scheme: `run.sh` tested the exit
  code of `bashio::services 'mqtt' 'ssl'` (always 0) instead of its value, so it
  always built `ssl://` and failed against the plaintext Home Assistant Mosquitto
  broker. Test the value now.
- MQTT TLS dial failed with "either ServerName or InsecureSkipVerify must be
  specified": default the TLS `ServerName` to the broker host for `ssl://` URLs.

## [0.2.0] - 2026-06-27

### Added
- TLS-PSK transport for older appliances (`connectionType: TLS`,
  `wss://host:443`): OpenSSL-backed via cgo behind `-tags tlspsk`, TLS 1.2
  ECDHE-PSK, driven through memory BIOs with the WebSocket layer over the tunnel
  (docs/01-protocol.md §4). Verified end-to-end against a real Neff appliance.
- `hc-util connection-test` now dispatches on the connection type, so it
  connects both AES and TLS devices (was AES-only).
- Add-on auto-config: drop the profile ZIP(s) into `/share/homeconnect` and the
  entrypoint parses **all** of them, writing a keys inventory (`/data/profiles/
  inventory.json`, 0600). A `devices` entry then only needs `name` + `host` +
  `haid` — `connection_type`/`psk64`/`iv64`/`description` are auto-filled from the
  matching ZIP. `hc-util parse` accepts a directory and a `--inventory` flag, and
  no longer prints secrets when writing an inventory (fixes a key leak into the
  add-on log). Flags are now honoured after the path argument too.

### Changed
- The Home Assistant add-on image is now **amd64-only** and built with cgo +
  OpenSSL (`-tags tlspsk`), so it supports both AES and TLS-PSK appliances out of
  the box. The CGo-free default `go build` still cross-compiles for AES-only
  standalone use; TLS devices there report `ErrTLSPSKUnsupported`.

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
