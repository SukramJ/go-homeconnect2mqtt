# go-homeconnect2mqtt — Home Assistant Add-on

This add-on runs the [go-homeconnect2mqtt](https://github.com/SukramJ/go-homeconnect2mqtt)
daemon inside Home Assistant. It bridges local Home Connect appliances
(Bosch/Siemens/Gaggenau/Neff) to MQTT — over the appliance's local WebSocket
protocol, no cloud in normal operation — with Home Assistant MQTT discovery and
a read-only diagnostic web UI.

## Installation

1. In Home Assistant go to **Settings → Add-ons → Add-on Store**.
2. Click the **⋮** menu (top right) → **Repositories** and add:
   `https://github.com/SukramJ/go-homeconnect2mqtt`
3. The **go-homeconnect2mqtt** add-on now appears in the store. Open it and click
   **Install**.
4. Provide the appliance profile and configure devices (see below).
5. **Start** the add-on. Entities appear automatically via MQTT discovery
   (`hass_enable` is on by default).

## Getting the profile + keys

The encryption keys and device description come once from the **Home Connect
Profile Downloader** (target format *openHAB*) — see the main
[onboarding guide](https://github.com/SukramJ/go-homeconnect2mqtt/blob/main/docs/connecting-devices.md).
You get a ZIP per appliance.

Two ways to supply it to the add-on:

- **Recommended:** copy the ZIP to `/share` (e.g. `/share/homeconnect/profile.zip`)
  and set the `profile_zip` option. The add-on parses it into device
  descriptions on start; the parse log prints each appliance's `haId`.
- Or run `hc-util parse` yourself and place the resulting `<haId>.json`
  description files somewhere under `/share`, referencing them via each device's
  `description` option.

Either way, set the per-appliance `host` (its LAN IP — mDNS usually does not
work from inside the container) and paste the `psk64`/`iv64` keys (printed by
`hc-util parse`) into the `devices` list.

## Configuration example

```yaml
profile_zip: /share/homeconnect/profile.zip
devices:
  - name: dishwasher
    host: 192.168.1.50
    connection_type: AES
    psk64: "<key from hc-util parse>"
    iv64: "<iv from hc-util parse>"
    haid: "0102030405"          # resolves description to /data/profiles/0102030405.json
mqtt_server: ""                  # empty = use the Home Assistant MQTT broker
hass_enable: true
web_enable: true
```

Leave `mqtt_server` **empty** to auto-use the Home Assistant MQTT broker (like
zigbee2mqtt); set it only to target a different broker.

## Diagnostic web UI

After starting, open the add-on's **Web UI** (side-panel icon). It is served
through Home Assistant Ingress — no port needs to be exposed — and shows a live,
read-only snapshot of every appliance's connection state, last-update age and
feature values.

## Notes

- The pre-built add-on images (`ghcr.io/sukramj/go-homeconnect2mqtt-addon-{arch}`,
  built by `.github/workflows/addon-image.yml`) are CGo-free and support **AES**
  appliances. **TLS-PSK** (older appliances on `wss://…:443`) needs a separate
  cgo build and is not included in the default add-on image.
- To build locally from `addon/Dockerfile` instead of pulling the GHCR image,
  remove the `image:` key from `addon/config.yaml`.

See [DOCS.md](DOCS.md) for the full options reference.
