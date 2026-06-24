# Home Connect — Wire Protocol Specification

> Complete, self-contained specification of the local Home Connect WebSocket protocol,
> reconstructed from `chris-mc1/homeconnect_websocket` (Python, v1.5.3) — files
> `hc_socket.py`, `session.py`, `message.py`, `const.py`, `helpers.py` and
> `doc/Home_Connect_Protocol.md`.
>
> This file is sufficient to reimplement the protocol core in Go. The reference repos
> are no longer required.

---

## 1. Transport & Mode Selection

Every device runs a WebSocket server on the LAN under the path **`/homeconnect`**.
There are two security modes; the choice is made solely based on the presence of an IV:

| Condition (from profile `.json`) | Mode | URL |
|---|---|---|
| `connectionType == "AES"`, `iv` set | **AES** (app-layer crypto) | `ws://<host>:80/homeconnect` |
| `connectionType == "TLS"`, no `iv` | **TLS-PSK** | `wss://<host>:443/homeconnect` |

Decision logic (Python `HCSessionBase.__init__`): `iv64` present → AES socket;
otherwise `psk64` present → TLS socket. (An unencrypted socket exists only for testing.)

**WebSocket details:**
- Endpoint path is **always** `/homeconnect`.
- **No** HTTP headers (no subprotocol, no Origin, no auth headers). Authentication
  runs exclusively over TLS-PSK or AES/HMAC.
- **Heartbeat:** ping every **20 s** (aiohttp `heartbeat=20`); missing pong ⇒ connection dead.
  Reimplement this in Go (ping ticker + pong deadline).

**IPv6:** the host is wrapped in square brackets when it contains `:`: `[2a0a:...]`.
⚠️ **Bug source (#409):** Python wraps blindly — already-bracketed hosts become `[[...]]`.
**Go rule:** strip existing brackets first, then wrap; handle link-local zone IDs (`%eth0`)
separately (yarl/getaddrinfo do not survive them).

---

## 2. Key Derivation

`psk64` and `iv64` are **URL-safe Base64 without padding**.

```
psk = base64url_decode(psk64)      # 32 bytes
iv  = base64url_decode(iv64)       # 16 bytes (AES only)
```

> Python decodes with `"==="` appended (excess padding is ignored).
> In Go: `base64.RawURLEncoding.DecodeString(...)`.

**AES mode — KDF (no HKDF! just one HMAC-SHA256 call each):**

```
enckey = HMAC_SHA256(key=psk, msg="ENC")    # 32 bytes → AES key
mackey = HMAC_SHA256(key=psk, msg="MAC")    # 32 bytes → HMAC key
```

- Labels are exactly ASCII bytes: `"ENC"` = `0x45 0x4E 0x43`, `"MAC"` = `0x4D 0x41 0x43`.
- No salt, no info string, no expand round.
- `iv` is **not** derived but used directly: (a) as the initial CBC IV,
  (b) as a constant prefix in **every** HMAC computation (see §3).

**TLS mode:** `psk` is passed directly as the PSK in the TLS handshake (no enckey/mackey/iv).

---

## 3. AES Mode: Message Crypto (the core — source of Bug #62)

### 3.1 Constants & Connection State

```
ENCRYPT_DIRECTION = 0x45  # 'E'  — messages SENT by the client
DECRYPT_DIRECTION = 0x43  # 'C'  — messages RECEIVED by the client
MINIMUM_MESSAGE_LENGTH = 32
```

Continuous state held per connection (reset on **every** `connect()`):

```
last_tx_hmac = 16 null bytes        # rolling HMAC of the send direction
last_rx_hmac = 16 null bytes        # rolling HMAC of the receive direction
aes_encrypt  = AES-CBC(enckey, iv)  # ONE persistent cipher object for ALL TX messages
aes_decrypt  = AES-CBC(enckey, iv)  # ONE persistent cipher object for ALL RX messages
```

> **Two independent, stateful crypto chains** (TX and RX). Both the **CBC chaining**
> (last ciphertext block = next IV) and the **HMAC chain** run over the entire
> connection. This is the root of #62 (see §3.4).

### 3.2 Sending (Encrypt-then-MAC)

```
1. clear = utf8(message)
2. Padding (NOT PKCS#7):
     pad_len = 16 - (len(clear) % 16)
     if pad_len == 1: pad_len += 16          # min. 2 pad bytes ⇒ for remainder 15, pad_len = 17
     padded = clear ‖ 0x00 ‖ random(pad_len-2) ‖ byte(pad_len)
   ⇒ first pad byte = 0x00, last byte = pad_len; result is always a multiple of 16.
3. ct = aes_encrypt.encrypt(padded)          # continuous CBC chain
4. mac_input = iv ‖ 0x45 ‖ last_tx_hmac ‖ ct
   last_tx_hmac = HMAC_SHA256(mackey, mac_input)[0:16]
5. Wire: ct ‖ last_tx_hmac                    # send_bytes
```

Padding examples (verified): `""`→16, `"a"`→15, 15 characters→17, 16 characters→16.

### 3.3 Receiving (MAC-then-Decrypt)

```
1. Frame must be BINARY, len >= 32, len % 16 == 0   (otherwise discard — see warning §3.4)
2. ct = buf[:-16]; recv_hmac = buf[-16:]
3. mac_input = iv ‖ 0x43 ‖ last_rx_hmac ‖ ct
   calc = HMAC_SHA256(mackey, mac_input)[0:16]
   if not constant_time_eq(recv_hmac, calc): -> "HMAC Failure" (AuthenticationError)
   last_rx_hmac = recv_hmac                  # advance the chain ONLY on success
4. plain = aes_decrypt.decrypt(ct)           # continuous CBC chain
5. pad_len = plain[-1]; if len(plain) < pad_len: "Padding Error"
   message = utf8_decode(plain[:-pad_len])
```

### 3.4 Bug #62 "HMAC Failure" — Cause & Go Obligations

Symptom (observed in the wild): **4124** `HMAC Failure` in a short time; the receive loop
breaks off. Open since March 2026, no fix in the repo.

**Cause:** `last_rx_hmac` and the CBC `aes_decrypt` state must run in lockstep with the
server stream. As soon as **one** incoming frame does not traverse the normal RX path
(loss, non-BINARY frame, length/padding error, unexpected server message), the chain is
**permanently** desynchronized → **every** subsequent frame fails. There is no resync
within the running stream. The Python lib erroneously keeps reading on the same socket →
endless flood.

**Obligations for the Go implementation:**
1. Always compare HMACs in **constant time** (`hmac.Equal`).
2. On the **first** HMAC failure (or padding/decode error): **immediately close the socket
   and fully reconnect** — never keep reading on a desynced chain.
3. **Strictly serialize** TX and RX crypto state each with its own mutex (two parallel
   `send()` calls would corrupt `last_tx_hmac`/CBC).
4. Run CBC as a true **stream** (carry the last ciphertext block as the next IV),
   not re-encrypting every message with the static `iv`.
5. Direction bytes from the **sender's** perspective: TX always `'E'` (0x45), RX always `'C'` (0x43).
   (The server signs its messages with `'C'`, so the client verifies with `'C'`.)

---

## 4. TLS-PSK Mode

```
psk = base64url_decode(psk64)
TLS client context:
  max_version   = TLS 1.2          # capped at 1.2
  ciphers       = "PSK"            # OpenSSL: all PSK suites (TLS_PSK_WITH_AES_*)
  check_hostname = false
  verify_mode    = CERT_NONE       # no server certificate validation
  psk_client_callback -> (identity=None/empty, psk=psk)
```

- Messages are exchanged as **plain UTF-8 text** over the TLS tunnel
  (no additional app-layer crypto — TLS protects everything).
- Auth errors are detectable as TLS handshake errors.

⚠️ **Go porting hurdle:** `crypto/tls` does **not** support TLS-PSK natively. Options:
- Use a PSK-capable TLS 1.2 library (e.g. a cgo OpenSSL binding), **or**
- prioritize the AES mode (many devices speak AES on port 80 anyway; AES is fully
  implementable in Go with the standard library).

> **Recommendation:** implement the AES mode first (covers newer devices), TLS-PSK as a
> second expansion stage. Which mode applies per device is stated in the profile `.json`
> (`connectionType`).

---

## 5. Message Format

JSON over WebSocket, compactly serialized (separators `,` and `:`):

```json
{"sID":<int>,"msgID":<int>,"resource":"<str>","version":<int>,"action":"GET|POST|RESPONSE|NOTIFY","data":[ ... ],"code":<int?>}
```

| Field | Type | Meaning |
|---|---|---|
| `sID` | int | Session ID (set by the device, from `/ei/initialValues`) |
| `msgID` | int | Message ID, monotonically increasing per sent message |
| `resource` | string | Endpoint, e.g. `/ci/services`, `/ro/values` |
| `version` | int | Service version (from `/ci/services`, default 1) |
| `action` | enum | `GET` / `POST` / `RESPONSE` / `NOTIFY` |
| `data` | array | Payload — **always a list when sending** (a single dict becomes `[dict]`) |
| `code` | int? | only in responses; **≠ null ⇒ error** (→ `CodeResponsError`) |

**Defensive parsing (mandatory):**
- Cast `sID`, `msgID`, `version` with `int(...)` when reading — devices sometimes send numbers as strings.
- Object fields may contain malformed JSON; the lib's workaround: replace `]"` → `]`,
  then parse again.

---

## 6. Handshake — Exact Sequence

### 6.1 Pre-Handshake

The client waits for the **first message sent by the device**. It must be
`resource == "/ei/initialValues"`. From it, take over:
- `sID = msg.sID`
- `last_msg_id = msg.data[0].edMsgID` (starting msgID for own messages)

Example frame:
```json
{"sID":<sid>,"msgID":<server_mid>,"resource":"/ei/initialValues","version":2,"action":"POST","data":[{"edMsgID":<client_start_mid>}]}
```

### 6.2 Handshake Steps

1. **RESPONSE to initialValues** (same resource/msgID/sID):
   ```json
   data: [{"deviceType": <2 if version==1, otherwise "Application">,
           "deviceName": "<app_name>", "deviceID": "<app_id>"}]
   ```
   (`app_name`/`app_id` freely choosable, e.g. `"go-homeconnect2mqtt"` / a random hex ID.)
2. **`GET /ci/services` (version 1)** → response: `[{"service":"ci","version":x}, ...]`
   → store in `service_versions`.
3. **If `ci` version < 3:**
   - **`GET /ci/authentication`** with `data:[{"nonce":"<token>"}]`,
     `token = base64url(random 32 bytes)` without `=` padding. The device replies with its own nonce.
   - **`GET /ci/info`** (tolerate/ignore error code) → HW info.
4. **If `iz` in services:** `GET /iz/info`.
5. **If `ei` version == 2:** `NOTIFY /ei/deviceReady` (fire-and-forget, no response).
6. **If `ni` in services:** `GET /ni/info`.
7. State → **CONNECTED**.

### 6.3 Post-Connect Init (immediately after CONNECTED)

1. **`GET /ro/allDescriptionChanges`** → update entities.
2. **`GET /ro/allMandatoryValues`** → update entities (all mandatory values).

⚠️ On some devices/firmwares both return `500 InternalServerError`
(local_hass #255/#128/#177). **Go:** handle tolerantly + retryable, no hard abort.

### 6.4 msgID/sID/version Defaults (before sending)

- `version`: if not set → `service_versions[resource[1:3]]` (the 2 characters after `/`,
  e.g. `/ci/...` → `"ci"`), default `1`.
- `sID`: if not set → current session ID.
- `msgID`: if not set → `last_msg_id`, then `last_msg_id += 1`.

### 6.5 Request/Response Correlation

`send_sync`: register one queue (capacity 1) per `msgID`; incoming `RESPONSE` messages are
matched via `msgID`. **Timeout 20 s.** `code != null` in the response ⇒ error
(`CodeResponsError`). Non-response messages (NOTIFY) go to the general message handler.

---

## 7. Reading / Setting Values / Programs (`/ro` service)

> The `/ro` section in `doc/Home_Connect_Protocol.md` is empty — the following semantics
> are reconstructed from `appliance.py` / `entities.py`.

**Reading:** no single read in normal operation. Values arrive via:
- Bulk at connect: `GET /ro/allDescriptionChanges` + `GET /ro/allMandatoryValues` (RESPONSE).
- Push: **`NOTIFY /ro/values`** and **`NOTIFY /ro/descriptionChange`**.

Each update item: `{"uid":<int>, "value":..., "access":..., "available":..., "min":..., "max":..., "stepSize":..., "execution":...}` (all except `uid` optional). Unknown `uid` → ignore (debug log only).

**Setting a value:**
```json
{"resource":"/ro/values","action":"POST","data":[{"uid":<uid>,"value":<typed value>}]}
```
On `RESPONSE` without `code`, set the local shadow value (`value_shadow`) optimistically;
the real value follows via `NOTIFY /ro/values`.

**Executing a command:** identical `POST /ro/values` with `{"uid":..,"value":<int>}`.

**Selecting / starting a program:**
```json
POST /ro/selectedProgram   data: {"program":<uid>, "options":[{"uid":..,"value":..}, ...]}
POST /ro/activeProgram     data: {"program":<uid>, "options":[ ... ]}
```
Missing readwrite options are filled in with their shadow value.

**Acknowledging events:** commands `BSH.Common.Command.AcknowledgeEvent` /
`BSH.Common.Command.RejectEvent` with the event UID.

> ⚠️ **Program start is device-specific** (see `05-resilience.md`): a blind
> `POST /ro/activeProgram` fails on some devices with 400/501/541. Cooktops require a
> direct `POST /ro/selectedProgram` (`validate=false`); dryers/washing machines need a
> READWRITE window on `BSH.Common.Root.ActiveProgram` (uid 256).

---

## 8. Service Catalog (from `doc/Home_Connect_Protocol.md`)

Services are reported with versions via `/ci/services`. Known endpoints:

### `ei` (External Interface)
- **`POST /ei/initialValues`** — first device message (contains `edMsgID`). Reply:
  `[{"deviceType":2|"Application","deviceName":..,"deviceID":..}]` (v1: `2`, v2: `"Application"`).
- **`NOTIFY /ei/deviceReady`** — client signals readiness (ei v2 only).

### `ci` (Command Interface)
- **`GET /ci/services`** — list of `{service, version}`. v1 always possible.
- **`GET /ci/authentication`** — 32-byte nonce (hex/base64), device replies with its own nonce.
- **`GET /ci/info`** — HW info:
  ```json
  {"deviceID":"SIEMENS-SN8S3647TE-68E05997A408","eNumber":"SN8S3647TE/33","brand":"SIEMENS",
   "vib":"SN8S3647TE","mac":"68-99-A4-0E-05-78","haVersion":"1.0","swVersion":"1.4.9",
   "hwVersion":"5056177560","deviceType":32,"deviceInfo":"DISHWASHER","customerIndex":33,
   "serialNumber":"017376983004000136","fdString":"8949","shipSki":"55DE...B953B1"}
  ```
- `GET /ci/tzinfo`, `GET /ci/networkdetails`, `GET /ci/wifiSetting`, `GET /ci/wifiNetworks`
- ci v3: `GET /ci/registeredDevices`, `GET /ci/pairableDevices`

### `iz` (Identification)
- **`GET /iz/info`** — HW info (similar to `/ci/info`, sometimes with `deviceType:"Dishwasher"`).

### `ni` (Network Interface)
- **`GET /ni/info`** — interface info (type, ssid, rssi, status, euiAddress, ipV4/ipV6).
- **`GET /ni/config`** — interface config (automaticIPv4/6, manualIPv4/6).

### `ro` (Remote Operation) — values & programs, see §7
- `GET /ro/allMandatoryValues`, `GET /ro/allDescriptionChanges`
- `POST /ro/values`, `NOTIFY /ro/values`, `NOTIFY /ro/descriptionChange`
- `POST /ro/selectedProgram`, `POST /ro/activeProgram`

---

## 9. Response/Error Codes (`const.py`, complete)

A `code` ≠ null in a RESPONSE ⇒ error. Complete table:

| Code | Meaning | Code | Meaning |
|---|---|---|---|
| 200 | OK | 524 | NotAvailable |
| 202 | Accepted | 525 | WriteRequest NotAvailable |
| 400 | BadRequest | 526 | ReadRequest NotAvailable |
| 403 | Forbidden | 527 | NotAvailableByList |
| 404 | NotFound | 528 | WriteRequest NotAvailableByList |
| 405 | MethodNotAllowed | 529 | ReadRequest NotAvailableByList |
| 413 | RequestEntityTooLong | 530 | NoExecution |
| 414 | RequestUriTooLong | 531 | ValueOutOfRange |
| 429 | TooManyRequests | 532 | InvalidUIDValue |
| 500 | InternalServerError | 533 | Incomplete |
| 501 | NotImplemented | 534 | Inconsistent |
| 502 | BadGateway | 535 | CmdViolation |
| 503 | ServiceUnavailable | 536 | InvalidFormat |
| 504 | GatewayTimeout | 537 | RemoteControlNotActive |
| 507 | InsufficientMemory | 538 | RemoteStartNotActive |
| 512 | UnknownUID | 539 | LockedByLocalControl |
| 513 | WriteRequest UnknownUID | 540 | DeviceStateNotCompliant |
| 514 | ReadRequest UnknownUID | 541 | ProcessStateNotCompliant |
| 515 | Busy | 542 | BackendNotConnected |
| 516 | WriteRequest Busy | 543 | EnergyManagementNotConnected |
| 517 | ReadRequest Busy | 544 | NotInLocalWiFi |
| 518 | NoAccess | 519 | WriteRequest NoAccess |
| 520 | ReadRequest NoAccess | 521 | NoAccessByList |
| 522 | WriteRequest NoAccessByList | 523 | ReadRequest NoAccessByList |

Practically relevant: **400** (wrong program start/float-instead-of-int), **500**
(`/ro/allMandatoryValues` handshake), **404** (handshake reconnect loop, #403),
**541** (writing outside the READWRITE window, #384).

---

## 10. Connection State Machine & Reconnect

```
CONNECTING → HANDSHAKE → CONNECTED
   (loss)  → RECONNECTING → (HANDSHAKE again) → CONNECTED
   (error) → ABNORMAL_CLOSURE
   (close) → CLOSING → CLOSED
```

**Python reconnect (`HCSessionReconnect`):** on a closed socket in the `finally` of the
receive loop → if `reconnect` active → state RECONNECTING + `_reconnect_loop`.
The loop attempts `socket.connect()` + pre-handshake + recv loop + handshake;
on `ConnectionFailedError` → `continue` **without backoff** (⚠️ Bug #41: thousands of
errors with permanently offline devices); on `HCHandshakeError` → CLOSING.

**State reset on every reconnect (must be reproduced correctly):**
1. **Crypto:** new socket ⇒ `last_rx_hmac`/`last_tx_hmac` = 16 null bytes, **new** CBC objects (chain starts at the `iv`).
2. **Session:** `sID` + `last_msg_id` fresh from a new `/ei/initialValues`.
3. **Services:** `service_versions` fresh from `/ci/services`.
4. **Entities:** after CONNECTED, `/ro/allMandatoryValues` again → resync all values.

**Go improvements over Python (core resilience requirement):**
- **Exponential backoff with jitter** (e.g. 1 s → 30 s, ±500 ms) instead of a tight loop.
- **Log rate limiting** for recurring errors (offline device = normal state, no spam).
- **Offline is not an error:** device sleeping/off ⇒ "unavailable" + LWT, no crash, no
  endless loop.
- On HMAC failure (#62): force reconnect instead of continuing to read.
- Constants are foreseen in Python but unused: `MAX_CONNECT_TIMEOUT=60`,
  `TIMEOUT_INCREASE_FACTOR=1.2`, `DEFAULT_HANDSHAKE_TIMEOUT=60`, `DEFAULT_SEND_TIMEOUT=20`.

---

## 11. Go Reimplementation — Compact Checklist

1. Mode: `iv64` → AES (ws:80), otherwise TLS-PSK (wss:443); path `/homeconnect`; heartbeat 20 s.
2. KDF: `enckey=HMAC-SHA256(psk,"ENC")`, `mackey=HMAC-SHA256(psk,"MAC")`; `iv` directly; Base64 RawURL.
3. AES-TX: custom padding → CBC stream encrypt → `HMAC(mackey, iv‖'E'‖last_tx_hmac‖ct)[:16]`; wire `ct‖mac`.
4. AES-RX: split `ct|mac` → HMAC `iv‖'C'‖last_rx_hmac‖ct` constant-time → CBC stream decrypt → unpad. Error ⇒ reconnect.
5. Serialize TX/RX state each with a mutex.
6. Handshake: `/ei/initialValues` → RESPONSE → `/ci/services` → (ci<3: `/ci/authentication`+`/ci/info`) → (iz: `/iz/info`) → (ei2: NOTIFY `/ei/deviceReady`) → (ni: `/ni/info`).
7. Post-connect: `/ro/allDescriptionChanges` + `/ro/allMandatoryValues` (tolerate 500).
8. msgID monotonic; correlate responses via msgID; timeout 20 s; `code!=null` = error.
9. Live updates: NOTIFY `/ro/values` + `/ro/descriptionChange`.
10. Writing: `POST /ro/values [{uid,value}]`; programs: `/ro/selectedProgram` / `/ro/activeProgram`.
11. Reconnect with backoff + jitter + log throttling; full state reset; offline ≠ error.
```
