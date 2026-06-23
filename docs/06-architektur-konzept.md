# go-homeconnect2mqtt — Architektur & Konzept

> Konzeption der Go-Implementierung als **Schwesterprojekt von `go-mtec2mqtt`**: gleiche
> Struktur, gleiche Konventionen, gleiche Resilienz-Philosophie. Home Connect ⇒ MQTT,
> rein lokal, ohne Cloud im Normalbetrieb. Quelle der Blaupause: Analyse von
> `../go-mtec2mqtt` (CLAUDE.md, internal/*, Makefile, Dockerfile, CI).

Referenzen: Protokoll → `01-protokoll.md`, Datenmodell → `02-datenmodell.md`,
Onboarding → `03-profil-format.md`, Mapping → `04-geraete-mapping.md`,
Resilienz → `05-resilienz.md`.

---

## 1. Zielbild

Ein eigenständiger Daemon (+ optionales Util-CLI), der

- ein oder mehrere Home-Connect-Geräte (Geschirrspüler, Induktionskochfeld, Waschmaschine)
  über das **lokale WebSocket-Protokoll** anbindet,
- deren Zustände/Werte nach **MQTT** publiziert und Schreibbefehle (Command-Topics) umsetzt,
- optional **Home-Assistant-MQTT-Discovery** erzeugt,
- **maximal resilient** gegen die in `05-resilienz.md` katalogisierten Praxisfehler ist.

Leitprinzipien (vom Schwesterprojekt übernommen): **modular & testbar**, **synchroner
Startup** (Fehler früh sichtbar), **Pure-Go mit minimalen Deps**, **operator-freundlich**
(externe YAML-Assets, XDG-Pfade), **strukturiertes Logging**, **graceful shutdown**.

---

## 2. Paket-Layout

Spiegelt `go-mtec2mqtt`; M-TEC/Modbus-Pakete werden durch Home-Connect-Pendants ersetzt.

```
cmd/
├── homeconnect2mqtt/      # Daemon-Einstiegspunkt (main.go)
└── hc-util/               # CLI: Profil parsen, Geräte-Dump, Feature-Liste, Connection-Test

internal/
├── config/                # YAML laden, Env-Overrides, Defaults, aggregierte Validierung
│  ├── config.go           # Config-Struct (flach, yaml-Tags)
│  ├── load.go             # LoadFile(), Locate() (CWD/XDG/HOME), Env-Coercion
│  ├── defaults.go
│  └── validate.go         # ValidationError (sammelt alle Probleme)
│
├── profile/               # Profil-Import (ersetzt "registers"): .json + 2 XML
│  ├── archive.go          # ZIP/JSON einlesen, .json-Index, XML-Pfade auflösen
│  ├── description.go      # DeviceDescription/FeatureMapping → Objektmodell (02-datenmodell)
│  ├── parser.go           # toleranter XML-Parser (force-list, skip+log)
│  └── types.go            # Typ-Tabellen refCID→content/protocolType, Enum-Subsets
│
├── homeconnect/           # Protokoll-Kern (ersetzt "modbus") — Port der Python-Library
│  ├── socket.go           # Transport: AES-Socket (ws:80) + TLS-PSK-Socket (wss:443)
│  ├── crypto.go           # KDF, AES-CBC-Stream, HMAC-Kette, Padding (01-protokoll §2/§3)
│  ├── message.go          # Message-Struct, Action-Enum, encode/decode
│  ├── session.go          # Handshake, msgID/sID, send/sendSync, Response-Korrelation
│  ├── reconnect.go        # State-Machine, Backoff+Jitter, voller State-Reset
│  ├── appliance.go        # High-Level: Entities aus Description, NOTIFY-Routing, Init
│  ├── entities.go         # Entity-Typen + Wert-Semantik (02-datenmodell §6)
│  └── errors.go           # kategorisierte Fehlertypen
│
├── bridge/                # Orchestrierung (entspricht "coordinator") — Herzstück
│  ├── bridge.go           # Run()-Loop, pro Gerät ein isolierter Worker
│  ├── device.go           # Device-Worker: connect→sync→publish→subscribe, Lifecycle
│  ├── publish.go          # Entity → MQTT-State; NOTIFY → Topic-Update
│  ├── command.go          # MQTT /set → write (Typ/Enum-Normalisierung, Schreibfenster, Startpfade)
│  └── *_test.go           # Tabellentests mit Stubs (stubMQTT, stubSession)
│
├── mqtt/                  # MQTT-Client (vom Schwesterprojekt übernehmbar)
│  ├── client.go           # Publish/Subscribe, QoS, LWT, Retain, KeepAlive
│  ├── lifecycle.go        # Reconnect mit Backoff + Subscription-Replay
│  └── protocol/           # (falls Pure-Go-Client wie im Schwesterprojekt)
│
├── hass/                  # Home-Assistant-MQTT-Discovery
│  ├── discovery.go        # Initialize(), Entry-Generierung, Birth/LWT
│  └── payload.go          # JSON-Payload je Plattform (04-geraete-mapping §6.3)
│
├── mapping/               # optionale Anreicherung (device_class/Einheit/Startpfade)
│  └── catalog.go          # lädt mapping.yaml (operator-patchbar, analog registers.yaml)
│
├── state/                 # optionaler In-Memory-Cache (nur wenn Web/Health aktiv) — siehe §10
│  ├── store.go            # thread-safe (RWMutex), Snapshot, UpdateDevice, SSE-Subscriber
│  └── types.go            # Snapshot, DeviceView, DeviceHealth (last-update-Alter)
│
├── web/                   # optionale Status-/Health-UI (Parität zum Schwesterprojekt) — siehe §10
│  ├── web.go              # HTTP-Listen, Routing, Basic-Auth
│  ├── api.go              # JSON-API: status/health/values + Write-Dispatch
│  ├── backend.go          # SSE-Push zum Browser
│  └── static/             # eingebettete SPA (go:embed) + i18n (de/en)
│
└── version/               # Build-Info (ldflags: Version, Commit, BuildDate)
```

**Externe Deps minimal halten** (Schwesterprojekt: nur `yaml.v3` + `x/sync`). Zusätzlich
nötig: eine WebSocket-Lib (z. B. `nhooyr.io/websocket` bzw. `coder/websocket` oder
`gorilla/websocket`); ggf. eine TLS-PSK-Lib für ältere Geräte (sonst AES-only zuerst).
XML via `encoding/xml`, Krypto via Standardbibliothek (`crypto/aes`, `crypto/cipher`,
`crypto/hmac`, `crypto/sha256`).

---

## 3. Einstiegspunkt & Lifecycle (`cmd/homeconnect2mqtt/main.go`)

Nach dem Muster des Schwesterprojekts:

```go
func main() {
    logger := slog.New(slog.NewTextHandler(os.Stderr, ...))
    // Flags: --config, --profile/--devices, --mapping, --version
    os.Exit(run(...))
}

func run(...) error {
    cfg, _      := config.Load(configPath, logger)        // YAML → env → defaults → validate
    devices, _  := profile.LoadDevices(devicesPath)       // Profile/Descriptions je Gerät
    catalog, _  := mapping.Load(mappingPath)              // optional, lenient

    ctx, cancel := signal.NotifyContext(ctx, SIGINT, SIGTERM); defer cancel()

    mqttClient := mqtt.NewClient(...)                     // synchroner First-Connect
    lc := mqtt.NewLifecycle(mqttClient, ...)
    if err := lc.Start(ctx); err != nil { return err }
    defer lc.Stop(stopCtx)                                // graceful, mit Timeout

    var disc *hass.Discovery
    if cfg.HASSEnable { disc = hass.New(...) }

    b := bridge.New(bridge.Deps{MQTT: mqttClient, Devices: devices, Catalog: catalog, HASS: disc, ...})
    return b.Run(ctx)                                     // blockiert bis Context-Done
}
```

- **Signal-Handling:** `signal.NotifyContext` → Context-Cancel bei Ctrl-C/SIGTERM.
- **errgroup** für parallele, gebundene Goroutinen (Bridge + optionaler Web-Server).
- **Graceful Shutdown:** MQTT-Disconnect mit kurzem Timeout; alle Device-Worker beenden;
  LWT auf „offline".
- **Synchroner First-Connect** (MQTT) → Startfehler sofort sichtbar; Geräte dürfen
  asynchron/offline sein (FK-1).

---

## 4. Konfiguration (`internal/config`)

Flaches `Config`-Struct mit `yaml`-Tags, 1:1 mit YAML; Auflösung **YAML → Env → Defaults →
Validate** (aggregierte `ValidationError`); `Locate()` in CWD/XDG/HOME. Env-Prefix z. B.
`HC2M_*`.

Vorschlag der wichtigsten Felder:

```yaml
# MQTT
MQTT_SERVER: "tcp://localhost:1883"
MQTT_LOGIN: ""
MQTT_PASSWORD: ""
MQTT_TOPIC: "homeconnect"          # Basis-Topic
MQTT_QOS: 1
MQTT_RETAIN: true

# Home Assistant
HASS_ENABLE: true
HASS_BASE_TOPIC: "homeassistant"
HASS_BIRTH_GRACETIME: 15

# Verbindung / Resilienz (siehe 05-resilienz.md)
APP_NAME: "go-homeconnect2mqtt"
APP_ID: ""                          # frei; leer → generieren
RECONNECT_INITIAL: 1                # s
RECONNECT_MAX: 30                   # s
RECONNECT_JITTER: 500               # ms
HANDSHAKE_TIMEOUT: 60               # s
SEND_TIMEOUT: 20                    # s
HEARTBEAT: 20                       # s
RESYNC_INTERVAL: 0                  # s; 0 = nur per NOTIFY (kein Polling nötig)

# Web-UI (optional, Parität zum Schwesterprojekt — siehe §10)
WEB_ENABLE: false
WEB_BIND: "127.0.0.1:8080"          # localhost-only per Default
WEB_USER: ""                         # Basic-Auth (beide leer = ohne Auth)
WEB_PASSWORD: ""

# Sonstiges
LANGUAGE: "de"                      # Anzeige-Namen (Werte/Entities bleiben technisch)
DEBUG: false
```

**Geräte** separat (analog `registers.yaml`), damit operator-patchbar:

```yaml
# devices.yaml
devices:
  - name: geschirrspueler
    host: 192.168.1.50               # IP oder mDNS-Name; leer → Default-Host aus Profil
    manual_host: true                # mDNS-Update unterdrücken
    connection_type: AES             # AES | TLS
    psk64: "<key>"                   # aus <serial>.json: "key"
    iv64: "<iv>"                     # aus <serial>.json: "iv" (nur AES)
    description: ./profiles/geschirrspueler.json   # geparste DeviceDescription (gecacht)
  - name: kochfeld
    ...
  - name: waschmaschine
    ...
```

> Alternativ direkt das Profil-ZIP referenzieren und beim Start parsen (`profile.archive`).
> Empfohlen: `hc-util` parst einmalig ZIP → Description-JSON + Geräteeintrag (Secrets bleiben
> im Operator-Bereich).

---

## 5. Bridge / Orchestrierung (`internal/bridge`)

Das Herzstück. Pro Gerät **ein isolierter Worker** (Goroutine + Sub-Context), gestartet via
`errgroup`/`x/sync`:

```
bridge.Run(ctx):
  für jedes Gerät: go deviceWorker(ctx, dev)
  warte auf ctx.Done

deviceWorker(ctx, dev):
  appliance = homeconnect.NewAppliance(dev.description, dev.host, psk, iv, ...)
  for ctx nicht abgebrochen:
     err = appliance.Connect(ctx)              # Handshake + Post-Init
     if err: publishAvailability(offline); backoff(); continue   # FK-1
     publishAvailability(online)
     publishDiscovery() (einmalig / bei HA-Birth)
     onNotify(uid, value):  publishState(dev, entity)            # NOTIFY /ro/values
     onConnectionState(s):  publishConnectionState(dev, s)
     subscribeCommands(dev): handleSet(...)                      # MQTT /set
     <-ctx.Done | <-disconnected
     publishAvailability(offline)
     # bei Disconnect: backoff + Reconnect (voller State-Reset, 01-protokoll §10)
```

**Eigenschaften:**
- **Geräte-Isolation:** ein abstürzendes/offline Gerät beeinflusst andere nicht.
- **Panic-Recovery** pro Worker (defensive Tiefe).
- **Publish what you can:** Teil-Updates publizieren; Fehler loggen, Loop weiter.
- **Health pro Gerät:** Connection-State + last-update-Alter über MQTT exponieren.

**Command-Handling (`command.go`):** MQTT `/set` → Feature über Topic-Pfad auflösen → Wert
normalisieren (Typ/Enum, Float→Int bei #68, case-insensitiv) → access/available/Schreibfenster
prüfen (FK-5) → gerätespezifischer Schreib-/Startpfad (FK-4) → bei Fehlercode loggen/retrien.

---

## 6. Resilienz-Integration (Kernanforderung)

Direkte Umsetzung der `05-resilienz.md`-Maßnahmen im Code:

| Fehlerklasse | Ort | Maßnahme |
|---|---|---|
| FK-1 Reconnect | `homeconnect/reconnect.go`, `bridge/device.go` | Backoff+Jitter, offline≠Fehler, Connect-Timeout, Log-Throttle, Worker-Isolation |
| FK-2 HMAC | `homeconnect/crypto.go`, `socket.go` | constant-time, Reconnect bei Failure, TX/RX-Mutex, CBC-Stream |
| FK-3 Parser | `profile/parser.go`, `entities.go` | force-list, skip+log, None-Guards, Regex `\d+` |
| FK-4 Programmstart | `bridge/command.go` | drei Startpfade + Hood-Fan-DELETE |
| FK-5 Schreibfenster | `bridge/command.go`, `entities.go` | descriptionChange auswerten, Write-Gating/Retry |
| FK-6 Typ/Enum | `homeconnect/entities.go`, `profile/types.go` | Float→Int, case-insensitiv, Enum-Miss→Rohwert |
| FK-7 Onboarding/IPv6 | `config`, `profile`, `hc-util` | kategorisierte Fehler, manuelle IP, IPv6-Klammern |
| FK-8 Abdeckung | `bridge/publish.go` | generisch alle Features exponieren |

---

## 7. Logging, Build, Qualität (vom Schwesterprojekt)

- **Logging:** `slog` (TextHandler, Info default, Debug bei `DEBUG=true`); strukturierte
  Keys (`device`, `resource`, `code`, `err`); **Redaction** für psk/iv/serialNumber/mac/shipSki/deviceID.
- **Makefile-Targets:** `setup`, `build`, `test` (`-race`), `check` (vet+fmt+lint+test),
  `fmt` (gofumpt+goimports), `lint` (golangci-lint), `docker`, `release` (cross-compile
  linux/amd64+arm64, darwin/arm64).
- **Dockerfile:** Multi-Stage, distroless `nonroot`, `CGO_ENABLED=0` (⚠️ Ausnahme falls
  TLS-PSK eine cgo-Lib braucht — dann separates Build-Target), `mapping.yaml`/Profile als
  Volume/Asset.
- **.golangci.yaml:** strenge Linter-Liste wie im Schwesterprojekt (errcheck, errorlint,
  gosec, govet, revive, sloglint, staticcheck, …).
- **CI:** lint + test-Matrix (Ubuntu/macOS/Windows) mit Race-Detector + Build-Sanity.
- **Git-Hooks:** Commits auf `main` blocken (Feature-Branch + PR).
- **Konventionen:** SPDX+Copyright-Header je Datei; Errors **wrappen** (`errors.Is`),
  nicht vergleichen; **lenient loading** (skip statt fatal); narrow Interfaces + Fakes in Tests;
  Tabellentests. Wo Python-Verhalten nachgebaut wird: Quelle im Kommentar zitieren
  (z. B. `// mirrors hc_socket.AesSocket._receive`).

---

## 8. Teststrategie

- **Krypto-Vektoren:** AES/HMAC/Padding gegen bekannte Ein-/Ausgaben testen (Round-Trip
  encrypt→decrypt; HMAC-Kette über mehrere Frames; Padding-Längen `""`→16, `"a"`→15,
  15→17, 16→16). Die Python-`tests/utils.py` liefert die gespiegelte Server-Seite als Referenz.
- **Parser:** gegen die Fixture-XMLs (in `07-referenz-quellen.md` eingebettet) + bewusst
  kaputte Varianten (Einzel- vs. Liste, fehlende Felder, uppercase-Enums, greedy Group-IDs).
- **Handshake/Session:** Stub-Socket, der die Server-Sequenz (`/ei/initialValues` → …) abspielt;
  msgID-Korrelation, Timeout, `code`-Fehler.
- **Reconnect:** simulierter Drop → Backoff, voller State-Reset, Resync.
- **Bridge/Command:** Stub-Session + Stub-MQTT; Tabellentests für Normalisierung,
  Schreibfenster-Gating, Startpfade.
- **Race-Detector** Pflicht (CBC/HMAC-State, Worker-Concurrency).

---

## 9. Roadmap (inkrementell, jede Stufe lauffähig)

1. **Krypto + Transport** (AES-Socket): KDF, CBC-Stream, HMAC-Kette, Padding, WS-Connect,
   Heartbeat. Unit-getestet gegen Vektoren. *(Risiko-ärmster Kern zuerst.)*
2. **Message + Session + Handshake**: encode/decode, `/ei/initialValues`→…→CONNECTED,
   sendSync-Korrelation, Post-Init (`allMandatoryValues`).
3. **Profil-Parser**: ZIP/JSON + XML → Description/Entities; Typ-Tabellen; toleranter Parser.
4. **Reconnect**: State-Machine, Backoff+Jitter, voller State-Reset, offline-Handling.
5. **Bridge + MQTT-Publish**: Entities → State-Topics, NOTIFY-Updates, availability/LWT,
   Connection-State. *(Erstes End-to-End: read-only Spiegelung.)*
6. **Command-Topics**: Schreiben mit Normalisierung, Schreibfenster, gerätespezifische Startpfade.
7. **HA-Discovery**: Plattform-Heuristik, Payloads, Birth/LWT.
8. **Anreicherung (`mapping.yaml`)**: device_class/Einheiten/Defaults; `hc-util` für Onboarding.
9. **TLS-PSK** (ältere Geräte) — falls benötigt; nach Klärung der Go-TLS-PSK-Option.
10. **Optional:** Web-UI + State-Cache (Parität zum Schwesterprojekt, siehe §10), mDNS-Discovery.

> Stufe 1–2 sind der höchste technische Risikopunkt (undokumentierte Krypto). Deshalb
> zuerst, mit harten Testvektoren, bevor Bridge/MQTT obenauf kommen.

---

## 10. Optionale Web-UI & State-Cache (Parität zum Schwesterprojekt)

`go-mtec2mqtt` bringt eine **optionale**, standardmäßig **abgeschaltete** Status-/Health-UI mit
(`internal/web/` + `internal/state/`). `go-homeconnect2mqtt` übernimmt dieses Muster 1:1 — als
*Zusatz*, nicht als Kern: Der Daemon bleibt ohne `WEB_ENABLE=true` eine reine MQTT-Bridge.

### 10.1 Zweck

Bei einer undokumentierten API ist **Beobachtbarkeit** besonders wertvoll. Die UI zeigt auf
einen Blick, was sonst nur im Log steht:
- Pro Gerät: Connection-State (connecting/handshake/connected/reconnecting/offline) + **Alter
  des letzten Updates** (erkennt sofort hängendes Polling / stille Desyncs, vgl. FK-1/FK-2).
- Aktuelle Feature-Werte je Gerät (Live, ohne MQTT-Client öffnen zu müssen).
- Optionaler Schreib-Dispatch (ein Feature direkt aus der UI setzen — nützlich zum Testen der
  gerätespezifischen Startpfade, FK-4).

### 10.2 `internal/state/` — In-Memory-Cache (nur aktiv bei `WEB_ENABLE`)

Spiegelt den `state.Store` des Schwesterprojekts:
- Thread-safe (`sync.RWMutex`); `UpdateDevice(device, values)` wird vom `bridge.publish`-Pfad
  parallel zum MQTT-Publish gefüttert (ein Eintrag pro Entity).
- `Snapshot()` liefert eine konsistente Kopie für die JSON-API.
- **`DeviceHealth`**: `UpdatedAt`, `AgeSeconds`, `ConnectionState`, `EntityCount` — die Basis
  der „stuck detection".
- **SSE-Subscriber**: je Browser ein gepufferter Channel (Kapazität 1) für Push-Updates.

> Wenn `WEB_ENABLE=false`, wird der Store **gar nicht** instanziiert — kein Overhead.

### 10.3 `internal/web/` — HTTP-Server + SPA

Spiegelt `internal/web/` des Schwesterprojekts:
- `web.go`: HTTP-Listener auf `WEB_BIND` (Default `127.0.0.1:8080` — localhost-only, nie
  versehentlich im Netz), optionale **HTTP-Basic-Auth** (`WEB_USER`/`WEB_PASSWORD`; beide leer
  ⇒ ohne Auth, z. B. hinter Reverse-Proxy).
- `api.go`: JSON-API — `GET /api/status`, `GET /api/health`, `GET /api/devices` (aktuelle
  Werte), `POST /api/devices/<device>/set` (Write-Dispatch in die Bridge).
- `backend.go`: **SSE** (`GET /api/events`) für Live-Push an den Browser.
- `static/`: kleine eingebettete SPA via **`go:embed`** (Ausnahme von der „nicht embedden"-
  Regel — UI-Assets sind nicht operator-patchbar gedacht) + **i18n** (de/en, gemäß `LANGUAGE`).

### 10.4 Lifecycle-Integration

Wie im Schwesterprojekt wird die UI bei `WEB_ENABLE=true` als zweite Goroutine über `errgroup`
neben der Bridge gestartet — ein Fehler/Cancel beendet beide sauber:

```go
if cfg.WebEnable {
    store := state.New()
    b := bridge.New(bridge.Deps{ ..., State: store })   // Bridge füttert den Store
    webSrv := web.New(cfg, store, b)                      // b für Write-Dispatch
    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return b.Run(gctx) })
    g.Go(func() error { return webSrv.Run(gctx) })
    return g.Wait()
}
return bridge.New(bridge.Deps{ ... }).Run(ctx)            // ohne UI: reine Bridge
```

### 10.5 Abgrenzung

- **Optional & opt-in** — Default aus; Kernbetrieb (MQTT/HA) braucht die UI nicht.
- Die UI ist **kein** Ersatz für Home Assistant; sie ist ein leichtgewichtiges Diagnose-/
  Health-Dashboard (Parität zum Schwesterprojekt), primär für Onboarding und Fehlersuche bei
  der undokumentierten Geräte-API.
- Umsetzung erst in **Roadmap-Stufe 10** — nach dem stabilen MQTT-Kern.

### 10.6 Schnittstellen-Vertrag

Der vollständige HTTP-API- und SSE-Vertrag (Endpunkt-Schemas, JSON-Payloads, Event-Format,
Fehler-Taxonomie, Routing-/Teststrategie) ist in **[`09-web-api.md`](09-web-api.md)** spezifiziert.
