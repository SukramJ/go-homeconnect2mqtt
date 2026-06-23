# Changelog

All notable changes to this project are documented here. The format loosely
follows Keep a Changelog; versions track `internal/version/version.go`.

## [Unreleased]

### Added
- Initial implementation of the local Home Connect to MQTT bridge:
  AES app-layer crypto transport, WebSocket protocol session/handshake,
  tolerant profile (DeviceDescription/FeatureMapping) parser, reconnect
  state machine, entity model, MQTT publish/command bridge, Home Assistant
  discovery, optional TLS-PSK transport, `hc-util` CLI and an optional
  status/health web UI.
