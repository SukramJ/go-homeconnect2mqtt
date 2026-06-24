# Web UI — HTTP API & SSE Contract (optional)

> Concrete interface contract for the **optional** status/health UI from
> `06-architecture.md` §10 (`internal/web/` + `internal/state/`). Active only when
> `WEB_ENABLE=true`. Mirrors the `api.go`/`backend.go` pattern, adapted to the
> Home Connect data model (`02-data-model.md`, `04-device-mapping.md`).
>
> This contract is the implementation and test basis for `web/api.go`,
> `web/backend.go`, and the embedded SPA.

---

## 1. Conventions

- **Base path:** `/api`. The SPA is served under `/` (embedded via `go:embed`).
- **Bind:** `WEB_BIND` (default `127.0.0.1:8080`, localhost-only).
- **Auth:** HTTP Basic when `WEB_USER` **and** `WEB_PASSWORD` are set; otherwise open
  (e.g. behind a reverse proxy / in a trusted LAN). Applies to **all** `/api/*` routes including SSE.
- **Content-Type:** Requests/responses `application/json; charset=utf-8`; SSE
  `text/event-stream`.
- **Encoding:** UTF-8. Timestamps **RFC 3339 / ISO 8601 UTC** (`2026-06-23T20:15:04Z`).
- **Read-only by default:** There is **one** writing endpoint (`POST …/set`). All others
  are `GET`.
- **CORS:** no CORS headers by default (same-origin SPA). Optionally configurable.
- **Language:** Display/label language follows `LANGUAGE` (de/en); technical field names/values
  remain language-independent.
- **Secrets:** Never expose `psk`/`iv`/`serialNumber`/`mac`/`shipSki` (redaction, cf.
  `03-profile-format.md` §6). `haId` is permitted.

---

## 2. Common Objects

### 2.1 `Feature`

The canonical representation of a device feature — identical in REST responses and in
SSE `value` events.

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

| Field | Type | Source / Meaning |
|---|---|---|
| `feature` | string | feature name (dot notation, `02-data-model` §8) |
| `topic` | string | associated MQTT state topic (`04-device-mapping` §6.1) |
| `uid` | int | numeric UID |
| `value` | any\|null | resolved value (enum→name, otherwise raw value) |
| `value_raw` | any\|null | raw value before enum resolution |
| `protocol_type` | string | `Boolean`/`Integer`/`Float`/`String`/`Object` (`02` §4.1) |
| `content_type` | string | finer type (`enumeration`/`temperatureCelsius`/… `02` §4.2) |
| `access` | string | `read`/`readwrite`/`writeonly`/`none`/`readstatic` |
| `available` | bool | device currently reports the feature as available |
| `writable` | bool | derived: `access ∈ {readwrite,writeonly}` **and** `available` |
| `unit` | string\|null | unit (from `mapping.yaml`, if present) |
| `device_class` | string\|null | HA device_class hint (from `mapping.yaml`) |
| `options` | string[]\|null | enum values (only for enums) |
| `min`/`max`/`step` | number\|null | only for numeric settings |
| `updated_at` | string | timestamp of the last value |

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
offline` (= `01-protocol.md` §10 state machine; `offline` = device unreachable).

### 2.3 `Error`

```json
{ "error": "not_writable", "message": "feature access is 'read'", "code": 403, "device_code": null }
```

| Field | Meaning |
|---|---|
| `error` | machine-readable key (table §5) |
| `message` | human-readable text |
| `code` | HTTP status (mirrored) |
| `device_code` | optional: `code` reported by the device (e.g. 541), see `01-protocol` §9 |

---

## 3. REST Endpoints

### `GET /api/status`
Overall daemon state.
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
Compact, for monitoring/probes. **HTTP 200** when `status == "ok"`, otherwise **503**
(degraded) — so the endpoint works directly as a readiness probe.
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
`status` = `ok` when MQTT is connected **and** no device is `stale`; otherwise `degraded`.
`stale` = `age_seconds > STALE_THRESHOLD` (suggestion: max(2× expected update interval, 120 s);
devices in state `offline`/`reconnecting` count as `stale`). Detects silent desyncs (FK-2).

### `GET /api/devices`
List of all devices: `{ "devices": [ /* DeviceSummary[] */ ] }`.

### `GET /api/devices/{device}`
A single device including all features. `{device}` = `name` (preferred) or `haId`.
```json
{
  "device": { /* DeviceSummary */ },
  "info": { "brand": "BOSCH", "type": "Dishwasher", "vib": "SMV6ZCX01G", "swVersion": "..." },
  "features": [ /* Feature[], sorted by feature */ ]
}
```
**404** `device_not_found` if unknown.

### `GET /api/devices/{device}/features/{feature}`
A single feature (`{feature}` = full dot-notation name, URL-encoded). Response: a
`Feature` object. **404** `device_not_found` / `feature_not_found`.

### `POST /api/devices/{device}/set`
Write dispatch into the bridge (the same path as an MQTT `/set`, including normalization,
write-window gating, and device-specific start paths — `04-device-mapping` §6.4, FK-4/FK-5/FK-6).

Request:
```json
{ "feature": "BSH.Common.Setting.PowerState", "value": "On" }
```
- `value` may be a string/number/bool; enums as a **name** (`"On"`) or as a raw value.
- Programs/commands: `value` is the program/command name, or an optional
  `options` object is supplied (`{ "feature": "...Program.Eco50", "action": "start", "options": {...} }`).

Response **202 Accepted** (command dispatched; the confirmed state follows via SSE):
```json
{ "accepted": true, "device": "geschirrspueler", "feature": "BSH.Common.Setting.PowerState", "value": "On" }
```
Errors: see §5 (e.g. **403** `not_writable`, **409** `write_window_closed`, **422**
`value_out_of_range`, **502** `device_error` with `device_code`).

### `GET /api/version`
`{ "version": "...", "commit": "...", "build_date": "..." }` (for the SPA display).

---

## 4. Server-Sent Events — `GET /api/events`

Live push to the browser. Query: optional `?device=<name>` (only events for this device).

**Behavior:**
- On connect, the server first sends a `snapshot` event with the current overall state
  (so the client does not have to call `GET /api/status` separately).
- After that, incremental events on changes.
- **Heartbeat:** every ~20 s an SSE comment line (`:\n\n`), so proxies do not drop the
  connection.
- `retry: 5000` is sent once (client reconnect hint).
- Optional monotonic `id:` per event (for `Last-Event-ID` resume; the MVP may omit this).

**Event types** (`event:` field + JSON in `data:`):

`snapshot` (once on connect):
```
event: snapshot
data: {"devices":[ /* DeviceSummary[] */ ],"mqtt":{"connected":true}}
```

`value` (feature value changed — fed from the `bridge.publish` path, in parallel with MQTT):
```
event: value
data: {"device":"geschirrspueler","feature":"BSH.Common.Option.RemainingProgramTime","value":3600,"value_raw":3600,"updated_at":"2026-06-23T20:15:07Z"}
```

`connection` (connection state of a device changed):
```
event: connection
data: {"device":"kochfeld","connection_state":"reconnecting","available":false,"updated_at":"2026-06-23T20:15:09Z"}
```

`health` (periodic, e.g. every 10 s, or on a `stale` transition):
```
event: health
data: {"status":"degraded","devices":[{"name":"kochfeld","connection_state":"offline","age_seconds":920,"stale":true}]}
```

> The SSE source is the `state.Store` (`06` §10.2): each subscriber gets a buffered
> channel (capacity 1); on a full buffer the oldest value is discarded ("latest wins"),
> so a slow browser never blocks the publish path.

---

## 5. Error Taxonomy (key → HTTP)

| `error` | HTTP | Trigger |
|---|---|---|
| `unauthorized` | 401 | Basic auth missing/wrong |
| `bad_request` | 400 | invalid JSON body / missing fields |
| `device_not_found` | 404 | unknown device |
| `feature_not_found` | 404 | unknown feature |
| `not_writable` | 403 | `access ∉ {readwrite,writeonly}` |
| `not_available` | 409 | feature currently `available=false` |
| `write_window_closed` | 409 | dynamic access currently read-only (FK-5, uid 256) |
| `value_out_of_range` | 422 | value violates min/max/step or enum |
| `value_type_error` | 422 | value not convertible to protocolType |
| `device_offline` | 503 | device not connected |
| `device_error` | 502 | device responded with an error `code` → `device_code` set |
| `internal` | 500 | unexpected server error |

Mapping device `code` → `device_error` (502) with `device_code` (e.g. 400/501/541, `01` §9).
`541 ProcessStateNotCompliant` may additionally be classified as `write_window_closed` (409)
when a dynamic access window has been detected.

---

## 6. Example Implementation (routing sketch)

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

`handleSet` calls the same bridge path as an MQTT `/set` (`bridge.Dispatch(device, feature,
value)`), so that REST and MQTT write paths normalize/gate identically. `handleSSE` subscribes
to the `state.Store`, writes `snapshot` + stream, `flush()` after each event, a heartbeat ticker,
and terminates cleanly on `r.Context().Done()`.

---

## 7. Test Strategy (UI)

- **API:** `httptest.Server` + stub `state.Store`/stub bridge; table tests per endpoint
  (200/4xx/5xx, JSON schema, redaction of secrets).
- **Auth:** with/without `WEB_USER`/`WEB_PASSWORD`.
- **SSE:** client reads the stream, checks `snapshot` first, then `value`/`connection`/`health`;
  heartbeat present; clean abort on context cancel; slow subscriber discards instead of
  blocking.
- **Write dispatch:** `POST …/set` maps device error codes correctly onto the taxonomy (§5).
- **Health probe:** 200 on `ok`, 503 on `degraded`.
