# Connecting your appliances — onboarding guide

This guide walks through getting a Home Connect appliance talking to the
`go-homeconnect2mqtt` bridge, end to end. The cloud is only needed **once**
(to download the profile); everything afterwards is local.

> Background: the protocol/profile details are in [`03-profile-format.md`](03-profile-format.md)
> and the topic layout in [`04-device-mapping.md`](04-device-mapping.md). Failure
> modes referenced as FK-x are catalogued in [`05-resilience.md`](05-resilience.md).

## At a glance

```
Home Connect cloud ──(once)──▶ profile.zip ──hc-util parse──▶ description JSON + devices.yaml entry
                                                                      │
   appliance on your LAN  ◀── ws://host:80 (AES) / wss://host:443 (TLS) ──┘
                                                                      │
                                                          homeconnect2mqtt ──▶ MQTT broker ──▶ Home Assistant
```

## Prerequisites

- A **Home Connect account** with the appliance registered and connected (set
  it up once in the Home Connect app; afterwards the bridge runs cloud-free).
- The appliance and the host running the bridge on the **same LAN**.
- An **MQTT broker** (e.g. Mosquitto) reachable from the host.
- The `homeconnect2mqtt` and `hc-util` binaries (`make build`, the Docker image,
  or a release archive).

---

## Step 1 — Download the device profile

The encryption keys and the device description are **not** served by the
appliance — they come once from the cloud via the external
**[Home Connect Profile Downloader](https://github.com/bruestel/homeconnect-profile-downloader)**.

1. Run the downloader and sign in with your Home Connect account.
2. Choose the target format **openHAB**.
3. You get a **ZIP** that contains, per registered appliance, three files:
   `<serial>.json` (keys + metadata), `<serial>_DeviceDescription.xml` and
   `<serial>_FeatureMapping.xml`.

> The bridge never logs in to the cloud. It only reads this ZIP. Keep it
> private — it contains the appliance keys.

---

## Step 2 — Parse the profile

Turn the ZIP into cached device descriptions and a ready-to-edit device list:

```sh
hc-util parse profile.zip --out ./profiles
```

This writes `./profiles/<haId>.json` per appliance and prints a `devices.yaml`
snippet to stdout, for example:

```yaml
# Add these entries to devices.yaml (secrets included — keep local):
devices:
  - name: 0102030405
    host: ""            # 0102030405 (mDNS) or set a manual IP
    connection_type: AES
    psk64: "…"
    iv64: "…"
    description: profiles/0102030405.json
```

Inspect what a device exposes (sanity check, optional):

```sh
hc-util dump profiles/0102030405.json
```

---

## Step 3 — Determine the appliance host

Set the `host:` for each device. Order of preference:

1. **mDNS default host** (leave `host: ""` to use it):
   - AES appliances → the `haId` (e.g. `0102030405`).
   - TLS appliances → `<brand>-<type>-<haId>` (e.g. `BOSCH-Dishwasher-0102030405`).
2. **Manual IP** — if mDNS does not resolve on your network (common), find the
   appliance's IP (your router's DHCP lease table, or an mDNS browser for
   `_homeconnect._tcp`) and put it in `host:`, then set `manual_host: true` so a
   later mDNS lookup won't override it. IPv6 literals are fine (with or without
   brackets). This manual-IP fallback is the fix for the most common onboarding
   failure (FK-7).

---

## Step 4 — Write the configuration

Copy the templates and fill them in:

```sh
cp config-template.yaml  config.yaml
cp devices-template.yaml devices.yaml
$EDITOR config.yaml devices.yaml
```

**`config.yaml`** — at minimum the MQTT broker:

```yaml
MQTT_SERVER: "tcp://192.168.1.10:1883"
MQTT_LOGIN: ""          # set both if your broker needs auth
MQTT_PASSWORD: ""
MQTT_TOPIC: "homeconnect"
HASS_ENABLE: true       # publish Home Assistant discovery
WEB_ENABLE: false       # optional diagnostics UI (Step 7)
```

**`devices.yaml`** — one entry per appliance (paste the `hc-util parse` output and
fill `name`/`host`):

```yaml
devices:
  - name: dishwasher                 # logical name; used in MQTT topics
    host: 192.168.1.50               # IP or mDNS name; "" = default host from profile
    manual_host: true                # suppress mDNS host updates when you set a fixed IP
    connection_type: AES             # AES | TLS
    psk64: "<key from <serial>.json>"
    iv64: "<iv from <serial>.json>"  # AES only
    description: ./profiles/dishwasher.json
```

> **AES vs. TLS:** newer appliances use **AES** on `ws://host:80` (default, works
> with the standard build). Older appliances use **TLS-PSK** on `wss://host:443`,
> which needs the cgo build: `make build-tlspsk`. With the standard build a TLS
> device simply reports `offline` (it never blocks the other appliances).

---

## Step 5 — Test the connection

Before running the daemon, verify each appliance connects + completes the
handshake (20 s timeout):

```sh
hc-util connection-test devices.yaml
```

```
✓ dishwasher: connected
✗ hob: no host configured
    hint: verify the device is on and reachable; set a manual IP in host:
```

Fix any `✗` (wrong/missing host, asleep appliance, wrong keys) before moving on.

---

## Step 6 — Run the daemon

```sh
homeconnect2mqtt --config ./config.yaml --devices ./devices.yaml
```

Docker:

```sh
docker run --rm -v "$PWD:/config" -w /config go-homeconnect2mqtt \
  --config /config/config.yaml --devices /config/devices.yaml
```

What to expect:

- The **MQTT connection is synchronous** at startup — a broker problem fails
  fast and visibly.
- An **appliance that is off/asleep is not an error** (FK-1): it shows up as
  `offline` and the worker keeps retrying with backoff. The daemon never crashes
  because one device is unreachable.
- Each device runs in an **isolated worker** — one failing appliance never
  affects the others.

---

## Step 7 — Verify

**Over MQTT** (e.g. `mosquitto_sub -h <broker> -t 'homeconnect/#' -v`):

```
homeconnect/<device>/availability          online        # LWT-backed
homeconnect/<device>/connection_state      connected
homeconnect/<device>/BSH/Common/Status/OperationState/state   Run
homeconnect/status                         online         # daemon-level status
```

Every feature is exposed generically under
`homeconnect/<device>/<Feature/Path>/state` (dotted feature names map to slash
paths). Writable features additionally get a `…/set` command topic.

**In Home Assistant** (if `HASS_ENABLE: true`): the appliance and its
entities appear automatically via MQTT discovery — no manual YAML. Discovery is
re-published when Home Assistant restarts.

**Optional diagnostics UI** — set `WEB_ENABLE: true` (default bind
`127.0.0.1:8080`) and open it in a browser for live per-device connection state,
last-update age and feature values. It is off by default and never required.

---

## Sending commands

Publish to a feature's `…/set` topic; values may be enum names, numbers or
booleans (normalised automatically):

```sh
mosquitto_pub -h <broker> -t 'homeconnect/dishwasher/BSH/Common/Setting/PowerState/set' -m 'On'
```

Programs are controlled via the `…/Root/SelectedProgram/set` (select) and
`…/Root/ActiveProgram/set` (start; send `off` to stop) topics, using the program
feature name as the value. The bridge picks the device-appropriate start path
automatically (FK-4) and gates writes on the appliance's current write window
(FK-5).

---

## Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| `connection-test` → cannot connect / 404 loop | wrong/stale host, or appliance asleep | set a manual IP (`host:` + `manual_host: true`); wake the appliance (FK-1/FK-7) |
| stays `offline`, others fine | appliance off/standby | normal — it reconnects when it wakes; check power/Wi-Fi (FK-1) |
| `auth` / handshake fails immediately | wrong `psk64`/`iv64`, or a TLS device on the standard build | re-check keys from `<serial>.json`; for TLS build with `make build-tlspsk` (FK-2/FK-7) |
| TLS device logs "TLS-PSK requires the 'tlspsk' build" | standard (CGO-free) build | rebuild with `make build-tlspsk` |
| occasional reconnects / `HMAC` log then recovery | crypto chain desync | expected self-healing — the bridge fully reconnects (FK-2) |
| `500` during handshake | firmware quirk on bulk fetch | tolerated automatically; values resync on the next NOTIFY (FK-2) |
| a write is ignored / `541` in logs | feature's write window is currently closed | the bridge retries within the window; trigger writes while the appliance is ready (FK-5) |

## Security notes

- Secrets (`psk64`/`iv64`, serial number, MAC, deviceID) are **never** published
  to MQTT or logged in clear text. Keep `devices.yaml` and the profile ZIP local.
- The optional web UI binds to `127.0.0.1` by default; if you expose it, set
  `WEB_USER`/`WEB_PASSWORD` or put it behind a reverse proxy.
