# Home Connect Local – Research & Findings

> Status: 2026-06-23. Basis for `go-homeconnect2mqtt`. Sources are the
> two repositories by `chris-mc1`:
> [`homeconnect_local_hass`](https://github.com/chris-mc1/homeconnect_local_hass)
> (Home Assistant integration) and
> [`homeconnect_websocket`](https://github.com/chris-mc1/homeconnect_websocket)
> (the underlying Python protocol library).

This document summarizes **how** local Home Connect access works and **how
reliable** it is in practice – as preliminary work for a dedicated Go
implementation (Home Connect ⇒ MQTT).

---

## 1. Overview

Home Connect devices (Bosch / Siemens / Gaggenau / Neff) can be controlled
**entirely locally** without any cloud. Each device runs a **WebSocket server**
on the LAN. The `homeconnect_local_hass` integration is merely the Home
Assistant binding; the actual protocol logic resides in the
`homeconnect_websocket` library.

Important: The data required for encryption and the device description comes
**once from the cloud** via the external **"Home Connect Profile Downloader"**
tool (choose `openHAB` as the target format). After that, operation runs purely
locally.

---

## 2. Architecture / Protocol

### 2.1 Transport: local WebSocket

Connection via IP/hostname (auto-discovery, otherwise manual IP entry). There
are **two security modes**, depending on the device:

| Mode        | Transport          | Encryption                                                       |
|-------------|--------------------|------------------------------------------------------------------|
| **TLS-PSK** (older devices) | `wss://<ip>:443` | TLS with Pre-Shared Key (`psk64`)                               |
| **AES** (newer devices)     | `ws://<ip>:80`   | Encryption at the **application layer**: AES-CBC with Key + IV (`iv64`), integrity via **HMAC** |

The WebSocket endpoint path is `/homeconnect`. In AES mode the transport itself
is unencrypted (`ws://`), but every message is encrypted/decrypted with AES keys
derived from the PSK and secured with a continuous HMAC.

### 2.2 Authentication: PSK from the device profile

The keys do **not come from the device**, but from the downloaded profile
archive. Per device it contains:

- **`<serialnumber>.json`** – metadata + **Encryption Key (PSK)** and, where
  applicable, IV
- **`<serialnumber>_DeviceDescription.xml`** – the device's capabilities:
  available UIDs, options, enums, min/max values
- **`<serialnumber>_FeatureMapping.xml`** – mapping of numeric UIDs to feature
  names such as `BSH.Common.Setting.PowerState`

### 2.3 Data model & messages

From the two XML files the library builds a `DeviceDescription` object.
Communication runs over a **hierarchical resource/feature model** in dot
notation (e.g. `BSH.Common.Setting.PowerState`). After a handshake (exchanging
initial values/session), states are synchronized and values are read/set. The
HA integration maps these features onto entities (sensors, switches,
selects, …).

### 2.4 Parallels to HomeMatic/godevccu

Structurally comparable to HomeMatic/CCU: just as `godevccu` describes devices
via embedded `*_DeviceDescription`/paramset JSONs, Home Connect describes its
devices via `*_DeviceDescription.xml` + `*_FeatureMapping.xml`.

---

## 3. Reliability & Known Issues

**Short verdict:** The basic construction is considered a "solid foundation",
but there are **recurring connection/stability problems** and **many
device-specific bugs**. The actual reliability depends heavily on the specific
model/firmware.

Numbers (as of 2026-06-23):
- `homeconnect_local_hass`: **86 open issues**, of which **41 labeled as bugs**
  (14 feature requests, the rest translations/questions).
- `homeconnect_websocket`: only **4 open issues** – noticeably quieter/better
  maintained.

### 3.1 Connection stability (the most important topic)

- **`local_hass` #403 – "Losing connection to devices; Loop Exceptions"**:
  Device runs stably for weeks and then drops to `unavailable`. In the log a
  reconnect loop with `WSServerHandshakeError: 404 … url='ws://…/homeconnect'`.
  Wi-Fi is verifiably fine → the problem lies in the WebSocket
  handshake/reconnect, not in the network. Open, without a fix.
- **`local_hass` #339** – very slow HA startup.
- **`local_hass` #410 / #409** – config flow aborts with "Unknown error", or
  IPv6 hosts are handled incorrectly (connection setup fails).

### 3.2 AES/HMAC layer (core library)

- **`homeconnect_websocket` #62 – "Receive loop Exception / HMAC Failure"**:
  **4124 occurrences** in a short time. The application-side AES encryption
  fails the HMAC check and tears down the receive loop. Precisely the HMAC
  integrity check from section 2.1 is a real source of errors here. Open since
  March 2026.
- **#70** – parser crashes on uppercase values (`SELECTANDSTART`) (partially
  addressed: commit "Normalize execution value to lowercase").
- **#68** – Bosch FridgeFreezer expects an integer payload even though it is
  described as a float → data type mismatch.

### 3.3 Device-specific bugs (the largest block)

Many issues are **model-dependent parser/feature errors**, because the
`DeviceDescription.xml` varies greatly per device:

- Crashes when setting up individual devices: #407 (Oven water tank), #395
  (Oven status regex too greedy), #385 (RemainingProgramTime), #368
  (group-ID format Siemens oven).
- Commands fail: #371 (CoffeeMaker Start → 400), #322 (Hood program selection
  → error), #386 (`fan.turn_off` → 500 on hoods), #400/#384 (missing start
  button / delayed start on the dryer).
- Does **not affect all users**: well-supported devices often run stably, while
  problems accumulate with "more exotic" models.

### 3.4 Maintenance status

- **~10-month silent phase** (last beta `1.0.5b10` in **Aug 2025**), which led
  to the meta-question **#390 "… is this abandoned?"**. As one contributor put
  it: *"The integration works well in general, but some appliances may have
  issues due to incomplete support … the foundation is solid."*
- **Activity returned in June 2026**: new beta `1.0.5b11` (2026-06-18), PR
  merges on 2026-06-15. However, there is a backlog of working PRs that users
  currently sometimes cherry-pick themselves (e.g. PR #332).
- The core library `homeconnect_websocket` is better maintained (most recently
  `1.5.3`, March 2026, only 4 open issues).

### 3.5 Bottom line

For common devices and a stable Wi-Fi network **usable and local without
cloud**, but **not at "set-and-forget" level**: expect occasional connection
drops/reconnect loops (#403, #62) and model-specific quirks.

---

## 4. Implications for `go-homeconnect2mqtt`

Concrete consequences from the research for a dedicated Go implementation:

1. **Profile import instead of live cloud access.** Read keys and the device
   description from the "Profile Downloader" archive (`.json` + two XML files).
   No OAuth/cloud flow needed during normal operation.
2. **Cover both security modes:**
   - TLS-PSK (`wss://<ip>:443`) – in Go via `crypto/tls` with PSK cipher suites
     (possibly a custom handshake, since PSK support in the standard library is
     limited).
   - AES-CBC + HMAC at the application layer over `ws://<ip>:80` – derive
     key/IV from the PSK, maintain an HMAC chain per direction.
3. **Robust reconnect behavior as a core requirement.** The most frequent
   real-world issues (#403, #62) are connection drops and HMAC desync.
   Therefore: clean reconnect with backoff, fully reset HMAC/session state on
   reconnection, handle 404 handshake cases specifically.
4. **Tolerant XML parser.** The `DeviceDescription.xml` varies greatly between
   models; many crashes arise from overly strict assumptions (regex, group-ID
   format, data types float vs. int, uppercase/lowercase of enum values). Parse
   defensively, skip unknowns instead of crashing.
5. **Correct IPv6/host handling from the start** (cf. #409): build URLs with
   proper bracket + zone-ID encoding.
6. **MQTT mapping:** The dot-notation feature model
   (`BSH.Common.Setting.PowerState`) maps naturally onto MQTT topics (e.g.
   `homeconnect/<device>/BSH/Common/Setting/PowerState`). Read = state topic,
   write = command topic.

---

## 5. Sources

- Integration: <https://github.com/chris-mc1/homeconnect_local_hass>
- Core library: <https://github.com/chris-mc1/homeconnect_websocket>
- Referenced issues: local_hass #403, #410, #409, #407, #400, #395, #390,
  #386, #385, #384, #371, #368, #339, #322; websocket #70, #68, #62
- Profile acquisition: "Home Connect Profile Downloader" (target format openHAB)
