# Home Connect — Profile Format & Onboarding

> How the device is onboarded: the key and device description come from the cloud **once**
> via the "Home Connect Profile Downloader". After that, everything runs locally.
> Reconstructed from `config_flow.py`, `tests/const.py`, and the READMEs of both repos.

---

## 1. Obtaining the Profile

Tool: **[bruestel/homeconnect-profile-downloader](https://github.com/bruestel/homeconnect-profile-downloader)**.

1. Sign in with your Home Connect account (devices must be registered/connected there).
2. Select **target format "openHAB"**.
3. A **ZIP** is downloaded that contains three files per registered device.

> The key exchange with the cloud happens exactly once during the download. In normal operation,
> **no** OAuth/cloud access is required — `go-homeconnect2mqtt` only reads the ZIP/JSON.

---

## 2. ZIP Contents per Device

| File | Contents |
|---|---|
| `<serial>.json` | **Index + keys**: encryption key (PSK), optional IV, `connectionType`, device metadata, paths to the XML files |
| `<serial>_DeviceDescription.xml` | Structure/types/access (see `02-data-model.md`) |
| `<serial>_FeatureMapping.xml` | Plain names/enum names (see `02-data-model.md`) |

⚠️ Do **not** rely on the naming convention — the `.json` references the XML files
explicitly via fields (`deviceDescriptionFileName` / `featureMappingFileName`). This is also how
the HA integration loads them: find all `*.json` in the ZIP and read the XML paths from them.

---

## 3. `<serial>.json` — Exact Fields

Confirmed from `config_flow.py` (`process_zip_file`, `_set_encryption_keys`) and `tests/const.py`:

| JSON key | Meaning | Usage |
|---|---|---|
| `haId` | unique device ID, e.g. `010203040506070809` | unique_id; default host in AES mode |
| `deviceDescriptionFileName` | path of the DeviceDescription.xml in the ZIP | load XML |
| `featureMappingFileName` | path of the FeatureMapping.xml in the ZIP | load XML |
| `connectionType` | `"TLS"` or `"AES"` | mode selection |
| `key` | PSK (base64url) — the `psk64` **for both modes** | crypto |
| `iv` | IV (base64url) — **AES only** | crypto (`iv64`) |
| `brand` | e.g. `BOSCH`, `SIEMENS` | display; default host (TLS) |
| `type` | e.g. `Dishwasher`, `Hob`, `Washer` | display; default host (TLS) |
| `vib` | sales/model abbreviation | display/DeviceInfo |
| `model` | model designation | display |

### Key/Host Assignment

```
psk64 = key
if connectionType == "AES":
    iv64        = iv
    default_host = haId
else:  # TLS
    iv64        = (not set)
    default_host = f"{brand}-{type}-{haId}"     # e.g. BOSCH-Dishwasher-0102...
```

The default host is an mDNS-resolvable name; if resolution fails, it must be possible to enter a
**manual IP** (see §5).

---

## 4. Discovery (mDNS / zeroconf)

The HA integration uses **zeroconf exclusively**: service `_homeconnect._tcp.local.`.
No DHCP, no SSDP.

TXT properties used:
- `id` → `haId` (= unique_id)
- `vib`, `brand`, `type` → display
- Host = resolved IP address from the discovery record

**Go recommendation:** offer mDNS discovery optionally (convenience), but **always** allow manual
host/IP configuration. For an already-configured device, update the IP via mDNS —
**unless** a "manual host" was set (then keep it fixed). Persist a `manual_host` flag.

---

## 5. Onboarding Flow (for `go-homeconnect2mqtt`)

1. Read the ZIP (or individual `.json` + XMLs) → per device: PSK/IV/connectionType/host/description.
2. Optional: mDNS discovery to resolve the IP.
3. Connection test (real connect + handshake, timeout ~20 s).
4. If the connect fails → prompt for / configure a **manual IP** (mandatory escalation, cf. #410/#297).
5. Persist the device configuration (PSK/IV are needed at runtime).

> **Recommendation for the MQTT tool:** parse the profile **once** and cache the parsed
> description as JSON (as the library suggests). Store the device configuration (host, PSK, IV,
> path to the description) in `config.yaml` or a device-specific file. See `06-architecture.md`.

---

## 6. Sensitive Fields — Logging/Diagnostics

The HA integration redacts the following fields in diagnostics — **never log these in plaintext
or publish them over MQTT:**

```
psk / key, aes_iv / iv, deviceID, serialNumber, shipSki, mac
```

**Go rule:** structured logging with redaction for these keys; no secrets in MQTT topics, at most
`haId`/`serialNumber` as an (optionally masked) device ID.

---

## 7. Onboarding Error Classes (from en.json / config_flow)

| Key | Meaning |
|---|---|
| `cannot_connect` | connection/handshake failed |
| `auth_failed` | PSK/TLS authentication failed |
| `invalid_profile_file` | ZIP/JSON not readable |
| `profile_file_parser_error` | XML parsing failed |
| `appliance_not_in_profile_file` | selected device not in the profile |
| `all_setup` | all devices already set up |

⚠️ **Lesson from #410:** the library throws **its own** exceptions (`ConnectionFailedError`,
`HCHandshakeError`) — **not** aiohttp errors. Anyone who only catches aiohttp errors gets
"Unknown error" instead of the host fallback. **Go:** clean, categorized error types +
always keep the "manual IP" escalation reachable.
