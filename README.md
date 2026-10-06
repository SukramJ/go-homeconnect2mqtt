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

# 2. Parse it into cached device descriptions + a device inventory entry
#    (it prints each appliance's haId, the device segment of its topics).
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

Since 0.15.0 the topic tree follows the
[mqtt-smarthome 2.0](https://github.com/mqtt-smarthome/mqtt-smarthome/blob/master/SPEC.md)
convention, `<name>/<function>/<item…>`, like every other project of this
family (openccu-loom ADR 0083). `<name>` is `MQTT_TOPIC` (default
`homeconnect`), and the device segment is the appliance's **haId** — a stable
hardware id — instead of its name in `devices.yaml`.

| 0.15.0 and later | Payload | Before (≤ 0.14.0) |
|---|---|---|
| `<name>/connected` | `0` not running (Last Will, graceful stop), `1` on the broker but no appliance reachable, `2` at least one appliance reachable | `<topic>/status` (`online`/`offline`) |
| `<name>/info` | retained JSON: `name` (`go-homeconnect2mqtt`), `version`, `spec`, `go`, `host`, `pid`, `started`, `maintenance`, `commit`, `build_date`, `appliances`, `language` | — |
| `<name>/status/<haId>/<Feature/Path>` | status object, e.g. `{"val":"On","ts":…,"lc":…}` | `<topic>/<device>/<Feature/Path>/state` (plain) |
| `<name>/set/<haId>/<Feature/Path>` | plain value or `{"val":…}` | `<topic>/<device>/<Feature/Path>/set` |
| `<name>/status/<haId>/_uid/<n>` | status object (a feature the profile does not name) | `<topic>/<device>/_uid/<n>/state` |
| `<name>/status/<haId>/online` | status object, `val` `true`/`false` | `<topic>/<device>/availability` (`online`/`offline`) |
| `<name>/status/<haId>/connection_state` | status object, `val` `connecting`/`connected`/`reconnecting`/`offline`/… | `<topic>/<device>/connection_state` |
| `<name>/set/<haId>/_control/start_program`, `…/stop_program` | any non-empty payload | `<topic>/<device>/_control/{start,stop}_program/set` |
| `<name>/maintenance/…` | see [Maintenance](#maintenance) | — |

Feature paths stay Home Connect's own identifiers: the dotted name
(`BSH.Common.Status.OperationState`) becomes the slash-separated item path,
every segment made topic-safe.

**Status payloads.** Every status item is a JSON object `{"val", "ts", "lc"}`
(`ts` when the value was observed, `lc` when it last changed, both in
milliseconds). Booleans are JSON booleans, numbers JSON numbers, Object
features their JSON value. An enum carries its **token** — the member name the
appliance speaks (`On`, `Run`, `Present`) — not the localized label it used to
publish; Home Assistant still shows the labels, through the discovery
payload. An active or selected program that names no known program publishes
the token `None`, which Home Assistant reads as "no value". Status items are
retained, published at QoS 0 on every change and again after every broker
reconnect — never on an unchanged value.

**`set` payloads.** A plain value and `{"val": …}` are the same request.
Booleans accept `true`/`false`, `1`/`0`, `on`/`off` and `yes`/`no` in any
case; numbers are rounded to the feature's step and clamped to its range;
enums are matched by token, case-insensitively, and localized labels are still
accepted. An Object feature takes a JSON object. Empty and retained messages
are ignored, a rejected or failed request is logged at `warn` with its topic
and payload, and `set` is subscribed at QoS 1.

**The haId.** It is read from the `haid` key of a device entry in
`devices.yaml`, or else from the cached description `hc-util parse` writes
(0.15.0 and later record it there). A device for which neither is known is
**refused at start**, with the device named in the error: add `haid:` to its
entry — the haId is the name of the appliance's `<haId>.json` in the profile
ZIP, and `hc-util parse` prints it — or re-run `hc-util parse`. It never
falls back to the device name, because the topics would then move a second
time once the haId is filled in.

**The instance name.** `MQTT_TOPIC` is the only thing that keeps two
instances on one broker apart, and nothing checks it: two instances with the
same name write the same topics and overwrite each other's `connected` and
`info`. Give each instance its own name. Note also that the default,
`homeconnect`, is the default instance name of hobbyquaker's Node.js adapter
`homeconnect2mqtt` as well — running both on one broker needs one of them
renamed. The name is one topic level: since 0.15.0 a `MQTT_TOPIC` containing
`/`, `+` or `#` is refused at start.

### Migrating from 0.14.x

0.15.0 is a clean break: there is no compatibility switch, and anything that
reads the raw topics — Node-RED flows, dashboards, scripts — has to move to the
table above and read `val` out of the JSON. **Home Assistant needs nothing**:
the discovery document re-points every entity to the new topics and keeps its
`unique_id`, entity id, device and history.

- **The old retained topics are cleared automatically.** On every start the
  daemon reads back, for a few seconds, what the broker holds under the old
  tree and clears exactly the topics the old release published for the
  appliances in `devices.yaml` — each feature's `…/state`, `…/availability`,
  `…/connection_state` — plus the old `<topic>/status`. It never matches by
  prefix, so another instance's topics, an appliance it is not configured
  with, and anything else under the same root are left alone. It also clears
  status items an appliance no longer has (`<name>/status/<haId>/…`). A
  second start finds nothing to do.
- **What it leaves:** an appliance that was removed from `devices.yaml` before
  the upgrade (its name is no longer known), and an appliance whose name in
  `devices.yaml` is one of the convention's function names — `status`, `set`,
  `get`, `info`, `meta`, `connected`, `maintenance` — because its old topics
  cannot be told from the new tree by their shape. Clear those by hand, e.g.
  `mosquitto_pub -h <broker> -u <user> -P <password> -t 'homeconnect/status/availability' -r -n`.
- **`MQTT_RETAIN` is removed.** Status items are always retained (spec §3.2);
  a leftover key is ignored with a `config.key_removed` warning at start.
- **`MQTT_QOS` now governs only** the discovery documents, `connected` and
  the Last Will. Status items are QoS 0 and `set` is subscribed at QoS 1, as
  the convention fixes them.
- **Every appliance needs its haId** (see above). Entries written by
  `hc-util parse` from 0.15.0 on carry `haid:`; the add-on fills it from the
  `haid` option.
- **Rolling back to 0.14.x** needs no manual step: it republishes its own
  discovery document and its own topics. The 0.15.0 tree stays retained until
  the next 0.15.0 start, whose sweep clears the old one again.

### Maintenance

On by default, as mqtt-smarthome 2.0 §7 recommends; `MQTT_MAINTENANCE: false`
switches it off (and `<name>/info` says so).

| Topic | Effect |
|---|---|
| `<name>/maintenance/set/loglevel` | `error`, `warn`, `info` or `debug`: the daemon's log level, until the next start |
| `<name>/maintenance/set/restart` | any payload: a graceful shutdown — `connected` goes to `0`, the process exits 0 — and only where a supervisor restarts it afterwards; otherwise refused and logged at `warn` |
| `<name>/maintenance/stats` | retained process statistics (`rss`, `heapUsed`, `heapTotal`, `cpu`, `uptime`, `ts`) every `MQTT_STATS_INTERVAL` seconds (default `60`, `0` = off) |

Whether a supervisor restarts the daemon is answered by `HC2M_SUPERVISED`
(`1`/`true` or `0`/`false`) when it is set, and otherwise detected: systemd,
Kubernetes or a container count as supervised. A container started without a
restart policy is detected as supervised too — set `HC2M_SUPERVISED=0` there.
The Home Assistant add-on sets it to `0`, because the Supervisor does not
restart an add-on that exits cleanly.

**Security.** Anyone who may publish on the broker can restart the daemon or
raise its log level through these topics. Use broker authentication and
per-client ACLs — the daemon needs `<name>/#` and the discovery prefix, nothing
else — and switch maintenance off on a broker that cannot be secured.

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

**A component this daemon stops publishing is removed.** Narrowing what is
published — excluding a feature in `mapping.yaml`, switching `HASS_DISCOVERY`
from `full` to `curated` (510 of 687 components per appliance), or replacing
an appliance's description file — deletes those entities from Home Assistant,
together with their recorder history and anything that references them.

A device document does not delete a component by leaving it out, so the
document carries a *tombstone* for each one instead: an entry holding a
platform and nothing else, which is Home Assistant's own form for a removal.
To know what it published last time, the daemon reads its previous document
back from the broker once per connection, immediately before the first
publish of that connection. Every way that read can fail produces *fewer*
removals and never different ones, and a document published by a second
instance of this daemon is judged component by component: a component is
carried forward only when its payload names a topic **only this instance
renders** — `<MQTT_TOPIC>/connected` or `<MQTT_TOPIC>/status/<haId>/online`,
or, in a document an earlier release left, `<MQTT_TOPIC>/status` or
`<MQTT_TOPIC>/<appliance>/availability` — so a sibling instance's entities are
never deleted.

That is an exact match rather than a prefix, and the difference is the whole
guarantee. A prefix test claimed every component of an instance rooted
*under* this one (`homeconnect` against `homeconnect/kitchen`) and deleted
its live entities. Two instances that share a `MQTT_TOPIC` root **are** one
instance as far as this rule can tell, and nothing distinguishes them: give
each instance its own root.

The daemon warns once per appliance on start when the curated filter drops
anything (`hass.curated_components_omitted`, with the count). Setting
`HASS_DISCOVERY` back to `full` re-creates the entities; it does not bring
their history back.

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
