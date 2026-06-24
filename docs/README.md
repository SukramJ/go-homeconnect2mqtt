# go-homeconnect2mqtt — Knowledge Base & Concept

These `docs/` are the **self-contained** foundation for `go-homeconnect2mqtt`
(Home Connect ⇒ MQTT, local, no cloud). All required information was extracted
from the reference projects, so those repos are no longer needed.

The project is designed as a **sister project of `go-mtec2mqtt`**: same
structure, conventions and resilience philosophy. The reusable infrastructure
(MQTT client, config loader, Makefile, golangci, Dockerfile, version, git hook)
now lives directly in this repository.

## Documents

| File | Contents |
|---|---|
| [`00-research.md`](00-research.md) | Background research: overview, reliability, implications |
| [`01-protocol.md`](01-protocol.md) | **Wire protocol**: transport (AES/TLS-PSK), KDF, AES-CBC+HMAC chain, handshake, services, error codes, reconnect state machine |
| [`02-data-model.md`](02-data-model.md) | **Data model**: DeviceDescription/FeatureMapping XML, type tables (refCID→type), enum logic, entity/value semantics |
| [`03-profile-format.md`](03-profile-format.md) | **Onboarding**: profile archive (`.json` + 2 XML), exact JSON fields, discovery, error classes, secrets |
| [`04-device-mapping.md`](04-device-mapping.md) | **Feature catalogues** (BSH.Common + dishwasher/hob/washer) + MQTT/HA discovery mapping |
| [`05-resilience.md`](05-resilience.md) | **Resilience**: 8 error classes from the GitHub issues + concrete Go countermeasures, device-specific |
| [`06-architecture.md`](06-architecture.md) | **Concept**: Go package layout, lifecycle, config, bridge, build/quality, test strategy, roadmap, optional web UI (§10) |
| [`07-reference-sources.md`](07-reference-sources.md) | **Verbatim artefacts**: crypto code reference + test fixtures (XML) |
| [`08-web-api.md`](08-web-api.md) | **HTTP API & SSE contract** of the optional web UI: endpoint schemas, JSON payloads, event format, error taxonomy |
| [`09-implementation-plan.md`](09-implementation-plan.md) | **Trackable implementation plan**: 13 phases (P0–P12) with checklists, file mapping, test gates, dependencies, master tracker |

## Reading order

- **Start implementing:** 06 (concept) → 01 (protocol) → 02 (data model) → 07 (fixtures/tests).
- **Onboarding/devices:** 03 (profile) → 04 (mapping).
- **Cross-cutting resilience:** 05 — woven into every building block (references FK-1…FK-8).

## Core facts in one paragraph

Every Home Connect appliance runs a local WebSocket server on the LAN at
`/homeconnect`. Newer appliances: **AES** on `ws://host:80` with app-layer
crypto (AES-256-CBC as a continuous stream + a rolling HMAC-SHA256 chain per
direction; keys from `HMAC(psk,"ENC"/"MAC")`). Older appliances: **TLS-PSK** on
`wss://host:443`. Keys/description come once from the profile downloader
(`.json` with `key`/`iv`/`connectionType` + two XML files). After the handshake
(`/ei/initialValues` → `/ci/services` → … → `/ro/allMandatoryValues`) values
arrive as `NOTIFY /ro/values`; writing via `POST /ro/values`. **Resilience is
the core requirement** (undocumented API): full reconnect on HMAC desync,
backoff+jitter, offline≠error, per-entity/per-device isolation, device-specific
program-start paths.
