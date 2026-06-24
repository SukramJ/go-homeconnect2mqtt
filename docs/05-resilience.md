# Home Connect — Resilience: Error Classes, Issues & Countermeasures

> The local Home Connect integration is **not an officially documented API**. Real-world
> practical problems stem from the GitHub issues of `homeconnect_local_hass` (86 open,
> 41 of them with a bug label) and `homeconnect_websocket` (8 issues). This document condenses the
> relevant issues into **error classes with concrete Go countermeasures** — resilience is
> the core requirement of `go-homeconnect2mqtt`.

As of: 2026-06-23. Maintenance status: the integration became active again in June 2026 after
~10 months of silence (beta `1.0.5b11`), but with a large backlog; many fixes exist only as
unmerged community PRs. The core library is better maintained (`1.5.3`, March 2026).

---

## 1. The 8 Error Classes (Priority for Go)

### FK-1 — Connection stability / Reconnect ★★★ (most common class)

**Symptoms:** A device runs stably for weeks, then drops to `unavailable`; a reconnect loop
with `WSServerHandshakeError: 404 ... ws://…/homeconnect` does not recover; sleeping devices
cause endless errors.

**Issues:** local_hass #403 (404 loop, open), #293 (off device → "Failed setup, will retry" loop),
#339 (off device blocks HA startup ~5 min), #287/#44/#42 (flapping/disconnect during sleep);
websocket #41 (permanently offline → 1000s of errors, **no backoff** in the code).

**Go countermeasures:**
- Reconnect with **exponential backoff + jitter** (e.g. 1 s → 30 s, ±500 ms). Python has none.
- **Offline = normal state**, not an error: device sleeping/off → MQTT `availability=offline` + LWT,
  keep polling with backoff, **never** crash the tool/worker.
- **Connect timeout** at startup (an off device must not block tool startup — see #339).
- **Log rate limiting** for recurring connect errors.
- Handle the 404 handshake specifically: full reconnect (fresh socket/state), not a retry on a dead socket.
- One isolated worker per device (a crashing device must not affect others).

### FK-2 — Crypto / HMAC desync ★★★

**Symptoms:** A flood of `HMAC Failure` (real-world: 4124 of them), receive loop tears down;
`Message not of Type binary`; handshake `500`/`404`.

**Issues:** websocket #62 (HMAC, open); local_hass #255/#128/#177 (`500 /ro/allMandatoryValues`),
#16/#158 (not binary / timeout), #297 (TLS auth failed).

**Go countermeasures (details in `01-protocol.md` §3.4):**
- Compare HMAC in constant time.
- On the **first** HMAC/padding/decode failure → **reconnect fully immediately**, do not keep reading.
- Serialize TX/RX crypto state each with a mutex; treat CBC as a true stream.
- Tolerate `500` in the handshake and make it retryable (no hard abort).

### FK-3 — Device-specific parser/feature bugs ★★★ (largest block)

**Symptoms:** Setup crash for individual devices; greedy regex; `None.enum`; `JSONDecodeError`
when parsing `initValue`; wrong group ID format.

**Issues:** local_hass #395/#385/#368/#277/#194/#145 (oven cavity regex `int('001.Remaining...')`),
#407 (`None.enum` oven WaterTank), #210/#292/#95/#38 (hob `JSONDecodeError` during setup).

**Go countermeasures:**
- **Per-entity isolation:** a broken feature → skip + log, **never** discard the entire device.
- Match group IDs strictly with `\d+`/`[^.]+`; ignore non-numeric ones.
- None/existence guards everywhere (enum, optional fields).
- Tolerant XML parser (see `02-data-model.md` §2).

### FK-4 — Program start is device-specific ★★★

**Symptoms:** A blind `POST /ro/activeProgram` fails with `400`/`501`/`541`.

**Issues:** local_hass #322 (hood/dishwasher/washer start `400`), #371 (coffee `400`),
#400 (dryer: no start entity), #201 (washer: no start since b10), #386 (hood fan off → `500`),
#385 comment (hob: direct `selectedProgram` write required).

**Go countermeasures — support three start paths:**
1. **Standard:** `POST /ro/activeProgram {program, options}`.
2. **Hob (cooktop):** direct `POST /ro/selectedProgram` (because `selectedProgram.validate=false`;
   the standard start crashes with `NoneType.start` when nothing has been preselected).
3. **Command-based:** `BSH.Common.Command.StartProgram` (if present).
4. Hood fan off: **DELETE** `/ro/activeProgram` instead of value 0.

### FK-5 — Dynamic `access` (write window) ★★

**Symptoms:** Writing → `541 ProcessStateNotCompliant` / `400`, because the field is currently read-only.

**Issue:** local_hass #384 (dryer): `BSH.Common.Root.ActiveProgram` (uid 256) switches
**~every 30 s** between READWRITE and READ. Writing is only accepted within the READWRITE window.

**Go countermeasures:**
- React to `NOTIFY /ro/descriptionChange` (process access updates).
- **Gate** writes: only send when `access ∈ {readwrite, writeonly}`; otherwise wait for the window
  / retry with backoff.
- Correct delayed-start message (example #384): `POST /ro/values [{551:delay},{256:programUID}]`.

### FK-6 — Data type / enum mismatch ★★

**Issues:** websocket #68 (float setting wants integer), #70 (`SELECTANDSTART` uppercase),
#56 (enum value outside the enumeration), #66 (unknown program UID).

**Go countermeasures (see `02-data-model.md` §7):**
- Write a float with an integer value (stepSize==1) as an **integer**.
- All string enums **case-insensitive**.
- Enum miss → raw value; unknown UID → None/ignore.

### FK-7 — Config flow / IPv6 / onboarding ★

**Issues:** local_hass #410 (library exceptions not caught → "Unknown error"),
#409 (IPv6 double brackets + zone ID), #268 (all already setup), #297 (no IP fallback on TLS).

**Go countermeasures (see `03-profile-format.md`):**
- Dedicated, categorized error types + **always** a manual-IP escalation.
- IPv6: strip existing brackets, then wrap; handle zone IDs separately.

### FK-8 — Localization / option coverage (low severity)

Program names fall back to raw IDs (#298/#283/#15); many options are missing in the
HA integration (EcoDry, CrystalDry, HalfLoad, iDos, Spin/Temp …). For a **generic**
MQTT tool this is less critical, since ideally **all** features are exposed (see
`04-device-mapping.md`).

---

## 2. Device-specific Issues — Your Three Devices

### Dishwasher

| Issue | Problem | Consequence for the Go tool |
|---|---|---|
| #322 | Siemens: program start `400 /ro/activeProgram` (intermittent) | start paths FK-4; retryable |
| #373 | Bosch SMV4ECX30E: delayed start only a read-only sensor | offer `StartInRelative` as a controllable number |
| #322 comment | `select…start_in` → `TypeError: NoneType not subscriptable` | `value["start"]` None-safe |
| #297 | Bosch SMV6ZCX01G (TLS): "Authentication failed", no IP fallback | FK-7; TLS-PSK + IP fallback |
| #351/#263/#205/#49 | EcoDry/ExtraDry/CrystalDry/HalfLoad options missing | expose all options generically |
| #44/#42 | flapping when off / start button stuck after reconnect | FK-1 |
| — | Delayed start: dishwasher uses `BSH.Common.Option.StartInRelative` | (in contrast to the washer, see below) |

### Induction Hob/Cooktop — most fragile setup type

| Issue | Problem | Consequence |
|---|---|---|
| #292/#204/#210/#95/#38 | setup crash `JSONDecodeError` when parsing `initValue` | FK-3: per-entity isolation, tolerant parser |
| #128 | Bosch PIF695HC1E: `500 /ro/allMandatoryValues` | FK-2: tolerate handshake 500 |
| #385 comment (Bosch PXX645HC1M) | zone status missing; `residuelheat` (typo) as state; `Access.NONE` commands; **start = direct `selectedProgram` POST** (`validate=false`) | FK-4 path 2; tolerate raw values |
| #111/#261 | program start requires on-device confirmation | treat as expected behavior |

> Hob zones are detected dynamically: `Cooking.Hob.Status.Zone.<n>.{State,OperationState,PowerLevel,FryingSensorLevel,CurrentTemperature,HeatupProgress,Duration,...}` — see `04-device-mapping.md`.

### Washing Machine / Washer-Dryer (Washer/WasherDryer/Dryer)

| Issue | Problem | Consequence |
|---|---|---|
| #201 | Bosch Series 8 WD: no program start since b10 (start unavailable, resume "not writeable") | FK-4 / FK-5 |
| #179 | Bosch WDU28513: spin speed/temperature not settable, temp number missing | make `LaundryCare.Washer.Option.{SpinSpeed,Temperature}` writable |
| #196 | Series 8 WD: delayed start "'Start in' not available" — **washer uses `BSH.Common.Option.FinishInRelative` (uid 551)**, not `StartInRelative` | correct field per device type |
| #384 | Siemens dryer: `FinishInRelative` → `541`; **uid 256 dynamic access (~30 s)** | FK-5: write-window gating |
| #400 | Bosch dryer WRB247D0FG: start button missing (no ActiveProgram entity) | derive start paths robustly |
| #255 | Siemens washer: `500 /ro/allMandatoryValues` on re-setup | FK-2 |
| #293/#403 | off state / 404 loop | FK-1 |
| #224 | Bosch dryer: duration sensor min/max wrong | handle values tolerantly |

**Delayed-start rule of thumb:** dishwasher → `StartInRelative`; washing machine/dryer →
`FinishInRelative` (uid 551, often read-only/`access=none` until armed). Support both, None-safe.

---

## 3. Resilience Design Principles (Summary)

1. **Isolation per device** — own worker/goroutine + context; one device must never take down others.
2. **Isolation per entity** — defensive parsing, skip+log instead of crash.
3. **Offline is normal** — backoff + jitter, LWT/availability, no spam, no crash.
4. **Crypto desync = full reconnect** — never keep reading on a desynced chain.
5. **Writing is conditional** — check access/available/write window, device-specific start paths.
6. **Read tolerantly, write strictly** — accept raw values/unknown UIDs; normalize types/enums when writing.
7. **Observability** — structured logs (with redaction), per-device health (last-update age),
   expose connection state over MQTT.
8. **Never secrets** in logs/topics (psk, iv, serialNumber, mac, shipSki, deviceID).
