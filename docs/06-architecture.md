# go-homeconnect2mqtt — Architecture & Concept

> Design of the Go implementation. Reusable infrastructure (Makefile, `internal/mqtt`,
> `internal/config`, Dockerfile, CI, the optional web/state UI) lives directly in this
> repository, following a consistent structure, consistent conventions, and a consistent
> resilience philosophy. Home Connect ⇒ MQTT, purely local, without cloud in normal operation.

References: protocol → `01-protocol.md`, data model → `02-data-model.md`,
onboarding → `03-profile-format.md`, mapping → `04-device-mapping.md`,
resilience → `05-resilience.md`.

---

## 1. Target Picture

A standalone daemon (+ optional utility CLI) that

- connects one or more Home Connect appliances (dishwasher, induction cooktop, washing machine)
  via the **local WebSocket protocol**,
- publishes their states/values to **MQTT** and applies write commands (command topics),
- optionally generates **Home Assistant MQTT discovery**,
- is **maximally resilient** against the real-world failures catalogued in `05-resilience.md`.

Guiding principles: **modular & testable**, **synchronous
startup** (errors visible early), **pure Go with minimal deps**, **operator-friendly**
(external YAML assets, XDG paths), **structured logging**, **graceful shutdown**.

---

## 2. Package Layout

M-TEC/Modbus packages are replaced by their Home Connect counterparts.

```
cmd/
├── homeconnect2mqtt/      # daemon entry point (main.go)
└── hc-util/               # CLI: parse profile, device dump, feature list, connection test

internal/
├── config/                # load YAML, env overrides, defaults, aggregated validation
│  ├── config.go           # Config struct (flat, yaml tags)
│  ├── load.go             # LoadFile(), Locate() (CWD/XDG/HOME), env coercion
│  ├── defaults.go
│  └── validate.go         # ValidationError (collects all problems)
│
├── profile/               # profile import (replaces "registers"): .json + 2 XML
│  ├── archive.go          # read ZIP/JSON, .json index, resolve XML paths
│  ├── description.go      # DeviceDescription/FeatureMapping → object model (02-data-model)
│  ├── parser.go           # tolerant XML parser (force-list, skip+log)
│  └── types.go            # type tables refCID→content/protocolType, enum subsets
│
├── homeconnect/           # protocol core (replaces "modbus") — port of the Python library
│  ├── socket.go           # transport: AES socket (ws:80) + TLS-PSK socket (wss:443)
│  ├── crypto.go           # KDF, AES-CBC stream, HMAC chain, padding (01-protocol §2/§3)
│  ├── message.go          # Message struct, Action enum, encode/decode
│  ├── session.go          # handshake, msgID/sID, send/sendSync, response correlation
│  ├── reconnect.go        # state machine, backoff+jitter, full state reset
│  ├── appliance.go        # high-level: entities from description, NOTIFY routing, init
│  ├── entities.go         # entity types + value semantics (02-data-model §6)
│  └── errors.go           # categorized error types
│
├── bridge/                # orchestration (equivalent to "coordinator") — the heart
│  ├── bridge.go           # Run() loop, one isolated worker per device
│  ├── device.go           # device worker: connect→sync→publish→subscribe, lifecycle
│  ├── publish.go          # entity → MQTT state; NOTIFY → topic update
│  ├── command.go          # MQTT /set → write (type/enum normalization, write window, start paths)
│  └── *_test.go           # table tests with stubs (stubMQTT, stubSession)
│
├── mqtt/                  # MQTT client (reused from the sister project)
│  ├── client.go           # publish/subscribe, QoS, LWT, retain, keep-alive
│  ├── lifecycle.go        # reconnect with backoff + subscription replay
│  └── protocol/           # (pure-Go client, as in the sister project)
│
├── hass/                  # Home Assistant MQTT discovery
│  ├── discovery.go        # Initialize(), entry generation, birth/LWT
│  └── payload.go          # JSON payload per platform (04-device-mapping §6.3)
│
├── mapping/               # optional enrichment (device_class/unit/start paths)
│  └── catalog.go          # loads mapping.yaml (operator-patchable)
│
├── state/                 # optional in-memory cache (only when web/health active) — see §10
│  ├── store.go            # thread-safe (RWMutex), Snapshot, UpdateDevice, SSE subscriber
│  └── types.go            # Snapshot, DeviceView, DeviceHealth (last-update age)
│
├── web/                   # optional status/health UI — see §10
│  ├── web.go              # HTTP listen, routing, basic auth
│  ├── api.go              # JSON API: status/health/values + write dispatch
│  ├── backend.go          # SSE push to the browser
│  └── static/             # embedded SPA (go:embed) + i18n (de/en)
│
└── version/               # build info (ldflags: Version, Commit, BuildDate)
```

**Keep external deps minimal** (only `yaml.v3` + `x/sync`). Additionally
required: a WebSocket library (e.g. `nhooyr.io/websocket` or `coder/websocket` or
`gorilla/websocket`); possibly a TLS-PSK library for older appliances (otherwise AES-only first).
XML via `encoding/xml`, crypto via the standard library (`crypto/aes`, `crypto/cipher`,
`crypto/hmac`, `crypto/sha256`).

---

## 3. Entry Point & Lifecycle (`cmd/homeconnect2mqtt/main.go`)

```go
func main() {
    logger := slog.New(slog.NewTextHandler(os.Stderr, ...))
    // Flags: --config, --profile/--devices, --mapping, --version
    os.Exit(run(...))
}

func run(...) error {
    cfg, _      := config.Load(configPath, logger)        // YAML → env → defaults → validate
    devices, _  := profile.LoadDevices(devicesPath)       // profiles/descriptions per device
    catalog, _  := mapping.Load(mappingPath)              // optional, lenient

    ctx, cancel := signal.NotifyContext(ctx, SIGINT, SIGTERM); defer cancel()

    mqttClient := mqtt.NewClient(...)                     // synchronous first connect
    lc := mqtt.NewLifecycle(mqttClient, ...)
    if err := lc.Start(ctx); err != nil { return err }
    defer lc.Stop(stopCtx)                                // graceful, with timeout

    var disc *hass.Discovery
    if cfg.HASSEnable { disc = hass.New(...) }

    b := bridge.New(bridge.Deps{MQTT: mqttClient, Devices: devices, Catalog: catalog, HASS: disc, ...})
    return b.Run(ctx)                                     // blocks until context done
}
```

- **Signal handling:** `signal.NotifyContext` → context cancel on Ctrl-C/SIGTERM.
- **errgroup** for parallel, bounded goroutines (bridge + optional web server).
- **Graceful shutdown:** MQTT disconnect with a short timeout; terminate all device workers;
  LWT to "offline".
- **Synchronous first connect** (MQTT) → startup errors immediately visible; appliances may be
  asynchronous/offline (FK-1).

---

## 4. Configuration (`internal/config`)

Flat `Config` struct with `yaml` tags, 1:1 with YAML; resolution **YAML → Env → Defaults →
Validate** (aggregated `ValidationError`); `Locate()` in CWD/XDG/HOME. Env prefix e.g.
`HC2M_*`.

Proposal for the most important fields:

```yaml
# MQTT
MQTT_SERVER: "tcp://localhost:1883"
MQTT_LOGIN: ""
MQTT_PASSWORD: ""
MQTT_TOPIC: "homeconnect"          # base topic
MQTT_QOS: 1
MQTT_RETAIN: true

# Home Assistant
HASS_ENABLE: true
HASS_BASE_TOPIC: "homeassistant"
HASS_BIRTH_GRACETIME: 15

# Connection / resilience (see 05-resilience.md)
APP_NAME: "go-homeconnect2mqtt"
APP_ID: ""                          # free-form; empty → generated
RECONNECT_INITIAL: 1                # s
RECONNECT_MAX: 30                   # s
RECONNECT_JITTER: 500               # ms
HANDSHAKE_TIMEOUT: 60               # s
SEND_TIMEOUT: 20                    # s
HEARTBEAT: 20                       # s
RESYNC_INTERVAL: 0                  # s; 0 = NOTIFY only (no polling needed)

# Web UI (optional — see §10)
WEB_ENABLE: false
WEB_BIND: "127.0.0.1:8080"          # localhost-only by default
WEB_USER: ""                         # basic auth (both empty = no auth)
WEB_PASSWORD: ""

# Misc
LANGUAGE: "de"                      # display names (values/entities stay technical)
DEBUG: false
```

**Appliances** kept separate (analogous to `registers.yaml`), so they remain operator-patchable:

```yaml
# devices.yaml
devices:
  - name: dishwasher
    host: 192.168.1.50               # IP or mDNS name; empty → default host from profile
    manual_host: true                # suppress mDNS update
    connection_type: AES             # AES | TLS
    psk64: "<key>"                   # from <serial>.json: "key"
    iv64: "<iv>"                     # from <serial>.json: "iv" (AES only)
    description: ./profiles/dishwasher.json   # parsed DeviceDescription (cached)
  - name: hob
    ...
  - name: washer
    ...
```

> Alternatively, reference the profile ZIP directly and parse it at startup (`profile.archive`).
> Recommended: `hc-util` parses the ZIP once → description JSON + device entry (secrets remain
> in the operator's domain).

---

## 5. Bridge / Orchestration (`internal/bridge`)

The core. **One isolated worker** per appliance (goroutine + sub-context), started via
`errgroup`/`x/sync`:

```
bridge.Run(ctx):
  for each device: go deviceWorker(ctx, dev)
  wait for ctx.Done

deviceWorker(ctx, dev):
  appliance = homeconnect.NewAppliance(dev.description, dev.host, psk, iv, ...)
  while ctx not cancelled:
     err = appliance.Connect(ctx)              # handshake + post-init
     if err: publishAvailability(offline); backoff(); continue   # FK-1
     publishAvailability(online)
     publishDiscovery() (once / on HA birth)
     onNotify(uid, value):  publishState(dev, entity)            # NOTIFY /ro/values
     onConnectionState(s):  publishConnectionState(dev, s)
     subscribeCommands(dev): handleSet(...)                      # MQTT /set
     <-ctx.Done | <-disconnected
     publishAvailability(offline)
     # on disconnect: backoff + reconnect (full state reset, 01-protocol §10)
```

**Properties:**
- **Appliance isolation:** a crashing/offline appliance does not affect the others.
- **Panic recovery** per worker (defensive depth).
- **Publish what you can:** publish partial updates; log errors and continue the loop.
- **Health per appliance:** expose connection state + last-update age over MQTT.

**Command handling (`command.go`):** MQTT `/set` → resolve feature via topic path → normalize
value (type/enum, float→int for #68, case-insensitive) → check access/available/write window
(FK-5) → device-specific write/start path (FK-4) → log/retry on error code.

---

## 6. Resilience Integration (Core Requirement)

Direct implementation of the `05-resilience.md` measures in code:

| Error class | Location | Measure |
|---|---|---|
| FK-1 Reconnect | `homeconnect/reconnect.go`, `bridge/device.go` | backoff+jitter, offline≠error, connect timeout, log throttle, worker isolation |
| FK-2 HMAC | `homeconnect/crypto.go`, `socket.go` | constant-time, reconnect on failure, TX/RX mutex, CBC stream |
| FK-3 Parser | `profile/parser.go`, `entities.go` | force-list, skip+log, nil guards, regex `\d+` |
| FK-4 Program start | `bridge/command.go` | three start paths + hood-fan DELETE |
| FK-5 Write window | `bridge/command.go`, `entities.go` | evaluate descriptionChange, write gating/retry |
| FK-6 Type/Enum | `homeconnect/entities.go`, `profile/types.go` | float→int, case-insensitive, enum miss→raw value |
| FK-7 Onboarding/IPv6 | `config`, `profile`, `hc-util` | categorized errors, manual IP, IPv6 brackets |
| FK-8 Coverage | `bridge/publish.go` | generically expose all features |

---

## 7. Logging, Build, Quality

- **Logging:** `slog` (TextHandler, Info by default, Debug when `DEBUG=true`); structured
  keys (`device`, `resource`, `code`, `err`); **redaction** for psk/iv/serialNumber/mac/shipSki/deviceID.
- **Makefile targets:** `setup`, `build`, `test` (`-race`), `check` (vet+fmt+lint+test),
  `fmt` (gofumpt+goimports), `lint` (golangci-lint), `docker`, `release` (cross-compile
  linux/amd64+arm64, darwin/arm64).
- **Dockerfile:** multi-stage, distroless `nonroot`, `CGO_ENABLED=0` (⚠️ exception if
  TLS-PSK requires a cgo library — then a separate build target), `mapping.yaml`/profiles as
  volume/asset.
- **.golangci.yaml:** strict linter list (errcheck, errorlint,
  gosec, govet, revive, sloglint, staticcheck, …).
- **CI:** lint + test matrix (Ubuntu/macOS/Windows) with race detector + build sanity.
- **Git hooks:** block commits on `main` (feature branch + PR).
- **Conventions:** SPDX+copyright header per file; **wrap** errors (`errors.Is`),
  do not compare them; **lenient loading** (skip instead of fatal); narrow interfaces + fakes in tests;
  table tests. Clean-room: cite the protocol spec in comments
  (e.g. `// per docs/01-protocol.md §3.3`), never third-party source.

---

## 8. Test Strategy

- **Crypto vectors:** test AES/HMAC/padding against known inputs/outputs (round-trip
  encrypt→decrypt; HMAC chain over multiple frames; padding lengths `""`→16, `"a"`→15,
  15→17, 16→16). The Python `tests/utils.py` provides the mirrored server side as a reference.
- **Parser:** against the fixture XMLs (embedded in `07-reference-sources.md`) + deliberately
  broken variants (single vs. list, missing fields, uppercase enums, greedy group IDs).
- **Handshake/session:** stub socket that replays the server sequence (`/ei/initialValues` → …);
  msgID correlation, timeout, `code` errors.
- **Reconnect:** simulated drop → backoff, full state reset, resync.
- **Bridge/command:** stub session + stub MQTT; table tests for normalization,
  write-window gating, start paths.
- **Race detector** mandatory (CBC/HMAC state, worker concurrency).

---

## 9. Roadmap (Incremental, Every Stage Runnable)

1. **Crypto + transport** (AES socket): KDF, CBC stream, HMAC chain, padding, WS connect,
   heartbeat. Unit-tested against vectors. *(Lowest-risk core first.)*
2. **Message + session + handshake**: encode/decode, `/ei/initialValues`→…→CONNECTED,
   sendSync correlation, post-init (`allMandatoryValues`).
3. **Profile parser**: ZIP/JSON + XML → description/entities; type tables; tolerant parser.
4. **Reconnect**: state machine, backoff+jitter, full state reset, offline handling.
5. **Bridge + MQTT publish**: entities → state topics, NOTIFY updates, availability/LWT,
   connection state. *(First end-to-end: read-only mirroring.)*
6. **Command topics**: writing with normalization, write window, device-specific start paths.
7. **HA discovery**: platform heuristics, payloads, birth/LWT.
8. **Enrichment (`mapping.yaml`)**: device_class/units/defaults; `hc-util` for onboarding.
9. **TLS-PSK** (older appliances) — if needed; after clarifying the Go TLS-PSK option.
10. **Optional:** web UI + state cache (see §10), mDNS discovery.

> Stages 1–2 are the highest technical risk point (undocumented crypto). Therefore
> first, with hard test vectors, before bridge/MQTT are layered on top.

---

## 10. Optional Web UI & State Cache

This project ships an **optional** status/health UI that is **disabled by default**
(`internal/web/` + `internal/state/`) — as an *add-on*, not as the core: without
`WEB_ENABLE=true` the daemon remains a pure MQTT bridge.

### 10.1 Purpose

With an undocumented API, **observability** is especially valuable. The UI shows at
a glance what otherwise only appears in the log:
- Per appliance: connection state (connecting/handshake/connected/reconnecting/offline) + **age
  of the last update** (immediately detects hung polling / silent desyncs, cf. FK-1/FK-2).
- Current feature values per appliance (live, without having to open an MQTT client).
- Optional write dispatch (set a feature directly from the UI — useful for testing the
  device-specific start paths, FK-4).

### 10.2 `internal/state/` — In-Memory Cache (Only Active With `WEB_ENABLE`)

A `state.Store`:
- Thread-safe (`sync.RWMutex`); `UpdateDevice(device, values)` is fed from the
  `bridge.publish` path in parallel with the MQTT publish (one entry per entity).
- `Snapshot()` returns a consistent copy for the JSON API.
- **`DeviceHealth`**: `UpdatedAt`, `AgeSeconds`, `ConnectionState`, `EntityCount` — the basis
  of "stuck detection".
- **SSE subscribers**: one buffered channel (capacity 1) per browser for push updates.

> If `WEB_ENABLE=false`, the store is **not** instantiated at all — no overhead.

### 10.3 `internal/web/` — HTTP Server + SPA

- `web.go`: HTTP listener on `WEB_BIND` (default `127.0.0.1:8080` — localhost-only, never
  accidentally on the network), optional **HTTP basic auth** (`WEB_USER`/`WEB_PASSWORD`; both empty
  ⇒ no auth, e.g. behind a reverse proxy).
- `api.go`: JSON API — `GET /api/status`, `GET /api/health`, `GET /api/devices` (current
  values), `POST /api/devices/<device>/set` (write dispatch into the bridge).
- `backend.go`: **SSE** (`GET /api/events`) for live push to the browser.
- `static/`: small embedded SPA via **`go:embed`** (an exception to the "don't embed" rule —
  UI assets are not intended to be operator-patchable) + **i18n** (de/en, per `LANGUAGE`).

### 10.4 Lifecycle Integration

When `WEB_ENABLE=true`, the UI is started as a second goroutine via `errgroup`
alongside the bridge — an error/cancel shuts both down cleanly:

```go
if cfg.WebEnable {
    store := state.New()
    b := bridge.New(bridge.Deps{ ..., State: store })   // bridge feeds the store
    webSrv := web.New(cfg, store, b)                      // b for write dispatch
    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return b.Run(gctx) })
    g.Go(func() error { return webSrv.Run(gctx) })
    return g.Wait()
}
return bridge.New(bridge.Deps{ ... }).Run(ctx)            // without UI: pure bridge
```

### 10.5 Scope Boundaries

- **Optional & opt-in** — disabled by default; core operation (MQTT/HA) does not need the UI.
- The UI is **not** a replacement for Home Assistant; it is a lightweight diagnostic/
  health dashboard, primarily for onboarding and troubleshooting the
  undocumented appliance API.
- Implemented only in **roadmap stage 10** — after the stable MQTT core.

### 10.6 Interface Contract

The complete HTTP API and SSE contract (endpoint schemas, JSON payloads, event format,
error taxonomy, routing/test strategy) is specified in **[`08-web-api.md`](08-web-api.md)**.
