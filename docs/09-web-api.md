# Web-UI — HTTP-API & SSE-Vertrag (optional)

> Konkreter Schnittstellen-Vertrag für die **optionale** Status-/Health-UI aus
> `06-architektur-konzept.md` §10 (`internal/web/` + `internal/state/`). Aktiv nur bei
> `WEB_ENABLE=true`. Spiegelt das `api.go`/`backend.go`-Muster des Schwesterprojekts,
> angepasst an das Home-Connect-Datenmodell (`02-datenmodell.md`, `04-geraete-mapping.md`).
>
> Dieser Vertrag ist die Implementierungs- und Testgrundlage für `web/api.go`,
> `web/backend.go` und die eingebettete SPA.

---

## 1. Konventionen

- **Base-Path:** `/api`. Die SPA wird unter `/` ausgeliefert (eingebettet via `go:embed`).
- **Bind:** `WEB_BIND` (Default `127.0.0.1:8080`, localhost-only).
- **Auth:** HTTP-Basic, wenn `WEB_USER` **und** `WEB_PASSWORD` gesetzt sind; sonst offen
  (z. B. hinter Reverse-Proxy / im vertrauten LAN). Gilt für **alle** `/api/*`-Routen inkl. SSE.
- **Content-Type:** Requests/Responses `application/json; charset=utf-8`; SSE
  `text/event-stream`.
- **Encoding:** UTF-8. Zeitstempel **RFC 3339 / ISO 8601 UTC** (`2026-06-23T20:15:04Z`).
- **Read-only by default:** Es gibt **einen** schreibenden Endpunkt (`POST …/set`). Alle anderen
  sind `GET`.
- **CORS:** standardmäßig keine CORS-Header (same-origin-SPA). Optional konfigurierbar.
- **Sprache:** Anzeige-/Label-Sprache folgt `LANGUAGE` (de/en); technische Feldnamen/Werte
  bleiben sprachunabhängig.
- **Secrets:** Niemals `psk`/`iv`/`serialNumber`/`mac`/`shipSki` ausliefern (Redaction, vgl.
  `03-profil-format.md` §6). `haId` ist erlaubt.

---

## 2. Gemeinsame Objekte

### 2.1 `Feature`

Die kanonische Repräsentation eines Geräte-Features — identisch in REST-Antworten und in
SSE-`value`-Events.

```json
{
  "feature": "BSH.Common.Status.OperationState",
  "topic": "homeconnect/geschirrspueler/BSH/Common/Status/OperationState",
  "uid": 4133,
  "value": "Run",
  "value_raw": 19,
  "protocol_type": "Integer",
  "content_type": "enumeration",
  "access": "read",
  "available": true,
  "writable": false,
  "unit": null,
  "device_class": "enum",
  "options": ["Inactive", "Ready", "DelayedStart", "Run", "Pause", "Finished", "Error"],
  "min": null,
  "max": null,
  "step": null,
  "updated_at": "2026-06-23T20:15:04Z"
}
```

| Feld | Typ | Quelle / Bedeutung |
|---|---|---|
| `feature` | string | Feature-Name (Punktnotation, `02-datenmodell` §8) |
| `topic` | string | zugehöriges MQTT-State-Topic (`04-geraete-mapping` §6.1) |
| `uid` | int | numerische UID |
| `value` | any\|null | aufgelöster Wert (Enum→Name, sonst Rohwert) |
| `value_raw` | any\|null | Rohwert vor Enum-Auflösung |
| `protocol_type` | string | `Boolean`/`Integer`/`Float`/`String`/`Object` (`02` §4.1) |
| `content_type` | string | feiner Typ (`enumeration`/`temperatureCelsius`/… `02` §4.2) |
| `access` | string | `read`/`readwrite`/`writeonly`/`none`/`readstatic` |
| `available` | bool | Gerät meldet Feature aktuell verfügbar |
| `writable` | bool | abgeleitet: `access ∈ {readwrite,writeonly}` **und** `available` |
| `unit` | string\|null | Einheit (aus `mapping.yaml`, falls vorhanden) |
| `device_class` | string\|null | HA-device_class-Hinweis (aus `mapping.yaml`) |
| `options` | string[]\|null | Enum-Werte (nur bei Enum) |
| `min`/`max`/`step` | number\|null | nur bei numerischen Settings |
| `updated_at` | string | Zeitpunkt des letzten Werts |

### 2.2 `DeviceSummary`

```json
{
  "name": "geschirrspueler",
  "haId": "BOSCH-Dishwasher-0123456789",
  "brand": "BOSCH",
  "type": "Dishwasher",
  "vib": "SMV6ZCX01G",
  "connection_state": "connected",
  "available": true,
  "updated_at": "2026-06-23T20:15:04Z",
  "age_seconds": 3,
  "feature_count": 142
}
```

`connection_state` ∈ `connecting | handshake | connected | reconnecting | closing | closed |
offline` (= `01-protokoll.md` §10 State-Machine; `offline` = Gerät nicht erreichbar).

### 2.3 `Error`

```json
{ "error": "not_writable", "message": "feature access is 'read'", "code": 403, "device_code": null }
```

| Feld | Bedeutung |
|---|---|
| `error` | maschinenlesbarer Schlüssel (Tabelle §5) |
| `message` | menschenlesbarer Text |
| `code` | HTTP-Status (gespiegelt) |
| `device_code` | optional: vom Gerät gemeldeter `code` (z. B. 541), siehe `01-protokoll` §9 |

---

## 3. REST-Endpunkte

### `GET /api/status`
Gesamtzustand des Daemons.
```json
{
  "version": "0.1.0",
  "commit": "abc1234",
  "build_date": "2026-06-23T18:00:00Z",
  "started_at": "2026-06-23T20:10:00Z",
  "uptime_seconds": 304,
  "mqtt": { "connected": true, "broker": "tcp://localhost:1883", "last_connected_at": "2026-06-23T20:10:01Z" },
  "devices": [ /* DeviceSummary[] */ ]
}
```

### `GET /api/health`
Kompakt, für Monitoring/Probes. **HTTP 200** wenn `status == "ok"`, sonst **503**
(degraded) — so taugt der Endpunkt direkt als Readiness-Probe.
```json
{
  "status": "ok",
  "mqtt": { "connected": true },
  "devices": [
    { "name": "geschirrspueler", "connection_state": "connected", "age_seconds": 3, "stale": false },
    { "name": "kochfeld",        "connection_state": "offline",   "age_seconds": 920, "stale": true }
  ]
}
```
`status` = `ok`, wenn MQTT verbunden **und** kein Gerät `stale`; sonst `degraded`.
`stale` = `age_seconds > STALE_THRESHOLD` (Vorschlag: max(2× erwartetes Update-Intervall, 120 s);
Geräte im Zustand `offline`/`reconnecting` zählen als `stale`). Erkennt stille Desyncs (FK-2).

### `GET /api/devices`
Liste aller Geräte: `{ "devices": [ /* DeviceSummary[] */ ] }`.

### `GET /api/devices/{device}`
Ein Gerät inkl. aller Features. `{device}` = `name` (bevorzugt) oder `haId`.
```json
{
  "device": { /* DeviceSummary */ },
  "info": { "brand": "BOSCH", "type": "Dishwasher", "vib": "SMV6ZCX01G", "swVersion": "..." },
  "features": [ /* Feature[], sortiert nach feature */ ]
}
```
**404** `device_not_found`, wenn unbekannt.

### `GET /api/devices/{device}/features/{feature}`
Einzelnes Feature (`{feature}` = voller Punktnotations-Name, URL-enkodiert). Antwort: ein
`Feature`-Objekt. **404** `device_not_found` / `feature_not_found`.

### `POST /api/devices/{device}/set`
Schreib-Dispatch in die Bridge (derselbe Pfad wie ein MQTT-`/set`, inkl. Normalisierung,
Schreibfenster-Gating und gerätespezifischer Startpfade — `04-geraete-mapping` §6.4, FK-4/FK-5/FK-6).

Request:
```json
{ "feature": "BSH.Common.Setting.PowerState", "value": "On" }
```
- `value` darf String/Zahl/Bool sein; Enums als **Name** (`"On"`) oder Rohwert.
- Programme/Commands: `value` ist der Programm-/Command-Name oder es wird ein optionales
  `options`-Objekt mitgegeben (`{ "feature": "...Program.Eco50", "action": "start", "options": {...} }`).

Antwort **202 Accepted** (Befehl abgeschickt; bestätigter Zustand folgt per SSE):
```json
{ "accepted": true, "device": "geschirrspueler", "feature": "BSH.Common.Setting.PowerState", "value": "On" }
```
Fehler: siehe §5 (z. B. **403** `not_writable`, **409** `write_window_closed`, **422**
`value_out_of_range`, **502** `device_error` mit `device_code`).

### `GET /api/version`
`{ "version": "...", "commit": "...", "build_date": "..." }` (für die SPA-Anzeige).

---

## 4. Server-Sent Events — `GET /api/events`

Live-Push an den Browser. Query: optional `?device=<name>` (nur Events dieses Geräts).

**Verhalten:**
- Beim Connect sendet der Server zuerst ein `snapshot`-Event mit dem aktuellen Gesamtzustand
  (so muss der Client nicht separat `GET /api/status` aufrufen).
- Danach inkrementelle Events bei Änderungen.
- **Heartbeat:** alle ~20 s eine SSE-Kommentarzeile (`:\n\n`), damit Proxies die Verbindung
  nicht kappen.
- `retry: 5000` wird einmalig gesendet (Client-Reconnect-Hinweis).
- Optionale monotone `id:` je Event (für `Last-Event-ID`-Resume; MVP darf darauf verzichten).

**Event-Typen** (`event:`-Feld + JSON in `data:`):

`snapshot` (einmalig beim Connect):
```
event: snapshot
data: {"devices":[ /* DeviceSummary[] */ ],"mqtt":{"connected":true}}
```

`value` (Feature-Wert geändert — gespeist aus dem `bridge.publish`-Pfad, parallel zu MQTT):
```
event: value
data: {"device":"geschirrspueler","feature":"BSH.Common.Option.RemainingProgramTime","value":3600,"value_raw":3600,"updated_at":"2026-06-23T20:15:07Z"}
```

`connection` (Verbindungszustand eines Geräts geändert):
```
event: connection
data: {"device":"kochfeld","connection_state":"reconnecting","available":false,"updated_at":"2026-06-23T20:15:09Z"}
```

`health` (periodisch, z. B. alle 10 s, oder bei `stale`-Wechsel):
```
event: health
data: {"status":"degraded","devices":[{"name":"kochfeld","connection_state":"offline","age_seconds":920,"stale":true}]}
```

> Die SSE-Quelle ist der `state.Store` (`06` §10.2): jeder Subscriber bekommt einen gepufferten
> Channel (Kapazität 1); bei vollem Puffer wird der älteste Wert verworfen („latest wins"),
> damit ein langsamer Browser den Publish-Pfad nie blockiert.

---

## 5. Fehler-Taxonomie (Schlüssel → HTTP)

| `error` | HTTP | Auslöser |
|---|---|---|
| `unauthorized` | 401 | Basic-Auth fehlt/falsch |
| `bad_request` | 400 | ungültiger JSON-Body / fehlende Felder |
| `device_not_found` | 404 | unbekanntes Gerät |
| `feature_not_found` | 404 | unbekanntes Feature |
| `not_writable` | 403 | `access ∉ {readwrite,writeonly}` |
| `not_available` | 409 | Feature aktuell `available=false` |
| `write_window_closed` | 409 | dynamischer Access gerade read-only (FK-5, uid 256) |
| `value_out_of_range` | 422 | Wert verletzt min/max/step oder Enum |
| `value_type_error` | 422 | Wert nicht in protocolType konvertierbar |
| `device_offline` | 503 | Gerät nicht verbunden |
| `device_error` | 502 | Gerät antwortete mit Fehler-`code` → `device_code` gesetzt |
| `internal` | 500 | unerwarteter Serverfehler |

Mapping Geräte-`code` → `device_error` (502) mit `device_code` (z. B. 400/501/541, `01` §9).
`541 ProcessStateNotCompliant` kann zusätzlich als `write_window_closed` (409) klassifiziert
werden, wenn ein dynamisches Access-Fenster erkannt wurde.

---

## 6. Beispiel-Implementierung (Routing-Skizze)

```go
// web/web.go — Routing (net/http, std-lib genügt)
mux := http.NewServeMux()
mux.HandleFunc("GET /api/status",                       s.handleStatus)
mux.HandleFunc("GET /api/health",                       s.handleHealth)
mux.HandleFunc("GET /api/devices",                      s.handleDevices)
mux.HandleFunc("GET /api/devices/{device}",             s.handleDevice)
mux.HandleFunc("GET /api/devices/{device}/features/{feature...}", s.handleFeature)
mux.HandleFunc("POST /api/devices/{device}/set",        s.handleSet)
mux.HandleFunc("GET /api/events",                       s.handleSSE)
mux.HandleFunc("GET /api/version",                      s.handleVersion)
mux.Handle("/", s.spa)                                  // go:embed SPA, SPA-Fallback auf index.html
handler := s.withBasicAuth(s.withJSON(mux))            // Auth + Content-Type-Middleware
```

`handleSet` ruft denselben Bridge-Pfad wie ein MQTT-`/set` (`bridge.Dispatch(device, feature,
value)`), damit REST- und MQTT-Schreibwege identisch normalisieren/gaten. `handleSSE` abonniert
den `state.Store`, schreibt `snapshot` + Stream, `flush()` nach jedem Event, Heartbeat-Ticker,
beendet sauber bei `r.Context().Done()`.

---

## 7. Teststrategie (UI)

- **API:** `httptest.Server` + Stub-`state.Store`/Stub-Bridge; Tabellentests je Endpunkt
  (200/4xx/5xx, JSON-Schema, Redaction von Secrets).
- **Auth:** mit/ohne `WEB_USER`/`WEB_PASSWORD`.
- **SSE:** Client liest Stream, prüft `snapshot` zuerst, dann `value`/`connection`/`health`;
  Heartbeat vorhanden; sauberer Abbruch bei Context-Cancel; langsamer Subscriber verwirft statt
  zu blockieren.
- **Write-Dispatch:** `POST …/set` mappt Geräte-Fehlercodes korrekt auf die Taxonomie (§5).
- **Health-Probe:** 200 bei `ok`, 503 bei `degraded`.
