# go-homeconnect2mqtt

[![Open your Home Assistant instance and add this add-on repository.](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FSukramJ%2Fgo-homeconnect2mqtt)

A local, cloud-free bridge from **Home Connect** appliances (Bosch / Siemens /
Gaggenau / Neff) to **MQTT**, with optional **Home Assistant** discovery.

Every Home Connect appliance runs a local WebSocket server on the LAN. This
daemon connects to it directly — no Home Connect cloud, no OAuth in normal
operation — mirrors the appliance state to MQTT and applies write commands.
Encryption keys and the device description come once from the *Home Connect
Profile Downloader* (openHAB target format); after that everything is local.

> Status: under active initial development. See
> [`docs/09-implementation-plan.md`](docs/09-implementation-plan.md) for the
> phased plan and progress tracker.

## Highlights

- **Local only.** AES app-layer crypto on `ws://host:80` (newer appliances) or
  TLS-PSK on `wss://host:443` (older appliances).
- **Resilient by design.** Exponential backoff + jitter reconnect, full crypto
  state reset on HMAC desync, per-device and per-entity isolation, "offline is
  not an error". See [`docs/05-resilience.md`](docs/05-resilience.md).
- **Generic MQTT mapping.** Every device feature is exposed, not a curated
  allowlist; an optional `mapping.yaml` enriches features with device classes,
  units and device-specific program-start paths.
- **Optional Home Assistant discovery** and an optional read-only status/health
  web UI (off by default).

## Quick start

```sh
# 1. Download your appliance profile with the Home Connect Profile Downloader
#    (target format: openHAB) — you get a ZIP per registered appliance.

# 2. Parse it into cached device descriptions + a device inventory entry.
hc-util parse profile.zip --out ./profiles

# 3. Configure the daemon.
cp config-template.yaml config.yaml
cp devices-template.yaml devices.yaml   # fill in host/keys/description paths
$EDITOR config.yaml devices.yaml

# 4. Run.
homeconnect2mqtt --config ./config.yaml --devices ./devices.yaml
```

For the full step-by-step walkthrough (finding the host/IP, connection test,
verification and troubleshooting) see the onboarding guide:
[`docs/connecting-devices.md`](docs/connecting-devices.md).

## Home Assistant add-on

This repository is also a Home Assistant add-on repository. In Home Assistant go
to **Settings → Add-ons → Add-on Store → ⋮ → Repositories** and add
`https://github.com/SukramJ/go-homeconnect2mqtt`, then install the
**go-homeconnect2mqtt** add-on. It auto-connects to the Home Assistant MQTT
broker, publishes discovery, and surfaces the diagnostic web UI via Ingress.
See [`addon/README.md`](addon/README.md) and [`addon/DOCS.md`](addon/DOCS.md).

Your appliance profiles and keys are **operator-specific and never baked into
the image** (the published image is generic: binaries + `mapping.yaml` only). On
first start the add-on creates a **`/share/homeconnect/`** drop folder — copy
your profile ZIP (or pre-parsed `<haId>.json` files) there and point the
`profile_zip`/`description` options at it. Keys (`psk64`/`iv64`) are supplied via
the add-on options and stay on your Home Assistant host.

## MQTT topics

```
<topic>/<device>/<Feature/Path>/state    # e.g. .../BSH/Common/Setting/PowerState/state
<topic>/<device>/<Feature/Path>/set      # writable features only
<topic>/<device>/availability            # online / offline (LWT)
<topic>/<device>/connection_state        # connecting / handshake / connected / reconnecting / ...
```

Feature names use dotted notation (`BSH.Common.Status.OperationState`) mapped to
slash-separated MQTT paths.

### Home Assistant discovery

Discovery is published as one retained **device document** per appliance:

```
<hass_base_topic>/device/<appliance slug>/config    # e.g. homeassistant/device/geschirrspuler/config
```

The last segment before `config` is the *slug* of the appliance name in
`devices.yaml`: lower case, with everything outside `a-z0-9` collapsed to a
single `_`, and German umlauts transliterated — `Geschirrspüler` becomes
`geschirrspuler`. The daemon logs the exact topic as
`hass.bundle_published topic=…` on every publish; copy it from there rather
than assembling it by hand.

Earlier releases published one retained config per entity, under
`<hass_base_topic>/<platform>/<appliance slug>/<feature>/config`. Those are
retracted automatically on the first start of this release, before the
document goes out, because Home Assistant refuses either form while the other
is still retained.

**Rolling back to a pre-device-document release** therefore needs one manual
step, once per appliance: the retained document has to be cleared, or the old
release's per-entity configs are refused and you get no entities.

```sh
mosquitto_pub -h <broker> -u <user> -P <password>   -t 'homeassistant/device/geschirrspuler/config' -r -n
```

`-u`/`-P` are required on an authenticated broker, which is what the Home
Assistant add-on always uses.

**A document does not delete a component by leaving it out.** Narrowing what
is published — excluding a feature in `mapping.yaml`, or switching
`HASS_DISCOVERY` from `full` to `curated` (510 of 687 components per
appliance) — removes nothing in Home Assistant: those entities keep their
retained registry entry, keep *both* availability sources this daemon
publishes, and keep receiving live state, because `HASS_DISCOVERY` is applied
where discovery is rendered and never on the state topics. The daemon warns
once per appliance on start (`hass.curated_components_omitted`, with the
count). Removing them is manual: restart Home Assistant (or reload the MQTT
integration) first — the delete option appears only once an entity is no
longer offered — then delete them from the device page.

## Building

```sh
make build       # bin/homeconnect2mqtt + bin/hc-util
make test        # race-enabled test suite
make check       # the full PR gate (vet + fmt + lint + test)
make docker      # distroless container image
```

## Documentation

The `docs/` directory is a self-contained knowledge base: wire protocol,
data model, profile format, device feature catalogue, resilience analysis,
architecture and the optional web API contract. Start with
[`docs/README.md`](docs/README.md).

## Acknowledgements & licensing

This is a standalone Go application of mixed lineage: a **clean-room
reimplementation** of the local Home Connect protocol (from the specification in
[`docs/`](docs/)), the Home Assistant integration **concepts** reimplemented in
Go, built on the reusable infrastructure of the sister project `go-mtec2mqtt`.
Full attribution, third-party licenses and the clean-room statement are in
[`NOTICE.md`](NOTICE.md).

> Note: the upstream `chris-mc1/homeconnect_websocket` reference has **no license**
> (all rights reserved); therefore no code from it is copied or ported — see
> `NOTICE.md`.

## Development

Parts of go-homeconnect2mqtt are developed with agentic AI assistance,
primarily [Claude Code](https://www.anthropic.com/claude-code). Submitted
issues are also triaged and analysed with agentic help. Every change is still
reviewed by a human maintainer and has to pass the project's test suite
before it lands — the AI accelerates the work, it does not replace the
review gate.

Contributions are welcome — AI-assisted or not. The rules for using AI tools
when contributing are in [`AI_POLICY.md`](AI_POLICY.md).

## License

MIT — see [LICENSE](LICENSE).
