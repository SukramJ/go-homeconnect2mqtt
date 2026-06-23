# CLAUDE.md — go-homeconnect2mqtt

Guidance for working in this repository.

## What this is

A local, cloud-free bridge from Home Connect appliances (Bosch / Siemens /
Gaggenau / Neff) to MQTT, with optional Home Assistant discovery. It is a
sister project of `go-mtec2mqtt`: same structure, conventions and
resilience philosophy. The full knowledge base lives in `docs/` (protocol,
data model, profile format, device mapping, resilience, architecture, web
API) and the phased implementation plan is `docs/10-implementation-plan.md`.

## Language & identity

- **Project language is English**: all code, comments, identifiers, docs
  authored for the project, commit messages and the README are in English.
  (The `docs/01`–`docs/09` research corpus is the German source material we
  port from; new artefacts are English.)
- Code is authored under the `SukramJ` handle; keep the existing
  `SPDX-License-Identifier: MIT` + `Copyright (C) 2026 SukramJ` headers
  (MQTT files carry the upstream `OpenCCU-Loom authors` header — preserve it).

## Conventions

- **Go 1.26+**, `CGO_ENABLED=0` for production builds (exception: a TLS-PSK
  cgo path, kept behind a separate build target). Tests run with
  `CGO_ENABLED=1 go test -race`.
- **Do not `go:embed` operator assets** (`devices.yaml` / `mapping.yaml` are
  operator-patchable and shipped next to the binary). The web SPA is the one
  embed exception.
- **Direct commits to `main` are blocked** via `.githooks/pre-commit`
  (`make setup`/`make hooks`); use a feature branch + PR.
- **Every file** starts with `// SPDX-License-Identifier: MIT` + copyright.
- **Wrap errors** (`errors.Is`/`errors.As`), never compare. **Lenient
  loading**: skip + log instead of fatal. Structured `slog` with context
  keys (`device`, `resource`, `code`, `err`).
- **Redaction is mandatory**: never log or publish `psk`/`iv`/`serialNumber`/
  `mac`/`shipSki`/`deviceID`.
- **Tests**: narrow interfaces + injected fakes (stub one collaborator per
  test), a fixed clock via `Deps.Now`, table-driven cases. High coverage is
  a project goal — every package ships meaningful tests.
- Where Python behaviour is ported, cite the source in a comment
  (e.g. `// mirrors hc_socket.AesSocket._receive`).
- **Resilience contract**: "publish what you can, retry next tick" — a
  read/publish error is logged and the loop continues; only `ctx` cancel
  stops a worker. Per-device and per-entity isolation (see
  `docs/05-resilienz.md`).

## Common commands

```
make setup        # install tooling + git hooks
make build        # build both binaries into bin/
make test         # go test -race ./...
make test-cover   # tests + coverage summary
make check        # vet + fmt-check + lint + test (the PR gate)
make run          # run the daemon against ./config.yaml + ./devices.yaml
```
