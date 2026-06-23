# go-homeconnect2mqtt — Implementation Plan (trackable)

> Concrete, step-by-step plan derived from documents `01`–`09`. Structured as a
> **checklist plan**: each phase is independently buildable/testable, has a hard
> **test gate** (acceptance criterion), names the **affected files** (layout from
> `06-architektur-konzept.md` §2) and references the underlying spec.
>
> **Legend:** `[ ]` open · `[~]` in progress · `[x]` done · ⭐ critical path · 🧪 test gate.
> Order follows the roadmap in `06` §9, made more granular and extended with
> bootstrap/release phases.

---

## 0. Progress overview (master tracker)

| Phase | Title | Status | Test gate met | Depends on |
|---|---|---|---|---|
| P0 | Project bootstrap & infrastructure | `[x]` | ✅ | — |
| P1 | ⭐ Crypto + AES transport | `[x]` | ✅ | P0 |
| P2 | ⭐ Message + session + handshake | `[x]` | ✅ | P1 |
| P3 | Profile import & XML parser | `[x]` | ✅ | P0 |
| P4 | Reconnect state machine | `[ ]` | `[ ]` | P2 |
| P5 | Entities + appliance (high-level) | `[x]` | ✅ | P2, P3 |
| P6 | Bridge + MQTT publish (read-only E2E) | `[ ]` | `[ ]` | P4, P5 |
| P7 | Command topics (writing) | `[ ]` | `[ ]` | P6 |
| P8 | Home Assistant discovery | `[ ]` | `[ ]` | P6 |
| P9 | Enrichment (`mapping.yaml`) + `hc-util` | `[ ]` | `[ ]` | P5, P6 |
| P10 | TLS-PSK (older appliances) | `[ ]` | `[ ]` | P2 |
| P11 | Optional web UI + state cache | `[ ]` | `[ ]` | P6 |
| P12 | Hardening, docs & release | `[ ]` | `[ ]` | all |

**Critical path:** P0 → P1 → P2 → P4/P5 → P6 → P7. P3 runs in parallel with P1/P2.
P8–P11 are additive; P10/P11 are optional.

---

## P0 — Project bootstrap & infrastructure

**Goal:** build/test/lint scaffold stands; `make check` is green on the empty
skeleton. All verbatim artefacts from `08-schwesterprojekt-vorlage.md` adopted and
renamed to `homeconnect2mqtt`.

- [x] `go mod init github.com/SukramJ/go-homeconnect2mqtt` (Go 1.26)
- [x] Global rename per `08` §0 (module, binary names, `HC2M_` prefix, ClientID, AppDir)
- [x] Directory skeleton (`06` §2)
- [x] `internal/version/version.go`
- [x] `internal/mqtt/` adopted verbatim incl. tests (path rewrite only; upstream headers kept)
- [x] `internal/config/load.go` engine adopted; HC2M `config.go`/`defaults.go`/`validate.go` authored
- [x] `Makefile`, `.golangci.yaml`, `Dockerfile`, `.dockerignore`, `.gitignore`, `.githooks/pre-commit`
- [x] `CLAUDE.md` (English conventions)
- [x] CI workflow: lint + test matrix (Ubuntu/macOS/Windows) with `-race` + build sanity
- [x] `config-template.yaml`, `devices-template.yaml`, `mapping.yaml` stub, `changelog.md`, `README.md`
- [x] SPDX + copyright header in every new file
- [x] `cmd/homeconnect2mqtt` and `cmd/hc-util` minimal entry points

**🧪 Test gate:** `make check` green on the skeleton; `go build ./...` builds both
binaries; git hook blocks direct commits on `main`. Config package fully tested.

---

## P1 — ⭐ Crypto + AES transport (`internal/homeconnect`)

**Goal:** lowest-risk but most delicate core first (`06` §9). The AES socket speaks the
app-layer crypto protocol exactly like the Python reference.

Spec: `01-protokoll.md` §1–§3, reference code `07-referenz-quellen.md` §1.

- [ ] `crypto.go`: base64 `RawURLEncoding` for `psk64`/`iv64` (`01` §2)
- [ ] KDF: `enckey=HMAC-SHA256(psk,"ENC")`, `mackey=HMAC-SHA256(psk,"MAC")`; `iv` direct (no HKDF)
- [ ] Custom padding (**not** PKCS#7): `pad_len=16-(len%16)`, if `==1` → `+16`; `clear‖0x00‖rand(pad_len-2)‖byte(pad_len)`
- [ ] TX: persistent CBC object (stream chaining) → `HMAC(mackey, iv‖0x45‖last_tx_hmac‖ct)[:16]`; wire `ct‖mac`
- [ ] RX: split `ct|mac` → `HMAC(mackey, iv‖0x43‖last_rx_hmac‖ct)[:16]` **constant-time** (`hmac.Equal`) → CBC-decrypt → unpad
- [ ] RX frame validation: BINARY, `len>=32`, `len%16==0`
- [ ] **State reset hook** per connection: `last_{tx,rx}_hmac=16×0x00`, fresh CBC objects from `iv`
- [ ] TX and RX state each behind its own mutex, strictly serialized
- [ ] HMAC/padding/decode error → typed `AuthError` forcing full reconnect (no further reads)
- [ ] `socket.go` (AES): WebSocket `ws://<host>:80/homeconnect`, no HTTP headers; IPv6 host building (strip→wrap brackets, zone id)
- [ ] Heartbeat: 20s ping ticker + pong deadline
- [ ] WebSocket lib chosen & in `go.mod` (`coder/websocket`)

**🧪 Test gate (`crypto_test.go`, `-race`):** padding lengths exact (`""`→16, `"a"`→15,
15→17, 16→16); multi-frame round-trip; HMAC chain vs. mirrored server side; tampered
frame → `AuthError`; IPv6 host not double-bracketed.

---

## P2 — ⭐ Message + session + handshake (`internal/homeconnect`)

**Goal:** full handshake to `CONNECTED` + post-init; request/response correlation.

Spec: `01-protokoll.md` §5, §6, §8, §9.

- [ ] `message.go`: struct, compact JSON separators, `data` always a list on send
- [ ] Defensive reads: cast `sID/msgID/version` to int; object-JSON `]"`→`]` workaround
- [ ] `Action` enum `GET|POST|RESPONSE|NOTIFY`
- [ ] Full error-code table (`01` §9) + `CodeResponseError`
- [ ] Pre-handshake: await first device message `/ei/initialValues` → `sID`, `last_msg_id`
- [ ] Handshake sequence (`01` §6.2) incl. `deviceType` v1/v2, nonce, `/ci/authentication`
- [ ] Default resolution before send: version/sID/msgID (`01` §6.4)
- [ ] `sendSync`: per-msgID queue, correlation, 20s timeout; NOTIFY → general handler
- [ ] Post-connect init: `/ro/allDescriptionChanges` + `/ro/allMandatoryValues`, tolerate `500`

**🧪 Test gate (`session_test.go`):** stub socket replays the server sequence; CONNECTED
reached; msgID correlation + timeout + `code` error tested; `500` in post-init does not abort.

---

## P3 — Profile import & tolerant XML parser (`internal/profile`)

**Goal:** ZIP/JSON + 2×XML → `DeviceDescription` object model; tolerant of model variance (FK-3).

Spec: `02-datenmodell.md`, `03-profil-format.md`, fixtures `07` §2/§3.

- [ ] `archive.go`: read ZIP or loose files; find all `*.json`; resolve XML paths via JSON fields
- [ ] map `<serial>.json` fields; host defaults (AES→`haId`, TLS→`brand-type-haId`); `manual_host`
- [ ] `types.go`: full `refCID→protocolType` and `refCID→contentType` tables (`02` §4)
- [ ] `parser.go` FeatureMapping → 3 maps; `force_list`; resolve enum `subsetOf`
- [ ] `description.go` recursive DeviceDescription parse; hex→int, name link, lowercase access/execution
- [ ] Robustness: `force_list`, per-section try/skip+log, skip unknown — never drop whole device
- [ ] cache parsed description as JSON
- [ ] categorized error types; secret redaction helper (psk/iv/serialNumber/mac/shipSki/deviceID)

**🧪 Test gate (`parser_test.go`):** verify against fixtures `07` §2/§3 (e.g. `uid 0x1002`→
`Status.2`, enum `{0:Open,1:Closed}`; `enid 0x3003 subsetOf`→`{1:Present}`); broken variants
skip+log instead of crash.

---

## P4 — Reconnect state machine (`internal/homeconnect/reconnect.go`)

**Goal:** full reconnect with correct state reset; offline is normal (FK-1).

Spec: `01-protokoll.md` §10, `05-resilienz.md` FK-1/FK-2.

- [ ] State machine `CONNECTING→HANDSHAKE→CONNECTED→RECONNECTING / CLOSING→CLOSED / ABNORMAL_CLOSURE`
- [ ] full state reset on every reconnect: crypto, session, services, entity resync
- [ ] exponential backoff + jitter (1s→30s, ±500ms), reusing the `mqtt.Lifecycle` pattern
- [ ] connect timeout at startup (an off appliance must not block daemon start, #339)
- [ ] log rate-limiting for recurring connect errors (#41)
- [ ] 404 handshake → fresh socket rather than retry on dead socket (#403)
- [ ] HMAC failure (P1) triggers an immediate full reconnect
- [ ] state-change callback (`onConnectionState`) for bridge/MQTT

**🧪 Test gate (`reconnect_test.go`, fixed clock):** simulated drop → exponential backoff with
jitter; after reconnect HMAC chains nulled & entities resynced; permanent offline yields no
unbounded log/CPU growth; ctx cancel exits cleanly.

---

## P5 — Entities + appliance high-level (`internal/homeconnect`)

**Goal:** typed entities from the description; NOTIFY routing; value semantics (`02` §6).

Spec: `02-datenmodell.md` §6/§7, `05-resilienz.md` FK-6.

- [ ] `entities.go` entity types indexed by `uid` and `name`
- [ ] value fields `value_raw`/`value`(enum→name)/`value_shadow`; `enumeration`+`rev_enumeration`
- [ ] `update()` from NOTIFY/RESPONSE: value cast, access/available/min/max/step, `execution` lowercase (#70)
- [ ] type cast `TYPE_MAPPING`: bool case-insensitive, int, float, string, object(JSON+`]"` workaround)
- [ ] enum miss → raw value (#56); unknown program uid → None (#66)
- [ ] `appliance.go`: build entities (per-entity isolation, skip+log); route NOTIFY; `Connect()`
- [ ] callbacks `onNotify`/`onDescriptionChange`

**🧪 Test gate (`entities_test.go`):** NOTIFY updates value/access/available; uppercase
`SELECTANDSTART` lowercased (#70); enum miss returns raw (#56); one broken feature isolated.

---

## P6 — Bridge + MQTT publish (read-only E2E) (`internal/bridge`)

**Goal:** first end-to-end: device values mirrored read-only to MQTT; one isolated worker per
device (FK-1).

Spec: `06-architektur-konzept.md` §3/§5/§6, `04-geraete-mapping.md` §6.1.

- [ ] `config` filled with HC2M fields (done in P0)
- [ ] `devices.yaml` loader (`profile.LoadDevices`)
- [ ] `cmd/homeconnect2mqtt/main.go`: flags, `signal.NotifyContext`, synchronous MQTT first-connect, `errgroup`, graceful shutdown + offline LWT
- [ ] `bridge.go` `Run()`: per-device goroutine + sub-context
- [ ] `device.go` worker loop: connect→sync→publish→subscribe; panic recovery; offline≠error
- [ ] `publish.go`: topic schema; publish enum names, optional raw attribute
- [ ] availability (online/offline, LWT) + connection_state topic
- [ ] expose every feature generically (FK-8)
- [ ] redaction throughout logging

**🧪 Test gate (`bridge_test.go` with stubMQTT+stubSession):** NOTIFY → correct state topic +
payload; availability/LWT/connection_state correct; a crashing/offline device does not affect
others; no secret in topic/log.

---

## P7 — Command topics (writing) (`internal/bridge/command.go`)

**Goal:** MQTT `/set` writes values/programs; device-specific start paths + write window (FK-4/5/6).

Spec: `04-geraete-mapping.md` §6.4, `05-resilienz.md` FK-4/5/6, `01` §7.

- [ ] set topic per writable feature; topic→feature resolution
- [ ] value normalization: enum name→raw (case-insensitive); float→int at stepSize==1 (#68); type cast
- [ ] write gating: check access/available; react to descriptionChange; await READWRITE window (uid 256, #384)
- [ ] `POST /ro/values`; optimistic `value_shadow`
- [ ] three start paths (FK-4): standard `activeProgram`; hob direct `selectedProgram` (validate=false); command `StartProgram`
- [ ] hood fan-off via DELETE `/ro/activeProgram` (#386)
- [ ] delayed-start per device type: dishwasher `StartInRelative`; washer/dryer `FinishInRelative` (uid 551)
- [ ] acknowledge/reject events
- [ ] error codes 400/501/541 logged, optionally retried, state unchanged

**🧪 Test gate (`command_test.go`, table-driven):** normalization correct; out-of-window write
gated/retried; all three start paths + hood DELETE + delayed-start fields per device type covered.

---

## P8 — Home Assistant MQTT discovery (`internal/hass`)

**Goal:** optional HA discovery payloads per platform; birth/LWT re-publish.

Spec: `04-geraete-mapping.md` §1/§6.3.

- [ ] `discovery.go`: `Initialize()`, entry generation, birth + LWT
- [ ] platform heuristic: switch/select/sensor/binary_sensor/number/button/event_sensor/light/fan
- [ ] power switch/select generator, program generator, start button, fallbacks
- [ ] `payload.go`: JSON per platform with unique_id/topics/device_class/unit/options/device{}
- [ ] hob zones dynamic via strict regex `^Cooking\.Hob\.Status\.Zone\.(\d+)\.` (FK-3)

**🧪 Test gate (`hass_test.go`):** platform assignment per feature type correct; payload schema
valid; birth triggers re-publish; hob-zone regex does not crash on `001.RemainingProgramTime`.

---

## P9 — Enrichment (`mapping.yaml`) + `hc-util` CLI

**Goal:** operator-patchable enrichment + onboarding CLI.

Spec: `04` §6.2, `03` §5, `06` §2.

- [ ] `internal/mapping/catalog.go`: lenient `mapping.yaml` load (skip+log)
- [ ] initial `mapping.yaml` content from catalogues `04` §2–§5
- [ ] `cmd/hc-util`: subcommands `parse`, `dump`, `connection-test`
- [ ] `connection-test` with categorized errors + manual-IP escalation (FK-7)

**🧪 Test gate:** `hc-util parse` produces valid description JSON + device entry from a fixture
ZIP; `dump` lists all entities; `connection-test` returns a categorized error.

---

## P10 — TLS-PSK for older appliances (`internal/homeconnect/socket.go`)

**Goal:** second build stage; `connectionType=="TLS"` (`wss://host:443`).

Spec: `01-protokoll.md` §4, `07` §1.

- [ ] clarify Go TLS-PSK option (PSK-capable TLS 1.2 lib vs. std-lib limits)
- [ ] TLS client: max TLS 1.2, PSK ciphers, no hostname check, PSK callback
- [ ] messages as plain UTF-8 text over the tunnel
- [ ] TLS handshake error → categorized auth error + IP fallback (#297)
- [ ] if cgo required: separate build target/image, documented

**🧪 Test gate:** mode selection (`iv64` present→AES, else TLS) correct; TLS path verified;
cgo build target documented & green.

---

## P11 — Optional web UI + state cache (`internal/state`, `internal/web`)

**Goal:** opt-in diagnostics/health dashboard (off by default). Core operation unaffected.

Spec: `06-architektur-konzept.md` §10, `09-web-api.md` (full HTTP/SSE contract).

- [ ] `state/store.go`: thread-safe, `UpdateDevice`, `Snapshot`, `DeviceHealth`, SSE subscriber — only instantiated when `WEB_ENABLE`
- [ ] `bridge.publish` feeds the store in parallel with MQTT
- [ ] `web/web.go`: `net/http` routing, bind `127.0.0.1:8080`, optional basic auth
- [ ] `web/api.go` REST endpoints + `POST /api/devices/{d}/set`
- [ ] `Feature`/`DeviceSummary`/`Error` schemas + error taxonomy; secret redaction
- [ ] `web/backend.go` SSE `/api/events`: snapshot first, then value/connection/health, heartbeat
- [ ] health probe: 200 on `ok`, 503 on `degraded`; stale detection
- [ ] `web/static/`: embedded SPA via `go:embed` + i18n de/en
- [ ] lifecycle: second errgroup goroutine next to the bridge

**🧪 Test gate (`web/*_test.go`):** httptest server + stubs; per endpoint 200/4xx/5xx + schema +
redaction; auth with/without creds; SSE snapshot-first + heartbeat + slow subscriber drops;
set-dispatch maps device codes to the taxonomy.

---

## P12 — Hardening, docs & release

**Goal:** production readiness, observability, reproducible release.

Spec: `05-resilienz.md` §3, `06` §7/§8, `08` §1.1/§4.

- [ ] resilience principles audit (`05` §3): device/entity isolation, offline handling, full
      reconnect, write gating, tolerant read/strict write, observability, no secrets
- [ ] verify FK-1…FK-8 ↔ code mapping (`06` §6)
- [ ] `go test -race ./...` + `make check` green; `govulncheck` + `go-licenses`
- [ ] device-specific issue matrix anchored as regression tests/notes (`05` §2)
- [ ] `README.md` complete; `changelog.md` + version bump
- [ ] `make release` (linux/amd64+arm64, darwin/arm64) + `make docker`
- [ ] structured logging final review (keys, redaction)

**🧪 Test gate:** release artefacts + SHA256SUMS built; docker image runs as `nonroot`; real
multi-device setup stable over ≥24h.

---

## Cross-cutting: Definition of Done (for every phase)

- [ ] code + tests in the same feature branch, PR against `main` (direct commit blocked)
- [ ] `make check` green (`vet` + `gofumpt` + `golangci-lint` + `go test -race`)
- [ ] new files carry SPDX + copyright headers
- [ ] where Python behaviour is ported, the source is cited in a comment
- [ ] errors wrapped (`errors.Is`), lenient loading (skip+log instead of fatal)
- [ ] no secrets in logs/topics/HTTP (psk/iv/serialNumber/mac/shipSki/deviceID)
- [ ] phase test gate met and master tracker (§0) set to `[x]`

## Open decisions / risks

| # | Topic | Decide before | Default recommendation |
|---|---|---|---|
| R1 | WebSocket lib (`coder/websocket` vs. `gorilla/websocket`) | P1 | `coder/websocket` (modern, ctx-native) — chosen |
| R2 | TLS-PSK solvable without cgo? | P10 | prioritize AES-only; TLS-PSK later, possibly cgo build target |
| R3 | Device mix for first test (AES vs. TLS) | P6 | start with an AES device (std-lib path) |
| R4 | License compliance of ported constants/logic | P12 | check upstream repo licenses (`07` §4, #69) |
