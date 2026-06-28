# go-homeconnect2mqtt add-on

## Quickstart

For a standard Home Assistant install with the Mosquitto broker:

1. Download your appliance profile(s) with the **Home Connect Profile Downloader**
   (target format *openHAB*) — one ZIP per appliance — and copy them into
   **`/share/homeconnect/`** (the add-on creates that folder on first start).
2. Add one entry per appliance under **`devices`**. With the ZIPs in place you
   only need three fields:
   - `name` — a logical name (used in MQTT topics).
   - `host` — the appliance's **LAN IP** (mDNS usually does not work from inside
     the container, so set an explicit IP).
   - `haid` — the appliance haId (in the ZIP's `<serial>.json` and the parse
     log). It auto-fills `connection_type`, `psk64`/`iv64` and the description
     from the matching ZIP. You can still set any of those explicitly to override.
3. Leave **`mqtt_server` empty** — the add-on auto-connects to the Home
   Assistant MQTT broker, and `hass_enable` is on by default, so entities appear
   automatically via MQTT discovery.
4. **Start** the add-on, then open its **Web UI** (side-panel icon) for the live
   diagnostic snapshot.

## Options reference

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `profile_zip` | str | `""` | Optional explicit path to ONE profile ZIP. If empty, **every `*.zip` in `/share/homeconnect`** is parsed on start. Either way you get `/data/profiles/<haId>.json` descriptions plus a keys inventory used to auto-fill `devices`. |
| `devices` | list | `[]` | One entry per appliance (see below). |
| `mqtt_server` | str | `""` | MQTT broker host or full URL. **Leave empty** to auto-use the Home Assistant MQTT broker. |
| `mqtt_port` | int | `1883` | MQTT broker port (only used when `mqtt_server` is a bare host). |
| `mqtt_login` | str | `""` | MQTT username (only when `mqtt_server` is set). |
| `mqtt_password` | password | `""` | MQTT password (only when `mqtt_server` is set). |
| `mqtt_topic` | str | `homeconnect` | Base MQTT topic for published appliance state. |
| `hass_enable` | bool | `true` | Publish Home Assistant MQTT discovery so entities appear automatically. |
| `hass_discovery` | list(full\|curated) | `curated` | `curated` (default) publishes only the primary set, aligned with the entities the official Home Connect integration creates (~60 instead of ~590 across three appliances); `full` exposes every feature (the long tail disabled-by-default + categorized as diagnostic/config). |
| `hass_discovery_refresh` | bool | `false` | One-shot migration. On start the add-on clears all its retained discovery configs and re-creates the entities, so Home Assistant picks up changes it caches at first registration (entity **category**, name). Set it `true`, restart, then set it back to `false`. Resets per-entity room/custom-name; entity ids and automations are preserved. |
| `language` | list(en\|de) | `en` | Friendly-name language. Entity **names** are localized; entity **ids** stay English and language-independent. |
| `web_enable` | bool | `true` | Enable the read-only diagnostic web UI (served via Ingress). |
| `debug` | bool | `false` | Verbose logging. |

### `devices` entry

| Field | Type | Description |
| --- | --- | --- |
| `name` | str | Logical device name; used in MQTT topics. **Required.** |
| `host` | str | Appliance LAN IP (or hostname). **Required** (not in the profile). |
| `haid` | str? | Appliance haId. With the ZIP in `/share/homeconnect` this alone auto-fills `connection_type`, `psk64`/`iv64` and `description`. |
| `connection_type` | list(AES\|TLS)? | Optional; auto-filled from the ZIP via `haid`. `AES` (newer) or `TLS` (older). |
| `psk64` | password? | Optional; auto-filled from the ZIP via `haid`. The pre-shared key. |
| `iv64` | password? | Optional; AES only; auto-filled from the ZIP. |
| `description` | str? | Optional; defaults to `/data/profiles/<haid>.json`. |

## Topics

State is published under `<mqtt_topic>/<device>/<Feature/Path>/state`; writable
features listen on `…/set`; availability/connection are at
`<mqtt_topic>/<device>/availability` and `…/connection_state`. Home Assistant
discovery configs are published under `homeassistant/<platform>/<unique_id>/config`.

## Notes

- On first start the add-on creates **`/share/homeconnect/`** — copy your profile
  ZIP(s) here. On start every `*.zip` is parsed into descriptions plus a keys
  inventory (`/data/profiles/inventory.json`, 0600, never logged), which auto-fills
  each `devices` entry from its `haid`. Profiles and keys are operator-specific,
  stay on your Home Assistant host, and are never part of the generic image.
- The add-on image (**amd64-only**) is built with cgo + OpenSSL, so it supports
  both **AES** (`ws://host:80`) and **TLS-PSK** (`wss://host:443`, older
  appliances) out of the box.
- `host` should be an explicit LAN IP: multicast mDNS typically does not reach
  the add-on container, so the profile's mDNS default host won't resolve.
- An appliance that is off/asleep shows as `offline` and reconnects
  automatically — it never blocks the other appliances.
