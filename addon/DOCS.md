# go-homeconnect2mqtt add-on

## Quickstart

For a standard Home Assistant install with the Mosquitto broker:

1. Download your appliance profile with the **Home Connect Profile Downloader**
   (target format *openHAB*) and copy the ZIP to `/share`
   (e.g. `/share/homeconnect/profile.zip`). Set **`profile_zip`** to that path.
2. Add one entry per appliance under **`devices`**:
   - `name` — a logical name (used in MQTT topics).
   - `host` — the appliance's **LAN IP** (mDNS usually does not work from inside
     the container, so set an explicit IP).
   - `connection_type` — `AES` (newer appliances) or `TLS` (older; see Notes).
   - `psk64` / `iv64` — the keys printed by `hc-util parse` (the parse log runs
     on start; you can read the keys from the downloader's `<serial>.json` too).
   - `haid` — the appliance haId, so the description resolves to
     `/data/profiles/<haid>.json` (printed in the parse log). Alternatively set
     `description` to an explicit path.
3. Leave **`mqtt_server` empty** — the add-on auto-connects to the Home
   Assistant MQTT broker, and `hass_enable` is on by default, so entities appear
   automatically via MQTT discovery.
4. **Start** the add-on, then open its **Web UI** (side-panel icon) for the live
   diagnostic snapshot.

## Options reference

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `profile_zip` | str | `""` | Path to the Home Connect profile ZIP (e.g. `/share/homeconnect/profile.zip`). Parsed on start into `/data/profiles/<haId>.json`. Optional if you supply explicit `description` paths. |
| `devices` | list | `[]` | One entry per appliance (see below). |
| `mqtt_server` | str | `""` | MQTT broker host or full URL. **Leave empty** to auto-use the Home Assistant MQTT broker. |
| `mqtt_port` | int | `1883` | MQTT broker port (only used when `mqtt_server` is a bare host). |
| `mqtt_login` | str | `""` | MQTT username (only when `mqtt_server` is set). |
| `mqtt_password` | password | `""` | MQTT password (only when `mqtt_server` is set). |
| `mqtt_topic` | str | `homeconnect` | Base MQTT topic for published appliance state. |
| `hass_enable` | bool | `true` | Publish Home Assistant MQTT discovery so entities appear automatically. |
| `language` | list(en\|de) | `en` | Display-name language (topics/entity_ids stay language-independent). |
| `web_enable` | bool | `true` | Enable the read-only diagnostic web UI (served via Ingress). |
| `debug` | bool | `false` | Verbose logging. |

### `devices` entry

| Field | Type | Description |
| --- | --- | --- |
| `name` | str | Logical device name; used in MQTT topics. |
| `host` | str | Appliance LAN IP or hostname. |
| `connection_type` | list(AES\|TLS) | `AES` for newer appliances, `TLS` for older (see Notes). |
| `psk64` | password | Pre-shared key (from `hc-util parse` / `<serial>.json`). |
| `iv64` | password | Init vector (AES only). |
| `description` | str? | Explicit path to a parsed description JSON. Omit to derive from `haid`. |
| `haid` | str? | Appliance haId; resolves the description to `/data/profiles/<haid>.json`. |

## Topics

State is published under `<mqtt_topic>/<device>/<Feature/Path>/state`; writable
features listen on `…/set`; availability/connection are at
`<mqtt_topic>/<device>/availability` and `…/connection_state`. Home Assistant
discovery configs are published under `homeassistant/<platform>/<unique_id>/config`.

## Notes

- The add-on image is CGo-free and supports **AES** appliances (`ws://host:80`).
  **TLS-PSK** appliances (`wss://host:443`) require a separate cgo build and are
  not supported by the default add-on image.
- `host` should be an explicit LAN IP: multicast mDNS typically does not reach
  the add-on container, so the profile's mDNS default host won't resolve.
- An appliance that is off/asleep shows as `offline` and reconnects
  automatically — it never blocks the other appliances.
