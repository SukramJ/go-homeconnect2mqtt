# go-homeconnect2mqtt

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

## MQTT topics

```
<topic>/<device>/<Feature/Path>/state    # e.g. .../BSH/Common/Setting/PowerState/state
<topic>/<device>/<Feature/Path>/set      # writable features only
<topic>/<device>/availability            # online / offline (LWT)
<topic>/<device>/connection_state        # connecting / handshake / connected / reconnecting / ...
```

Feature names use dotted notation (`BSH.Common.Status.OperationState`) mapped to
slash-separated MQTT paths.

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

## License

MIT — see [LICENSE](LICENSE). When reusing logic/constants ported from the
upstream Python reference projects, observe their licenses.
