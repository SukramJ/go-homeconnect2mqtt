# NOTICE — Attribution & Third-Party Licenses

`go-homeconnect2mqtt` is licensed under the MIT License (see [LICENSE](LICENSE)).
This file documents its relationship to other projects and the third-party
material it includes or builds upon.

## Project relationship

This is a new, standalone Go application of mixed lineage:

- The **local Home Connect protocol** (AES app-layer crypto, session/handshake,
  XML profile parsing, entity model) is a **clean-room reimplementation** derived
  from our own protocol specification in [`docs/`](docs/) — see the clean-room
  note below.
- The **integration concepts** (device→platform mapping, Home Assistant
  discovery heuristics, onboarding, and the 8 resilience error classes FK-1…FK-8)
  were **reimplemented in Go**, inspired by the Home Assistant integration listed
  below (concepts/requirements, not code).
- The **reusable infrastructure** (the pure-Go MQTT client, config loader,
  Makefile/Dockerfile/CI, package layout and conventions) is **adapted from the
  sister project `go-mtec2mqtt`**.

## Acknowledgements

With thanks to the projects this work learned from:

| Project | Role | License |
|---|---|---|
| [`chris-mc1/homeconnect_websocket`](https://github.com/chris-mc1/homeconnect_websocket) | Protocol/crypto behaviour reference (clean-room — no code reused) | **None (all rights reserved)** |
| [`chris-mc1/homeconnect_local_hass`](https://github.com/chris-mc1/homeconnect_local_hass) | Mapping / onboarding / resilience concepts (reimplemented) | MIT |
| [`bruestel/homeconnect-profile-downloader`](https://github.com/bruestel/homeconnect-profile-downloader) | External tool to obtain the profile archive (output consumed only; **not** reimplemented) | MIT |
| `SukramJ/go-mtec2mqtt` | Sister project: structure, conventions, reusable infrastructure | MIT |
| `SukramJ/openccu-loom` | Origin of the pure-Go MQTT client (`internal/mqtt`) | MIT |

## Clean-room statement (`homeconnect_websocket`)

The upstream Python library `chris-mc1/homeconnect_websocket` carries **no license
file**, which under default copyright means *all rights reserved*. Accordingly:

- **No source code, constants, structure, or test fixtures from it are copied or
  ported verbatim.**
- The Go implementation is written from the **language-neutral protocol
  specification** in [`docs/01-protocol.md`](docs/01-protocol.md) and
  [`docs/02-data-model.md`](docs/02-data-model.md), which describe protocol facts
  (the AES/HMAC scheme, message format, error codes, type tables) rather than any
  third-party expression. Code comments cite the spec section, not upstream files.
- Protocol-level facts (error-code numbers, refCID type tables, feature-name
  namespaces) originate from the Home Connect appliances/protocol themselves.

If the upstream author adds a permissive license (tracking issue #69), this
section can be relaxed to a normal attribution.

## Bundled / dependency licenses

Reused first-party code keeps its original copyright header
(`internal/mqtt/*` → "OpenCCU-Loom authors", MIT).

Go module dependencies (all permissive):

| Module | License |
|---|---|
| `github.com/coder/websocket` | ISC |
| `golang.org/x/sync` | BSD-3-Clause |
| `gopkg.in/yaml.v3` | MIT / Apache-2.0 |

Run `make licenses` (go-licenses) to re-verify dependency licenses before a release.
