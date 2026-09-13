# Changelog

All notable changes to this project are documented here. The format loosely
follows Keep a Changelog; versions track `internal/version/version.go`.

## [Unreleased]

### Changed
- **Home Assistant discovery is now one retained *device document* per
  appliance instead of one retained config per entity.** Where this daemon
  published 687 topics under
  `homeassistant/<platform>/<appliance>/<feature>/config`, it now publishes a
  single `homeassistant/device/<appliance>/config` (~460 KB) that carries
  every component. Home Assistant reads both forms; a document is what it
  asks new integrations to publish, and it is one message per appliance
  instead of several hundred on every reconnect.
  **What you will see:** nothing, if it goes well. No entity id, unique id,
  device identifier or history changes — Home Assistant keys the registry on
  `unique_id`, which does not move — so entity names you renamed, areas you
  assigned, dashboard cards and automations all survive. The old per-entity
  configs are retracted first, in the same step, because Home Assistant
  refuses a document while a per-entity config for the same `unique_id` is
  still retained (and refuses the reverse just as firmly), reporting only
  `WARNING [mqtt.entity] Received a conflicting MQTT discovery message`.
  **If the daemon is killed part-way through the migration** — after the
  retractions, before the document — the appliance's entities are *gone*, not
  unavailable, until the daemon is started again. The next start redoes both
  halves: nothing is remembered across a restart, by design.
  **If your broker limits the packet size** below the size of the document,
  the migration is refused *before* anything is retracted, your existing
  entities are left exactly as they are, and the daemon logs
  `hass.bundle_publish` naming the size and the broker's limit. Raise the
  broker's `max_packet_size` (Mosquitto's default is unlimited; EMQX's is
  1 MB) and restart. A broker that advertises no limit, and an MQTT 3.1.1
  broker, are treated as "no limit", never as a small one.
  **If you roll back to an earlier release**, the retained device document is
  still on the broker and the old release's per-entity configs will be
  refused the same way, leaving you with no entities. Clear the document
  once, by hand, before or after starting the old version:
  ```sh
  mosquitto_pub -h <broker> -u <user> -P <password>     -t 'homeassistant/device/geschirrspuler/config' -r -n
  ```
  The last path segment before `config` is the *slug* of the appliance name
  as it appears in `devices.yaml` — `Geschirrspüler` becomes
  `geschirrspuler`, not `Geschirrspüler` and not `geschirrspüler`. Copy the
  topic verbatim from the daemon's own `hass.bundle_published` log line
  rather than assembling it; that is the one spelling that cannot be wrong.
  Repeat it once per appliance. Home Assistant will then re-adopt the same
  entities from the per-entity configs, with their history.
  **One capability is not yet carried over.** Under the per-entity form, a
  feature that leaves this daemon's set — excluded in `mapping.yaml`, dropped
  by switching `HASS_DISCOVERY` from `full` to `curated`, or missing after an
  appliance is replaced or its firmware changes — had its config retracted
  and its entity disappeared. A device document does not remove a component
  by omitting it, so such an entity now stays in Home Assistant, reading
  *available* and showing its last value, until it is deleted by hand. To
  remove one: **restart Home Assistant (or reload the MQTT integration)
  first** — the delete option only appears once the entity is no longer being
  provided — then open the device page and delete the entity there.
- The daemon now re-publishes discovery on every broker (re)connect, not only
  when an appliance reconnects or Home Assistant restarts. A broker that came
  back without its retained store previously left every entity missing until
  the daemon itself was restarted.

### Fixed
- **A second instance of this daemon on the same broker could have its button
  entities deleted by the first.** Two instances with different `MQTT_TOPIC`
  roots, the same `HASS_BASE_TOPIC` and an appliance name in common publish
  byte-identical config topics and unique ids, so the orphan cleanup tells
  them apart by the topics inside the payload. A `button` config carries no
  `state_topic` — twenty per appliance — and for those the check fell back to
  the shared `homeconnect_` name prefix and claimed the other instance's as
  its own. Reachable with one instance in `curated` mode and one in `full`,
  and whenever `HASS_DISCOVERY_REFRESH` was used. The check now reads every
  topic a payload names (`state_topic`, `command_topic`, the availability
  sources) and requires all of them to be under this instance's own root.
- The daemon's own birth and death markers could rejoin the MQTT circuit
  breaker after a refactor, because the topic that keeps them off it was
  spelled twice at the composition root. A connection drop opens the breaker,
  so the first act of a reconnected daemon — announcing itself online — would
  have been refused, leaving every entity unavailable until a recovery probe
  happened to succeed.
- Discovery could still be published after the daemon announced itself
  offline on shutdown, leaving a retained "online"-era config behind.
- **Eleven entities per appliance were being discarded by Home Assistant
  without a word.** `mapping.yaml` mirrors the official `home_connect`
  integration, where thirteen features are *binary* sensors, so it gives them
  a `device_class` from the binary-sensor vocabulary — `door`, `plug`,
  `connectivity`, `light`, `battery_charging`. Whenever an appliance reports
  one of them as anything but a plain on/off value (nine refrigeration doors
  and the charging connection arrive as text), this daemon publishes it as a
  `sensor`, and the `sensor` platform declares none of those classes. Home
  Assistant dropped each config during schema validation: no error, no log
  line, no entity, while the other 676 appeared normally. The daemon now
  checks every `device_class` against Home Assistant's own per-platform
  vocabulary before publishing it, and logs
  (`hass.device_class_refused`) any catalogue override it has to drop.
  **What you will see:** eleven entities appear where nothing was, as
  disabled-by-default diagnostics; ten of them without an icon or device
  class, and *Battery charging state* as a proper enumeration sensor with a
  translated options list. No entity id, unique id or platform changes, so no
  history is lost and there is nothing to do by hand. On an appliance with no
  refrigeration compartments — a dishwasher, an oven — nine of the eleven are
  features the appliance does not have and never appear at all.
- **Entities now go unavailable when the daemon dies.** The Last Will wrote
  `<MQTT_TOPIC>/status` and no discovery payload referenced it, so on a
  crash, SIGKILL or an unclean broker loss every entity stayed *available*,
  showing its last retained value indefinitely. Every payload now declares
  both availability levels — the bridge status topic and the device
  availability topic — with `availability_mode: all`. The topic itself did
  not move (it was already in the daemon's own publish root, not in Home
  Assistant's discovery tree), so there is no retained copy to clear and an
  automation watching it keeps working. The Last Will also goes out at
  `MQTT_QOS` now instead of QoS 0, matching the birth publish.
- A writable *selected program* element on an appliance that exposes no
  programs was published as a `select` with an empty `options` list — a
  visible, writable dropdown that could never be set. It is now a read-only
  sensor, as the non-writable case has always been. The superseded `select`
  config is retracted by the daemon itself on the next discovery run; no
  operator step. That one entity re-registers under the `sensor` domain.
- German dropdown options were sorted by their English originals: the
  operating state read `Auto, Aus, Ein` (Auto/Off/On). Localised options are
  now sorted in the display language, with umlauts collated as DIN 5007-1
  does. The option *set* is unchanged, so retained state stays valid.
- The device's command subscription (`<root>/<device>/#`) matched all 689 of
  this daemon's own publishes for that device and the broker echoed every one
  of them back. It now subscribes with MQTT 5.0 No Local, and checks that an
  arriving topic is a command topic before dispatching anything — the latter
  matters because No Local does not cover the retained replay a broker sends
  on subscribe, and because with `MQTT_RETAIN: false` every state publish used
  to spawn a goroutine whose only job was to return.
- A device name in `devices.yaml` is validated. `+` and `#` are rejected
  because the subscription filter does not escape them (a device named `#`
  subscribed the daemon to every topic on the broker); so are control
  characters, invalid UTF-8, and a name with no ASCII letter or digit, which
  produced an empty Home Assistant node id. Non-ASCII names such as
  `Geschirrspüler` remain fully supported, and so does a name containing `/`.

### Changed
- Every MQTT topic this daemon builds is now composed in one place
  (`internal/layout`) instead of six across three packages. The state topic
  was built twice and the two synthetic program buttons' command topic was
  spelled twice — once by the discovery layer that advertises it, once by the
  handler that acts on it, with nothing comparing them. Nothing on the wire
  changes.
- Both discovery payload builders (the feature-derived entities and the two
  synthetic program buttons) now share one function for the keys every entity
  carries, so a key added to one cannot be missing from the other. Nothing on
  the wire changes.
- `mqttSession` is replaced by `mqtt.SplitClient` from go-mqtt v1.4.0.
- The daemon's publish and subscribe loops now run on the shared
  [`go-hamqtt`](https://github.com/SukramJ/go-hamqtt) `publisher` package
  instead of four hand-rolled copies of the same thing: entity state and
  availability, the inbound command routing, the birth/Last-Will pair on
  `<MQTT_TOPIC>/status`, and the sweep that clears retained discovery
  configs this daemon no longer publishes. **Every published topic, every
  QoS and every retain flag is unchanged** — that was measured against a
  byte-level pin of the whole topic tree, not assumed. Four operator-visible
  effects:
  - **Far fewer broker messages.** An appliance re-reports every feature on
    every notification whether or not anything moved, and each of those used
    to cost one retained publish and one Home Assistant state evaluation.
    An unchanged value is now compared and dropped. A changed one still goes
    out, and a reconnect re-sends everything, so nothing can be left stale by
    a broker that came back without its retained store.
  - **Commands no longer run on the MQTT read loop.** Each one used to start
    its own goroutine, unbounded and unordered, because a Home Connect write
    blocks for seconds and would otherwise stall acknowledgement handling
    into a spurious reconnect. There is now a worker pool that preserves the
    order of two commands on the same topic.
  - **The orphan sweep opens one snapshot subscription
    (`<HASS_BASE_TOPIC>/#`) instead of two narrower ones.** What it is
    willing to delete got *narrower*, not wider: a retained config must be
    in the five-segment form this daemon publishes, on one of the six
    platforms it emits, under the node id of a device in this instance's
    configuration, and its payload must carry a `unique_id` in the
    `homeconnect_` namespace *and* a state topic under this instance's own
    `MQTT_TOPIC`. A second instance of this daemon on the same broker, with
    a different `MQTT_TOPIC` and an appliance of the same name, is
    untouchable by it.
  - **A reconnect re-announces "online" and rebuilds what the daemon
    believes the broker holds.** Previously only the first connect did, so a
    reconnected daemon could sit "offline" in Home Assistant while happily
    publishing state nobody displayed.

### Changed
- Bump [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt)
  v1.3.0 -> v1.5.1. Two minors, both relevant. v1.4.0 adds
  `mqtt.SplitClient`, which makes this daemon's hand-rolled `mqttSession`
  redundant (not yet removed). v1.5.1 fixes a double-dispatch defect: through
  v1.5.0 a delivery carrying no subscription identifier was matched by topic
  against stamped subscriptions too, so a consumer with a broad subscription
  overlapping its own command tree could run a handler twice per published
  message. This bridge subscribes to the whole device sub-tree
  (`<root>/<device>/#`), which makes it the most exposed of the sister
  projects; its remaining filters do not overlap it, so no doubled write has
  been observed. No source changes were needed.

### Added
- Golden-file pins for everything published to Home Assistant: all 687
  discovery payloads per appliance (in both languages, both `HASS_DISCOVERY`
  modes, with and without `mapping.yaml`) together with their topic, QoS and
  retain flag, the identity plane on its own, and the complete state /
  command / availability / subscription tree. Test-only; nothing on the wire
  changes. See `notes/adr0070-phase7-measurement.md`.

### Removed
- Stop publishing `object_id` in HA discovery payloads (entity configs in
  `internal/hass/payload.go` and the program-control buttons in
  `internal/hass/discovery.go`). Home Assistant's MQTT discovery schemas are
  `extra=REMOVE_EXTRA`: a key no platform declares is dropped on arrival,
  silently — no error on the wire, no log line. Measured against the schemas
  of Home Assistant 2026.9, `object_id` is accepted by **0 of the 32 MQTT
  platforms**, `default_entity_id` by 28. The key added in 0.10.1 has
  therefore been doing nothing since HA removed it; it was only dead weight in
  every retained config and misleading to anyone reading those payloads. No
  user-visible effect: every payload already carried `default_entity_id` with
  the identical English, language-independent seed, so entity ids are
  unchanged.

## [0.12.0] - 2026-08-16

### Changed
- Bump [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt)
  v1.2.0 -> v1.3.0, an audit release with 42 findings fixed. No source
  changes were needed on our side — the new API surface
  (`LifecycleConfig.FlapWindow`, `ConnectResult.ServerKeepAliveSet`) is
  additive and this bridge doesn't touch either.
- Behavior change: a connection that drops within 10s of connecting now
  reconnects with exponential backoff instead of immediately (flap
  damping, `LifecycleConfig.FlapWindow`, new default). Our `LifecycleConfig`
  in `cmd/homeconnect2mqtt/main.go` sets `InitialBackoff`/`MaxBackoff`/
  `Jitter`/`Logger` only, so it inherits the new default — a broker that
  bounces the link repeatedly right after connect no longer produces a
  tight reconnect loop.
- The publish-path circuit breaker (`mqtt.NewBreaker`, wired in
  `cmd/homeconnect2mqtt/main.go`) no longer opens on client-side
  validation errors (e.g. `protocol.ErrProtocolViolation`); only genuine
  broker-side/transport failures count toward its 5-failure threshold, so
  `homeconnect2mqtt.mqtt_breaker_state` now reflects broker health more
  accurately.
- Also inherited without code changes: no spurious reconnect after an
  intentional `Lifecycle.Stop` (our clean-shutdown path), and stricter
  inbound wire validation (a malformed broker frame now tears the
  connection down per MQTT §4.13 instead of being tolerated).

## [0.11.1] - 2026-08-07

Discovery fixes: Home Assistant rejected three classes of config payload, so
the affected entities never appeared.

### Fixed
- Event binary sensors no longer carry `device_class: enum`. `enum` is a
  sensor-only class, and HA discards the whole entity when it sees it on a
  binary sensor. Discovery now validates `device_class` against the platform
  it publishes to (and drops `unit_of_measurement`/`state_class` where the
  platform has none), so neither the heuristic nor an operator override can
  produce a config HA refuses. `device_class: enum` is also removed from the
  38 event features in the shipped `mapping.yaml`.
- Command buttons publish the required `command_topic` (plus
  `payload_press: "true"`, the value written to the boolean command feature)
  and no longer a meaningless `state_topic` — previously HA logged
  `required key not provided @ data['command_topic']` and skipped them, so
  `PauseProgram`/`ResumeProgram` and friends were missing.
- An active/selected program that does not resolve to a named program —
  idle (raw uid `0`), or a uid the profile does not name — publishes `None`
  instead of the raw uid, which HA logged as `Invalid option` while keeping
  the stale value. `None` clears the select and sets the sensor unknown.

## [0.11.0] - 2026-07-07

Hardening release: an adversarially verified audit across untrusted input,
secrets handling, concurrency and resource safety — every confirmed finding
fixed, each fix covered by tests.

### Security
- Reject unsafe `haId` values from profile archives (`^[A-Za-z0-9._-]+$`,
  no `.`/`..`): a crafted ZIP index could previously escape the `--out`
  directory via `haId: "../../…"` and write files anywhere. Unsafe profiles
  are skipped + logged per lenient loading; `hc-util parse` keeps a
  belt-and-braces file-name check.
- Cap ZIP inflation while parsing profile archives (16 MiB per entry,
  64 MiB per archive, declared-size fast reject): a decompression bomb now
  yields `ErrInvalidProfile` instead of OOMing the process; other archives
  in the directory still load.
- Cap the `POST /api/devices/{device}/set` body at 1 MiB (413
  `payload_too_large`) and reap idle keep-alive connections
  (`IdleTimeout` 120 s) on the diagnostics web server.
- `WriteInventory` recreates the inventory file, so the documented 0600
  mode holds even when a lax-permission file already exists at the path.
- Structural redaction enforcement: `DeviceConfig`/`DeviceProfile`/
  `InventoryEntry` implement `slog.LogValuer` (PSK/IV always masked) and
  both binaries install a `ReplaceAttr` guard that masks any secret-keyed
  log attribute (psk/iv/serialNumber/mac/shipSki/deviceID, all common
  casings).

### Fixed
- A failed connect attempt now tears down its half-open connection: the
  reconnect manager closes the connection on connect failure and a failed
  session handshake self-cleans. Previously every retry against a
  handshake-failing appliance leaked one socket + receive-loop goroutine,
  and the stale loop raced the next attempt's session state.
- The session receive loop runs on a connection-scoped context: setting
  `ReconnectConfig.ConnectTimeout` no longer kills every successfully
  established connection the moment the connect phase ends.
- AES transport sends hold one lock across encrypt + wire write, so
  concurrent commands can no longer put frames on the wire in a different
  order than the CBC/HMAC chain advanced (an unrecoverable desync).
- TLS-PSK (cgo) transport: the `Read`/`Write` pumps re-check `closed`
  under the lock, fixing a potential SIGSEGV when `Close` frees the
  OpenSSL objects while a blocking read is being unblocked.
- A recovered device-worker panic no longer silently kills that device
  until restart: the worker publishes offline, backs off (1 s doubling to
  30 s) and re-enters its reconnect loop, so retained availability can no
  longer stick at `online` with stale values.
- Web server shutdown no longer stalls the full 5 s budget while an SSE
  client is connected: open SSE streams are told to end when shutdown
  starts (regular in-flight requests keep the full grace window), and a
  shutdown error is logged + force-closed instead of discarded.

### Changed
- Adopt [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt)
  v1.2.0 (up from v1.1.0): the client's own hardening release (28 verified
  findings — serialized connect/disconnect, cross-epoch quota/packet-id
  protection, typed ack waiters against forged acknowledgements, decoder
  robustness). No exported signatures changed.

### Added
- Active WebSocket heartbeat: the `HEARTBEAT` config option (default 20 s)
  is now actually wired — a failed ping drops the connection so a silently
  dead appliance is detected within seconds instead of minutes of stale
  `online` state.
- Entity state publishes are decoupled from the appliance receive loop via
  a per-device bounded FIFO publisher: an MQTT broker brownout can no
  longer stall websocket frame processing. Every transition is preserved in
  order (event pulses like `Present → Off` are never coalesced away); only
  a full backlog (1024 entries) drops the oldest update, with a warning.
- Panic isolation now also covers the appliance data path and the publish
  drain goroutine: a malformed frame drops that frame (not the daemon),
  and a panicking MQTT publish drops that publish (not the process).

## [0.10.1] - 2026-07-06

### Fixed
- Publish `object_id` alongside `default_entity_id` in every HA discovery
  payload (entities and the program-control buttons). Current Home Assistant
  releases do not yet honour `default_entity_id` reliably
  ([home-assistant/core#157241](https://github.com/home-assistant/core/issues/157241)):
  the seed is ignored and HA derives a generic `entity_id` from the localized
  name instead. The deprecated-but-still-working `object_id` fixes that on
  current HA, while `default_entity_id` keeps future HA correct. Both carry the
  same English, language-independent seed; only the display `name` is localized
  and `unique_id` stays independent.

## [0.10.0] - 2026-07-04

### Added
- Adopt the circuit breaker shipped with
  [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt) v1.1.0
  (up from v1.0.0, additive release): the bridge's publish path (device
  state, discovery configs, orphan-reconcile cleanups) now fails fast with
  `ErrCircuitOpen` during a degraded-broker phase (TCP link up, acks
  missing) instead of each publish stalling on the ack timeout. 5
  consecutive broker-side failures open the circuit, a single half-open
  probe tests recovery after 30s, one success closes it again. State
  transitions surface as a `homeconnect2mqtt.mqtt_breaker_state` warning
  log. Subscriptions and the lifecycle-owned status-topic publishes are
  not gated — the reconnect loop stays in charge of the link itself.

## [0.9.0] - 2026-07-04

### Changed
- Adopt [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt)
  v1.0.0 (up from v0.2.0). **MQTT 5.0 is now the default wire protocol**
  (3.1.1 remains selectable via `ProtocolVersion`); no bridge doc promises
  MQTT 3.1.1-only broker support, and Home Assistant's bundled Mosquitto
  already speaks v5. Reconnect handling is now event-driven rather than
  polling, and the underlying client gains full QoS 0/1/2 support (this
  bridge still only publishes/subscribes at QoS 0/1).
- Subscriptions are now confirmed by the broker: `Subscribe` blocks until
  the SUBACK arrives and returns a hard error on a broker-rejected filter,
  instead of the previous fire-and-forget call that only logged a
  rejection after the fact.
- Publishing now fails fast against a dead connection
  (`ErrNotConnected`/`ErrConnectionLost`) instead of blocking until a
  timeout; command, birth-topic, and discovery-reconcile handlers were
  migrated to the new `func(*mqtt.Message)` handler signature
  (`msg.Topic`/`msg.Payload`/`msg.Retain`).
- `TCPConfig.WillTopic`/`WillPayload`/`WillRetain` were replaced by a
  single `Will: &mqtt.Will{...}` struct, and `CleanSession` was renamed
  `CleanStart` (same wire bit). Both are internal wiring only — no add-on
  option or environment variable changed.

## [0.8.2] - 2026-07-03

### Fixed
- Adopt go-mqtt v0.2.0 (retained MessageHandler flag, per-filter QoS replay,
  hardened ping watchdog). Command and birth MQTT handlers now dispatch
  asynchronously so blocking work no longer stalls the read loop and trips
  spurious ping_timeout reconnects.

## [0.8.1] - 2026-07-02

### Docs
- Retargeted stale `internal/mqtt` documentation references (`NOTICE.md`,
  `docs/06-architecture.md`, `docs/09-implementation-plan.md`) to the
  extracted `github.com/SukramJ/go-mqtt` module. No functional change.

## [0.8.0] - 2026-07-02

### Changed
- The MQTT client is now sourced from the shared
  [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt) module
  (v0.1.0) instead of the locally vendored `internal/mqtt`. The client used to
  be duplicated four times over across the `go-*2mqtt` bridges
  (`go-mtec2mqtt`, `go-daikin2mqtt`, `go-homeconnect2mqtt`, `go-zendure2mqtt`);
  it now lives once, so a fix lands in one place and every bridge picks it up
  via `go get -u`. Behaviour is unchanged — the module is a superset of this
  repo's own `internal/mqtt` and keeps the `loopDone` teardown fix (`Stop()`
  now waits for the reconnect loop to exit before returning).

### Security / Fixed (inherited from the shared module)
- MQTT frames now carry a 1 MiB frame-size cap that rejects an oversized
  `remaining length` before allocating a body buffer, closing an OOM/DoS
  vector against a malicious or malfunctioning broker.
- Broker-rejected subscriptions (SUBACK failure return codes) are now
  surfaced and logged instead of being silently ignored.

## [0.7.1] - 2026-07-02

### Fixed
- MQTT half-open connections are now detected and recovered. The keep-alive loop
  sent PINGREQ but never checked that the matching PINGRESP came back, and the
  read loop runs without a read deadline — so a broker/network drop without a TCP
  FIN/RST (e.g. a Mosquitto or Home Assistant restart) left the read loop blocked
  in `ReadFrame` forever: the socket was never torn down, no reconnect happened,
  and QoS-1 publishes (Home Assistant discovery configs) timed out with
  `context deadline exceeded` on the dead socket until a manual add-on restart. A
  PINGRESP watchdog now declares the connection lost when a keep-alive ping goes
  unanswered, so the existing reconnect logic re-dials automatically (within one
  keep-alive interval).

## [0.7.0] - 2026-06-28

### Added
- `HASS_DISCOVERY_REFRESH` (add-on `hass_discovery_refresh`): a one-shot migration
  flag. On start the daemon clears every retained discovery config it owns, waits
  for Home Assistant to drop the entities, then the device workers re-create them
  — so HA picks up changes it caches at first registration (entity **category**,
  name), which a plain re-publish does not update. Turn it back off after one run.
  Resets per-entity room/custom-name; entity ids and automations are preserved.

## [0.6.3] - 2026-06-28

### Fixed
- Numeric enum values are no longer mistranslated. A bare number ("60", "90") in
  a dropdown — e.g. a hob power level — collided in the flat enum catalogue with a
  program leaf that normalized to the same number, so the hob power level "60"
  showed "Waschen und trocknen (60 min)". Numbers now always pass through
  untranslated (a number stays a number), and the offending numeric catalogue
  keys were removed. Named levels (Boost, Warmhalten, Aus) stay translated.

## [0.6.2] - 2026-06-28

### Fixed
- Entity category: writable controls are now `config` (the Configuration section)
  instead of `diagnostic`. Writable **options** (and command buttons) were
  miscategorized as diagnostic, so hob controls like zone selector, power level,
  frying-sensor level, duration and join-zone landed under Diagnostic instead of
  Configuration. Read-only settings/options/status/events stay diagnostic; the
  main set stays primary.

## [0.6.1] - 2026-06-27

### Added
- Hob (cooktop) controls in curated mode: the writable hob features — zone
  selector, power level, frying-sensor level, join/automatic zone selection, key
  lock, power management, timers, buzzer volume, energy indication, and the
  extractor-hood automation — are now enabled-by-default, so a hob is
  controllable from Home Assistant via its program-assistant model (choose zone +
  level + program, then start). Per-zone power stays read-only — Home Connect
  exposes no direct remote zone power control.

## [0.6.0] - 2026-06-27

### Changed
- **`HASS_DISCOVERY: curated` is now the default** (add-on `hass_discovery`), and
  the curated set is aligned with the official Home Assistant `home_connect`
  integration: only the official-catalogue features are enabled-by-default, so a
  typical three-appliance setup drops from ~590 created entities to ~60 (the
  native integration creates ~74). `full` still exposes every feature. Stale
  entities from earlier runs are removed automatically by the discovery
  orphan-cleanup once the daemon publishes the curated set.

### Added
- **Program controls** ("choose, then start"): a synthetic *Start program* /
  *Stop program* button per appliance that runs programs. Start posts the program
  chosen in the *Selected program* select to `/ro/activeProgram` (the appliances
  expose no start command of their own); Stop aborts the active program.
- The remaining untranslated program/recipe names — the hob frying-sensor recipes
  (Speck, Béchamelsauce, Käsesauce, …) and a few washer programs — are now German
  too (~65 added; the enum catalogue is ~765 members).

### Fixed
- The active/selected program publishes empty while idle instead of the raw uid
  `0`, so a program select shows "no selection" rather than a number.

## [0.5.3] - 2026-06-27

### Added
- Selected program is now a writable **select**: when the feature is writable it
  is exposed as a Home Assistant select listing the (localized) program names,
  and choosing one resolves the label back to its program and posts it to
  `/ro/selectedProgram` (the program-selection path). The active program stays a
  read-only sensor. Verified end-to-end on the dishwasher — selecting "Eco 50 °C"
  makes the appliance select it, and it round-trips back to "Auto 2".

## [0.5.2] - 2026-06-27

### Added
- Active/selected program now resolves to the (localized) program **name**
  instead of the raw program id: the active/selected-program entities get an
  enumeration mapping each program uid to its short name, so they publish e.g.
  "Auto 2" / "Eco 50 °C" rather than `8195`. While idle (no active program) the
  active-program entity still reads `0`.

### Fixed
- The Home Assistant add-on showed no changelog — there was no
  `addon/CHANGELOG.md` (the Supervisor renders that file in the add-on's
  *Changelog* tab). It now mirrors `changelog.md`, with a CI check keeping the
  two in sync (`make addon-changelog` regenerates it).

## [0.5.1] - 2026-06-27

### Added
- Localize the remaining device-specific enum/dropdown values: ~300 member
  labels harvested from the appliance profiles — settings/statuses the official
  integration does not expose as selects (hob key-lock/buzzer/timer, washer
  textile type, language codes, …) — are now German too. The enum catalogue grew
  to ~700 members. Verified end-to-end across the real dishwasher, washer and
  hob: **100 %** of entity names *and* enum/dropdown values localized.

## [0.5.0] - 2026-06-27

### Added
- Comprehensive German localization that mirrors the official Home Assistant
  `home_connect` integration: **all** entity names and enum/dropdown values are
  localized (de/en) across every appliance domain (dishwasher, washer, dryer,
  oven, hob, hood, fridge, coffee maker, cleaning robot, …). Entity ids and MQTT
  topics stay English.
- `mapping.yaml` is now a ~680-feature catalogue derived from the official entity
  descriptions (`name`/`name_de`/`device_class`/`entity_category`, and
  enabled-by-default for the official set), joined via `aiohomeconnect` feature
  keys and the appliance profiles. Curated extras the official set omits are
  preserved. German labels are project-authored (the integration's `de.json` is
  not distributed with its source).
- `internal/i18n` enum catalogue (`catalog_gen.go`) grew to ~400 German member
  labels (states, settings, programs, options) with normalized,
  separator-insensitive lookup.

### Known limitations
- Active/selected program is still published as the raw program id rather than
  the program name (program-id resolution is a separate change); the German
  program labels already live in the catalogue for when it lands.

## [0.4.0] - 2026-06-27

### Added
- Localized dropdown/enum values: `select` options, enum-sensor options and the
  published state now follow `LANGUAGE` (de/en), and the write path accepts
  localized labels — the sister-project approach (HA's native enum translations
  aren't available to MQTT discovery). Entity ids and topics stay English. Common
  cross-appliance member names (operation/door/power state, remote-control level,
  common settings) ship translated; uncatalogued values pass through unchanged so
  options and state stay consistent.
- Enum sensors now publish their `options` (required by HA's `enum` device class;
  previously only `select` did).
- More German entity names (`RemoteControlLevel` + the common dishwasher settings).

## [0.3.1] - 2026-06-27

### Added
- Discovery orphan cleanup: after (re)publishing a device's Home Assistant
  discovery, the bridge clears its own retained config topics it no longer
  publishes — features now excluded/renamed/re-platformed, or dropped by curated
  mode — so they don't linger as unavailable entities. Scoped per device, guarded
  by `unique_id`/state-topic ownership (never touches other integrations), async.

## [0.3.0] - 2026-06-27

### Added
- Localized entity names: Home Assistant friendly names follow `LANGUAGE`
  (de/en) while entity ids stay English and language-independent (seeded via
  `default_entity_id`, the replacement for the removed `object_id`).
- Entity decluttering — the bridge still exposes every feature, but the long
  tail is now published `enabled_by_default: false` (one click to enable) and
  categorized (`entity_category: diagnostic`/`config`), so a device drops from
  ~195 to ~28 entities shown by default. A small primary set stays enabled.
- `HASS_DISCOVERY: full|curated` (add-on `hass_discovery`): `curated` publishes
  only the primary set; `full` (default) publishes everything.
- `mapping.yaml` is now a curation catalogue: per feature `name`/`name_de`,
  `state_class`, `entity_category`, `enabled_by_default`, `exclude` (in addition
  to `device_class`/`unit`). A curated default catalogue ships with the project.
- Numeric sensors get a `state_class` (measurement / total_increasing) for
  long-term statistics.

## [0.2.1] - 2026-06-27

### Fixed
- Add-on MQTT auto-discovery used the wrong scheme: `run.sh` tested the exit
  code of `bashio::services 'mqtt' 'ssl'` (always 0) instead of its value, so it
  always built `ssl://` and failed against the plaintext Home Assistant Mosquitto
  broker. Test the value now.
- MQTT TLS dial failed with "either ServerName or InsecureSkipVerify must be
  specified": default the TLS `ServerName` to the broker host for `ssl://` URLs.

## [0.2.0] - 2026-06-27

### Added
- TLS-PSK transport for older appliances (`connectionType: TLS`,
  `wss://host:443`): OpenSSL-backed via cgo behind `-tags tlspsk`, TLS 1.2
  ECDHE-PSK, driven through memory BIOs with the WebSocket layer over the tunnel
  (docs/01-protocol.md §4). Verified end-to-end against a real Neff appliance.
- `hc-util connection-test` now dispatches on the connection type, so it
  connects both AES and TLS devices (was AES-only).
- Add-on auto-config: drop the profile ZIP(s) into `/share/homeconnect` and the
  entrypoint parses **all** of them, writing a keys inventory (`/data/profiles/
  inventory.json`, 0600). A `devices` entry then only needs `name` + `host` +
  `haid` — `connection_type`/`psk64`/`iv64`/`description` are auto-filled from the
  matching ZIP. `hc-util parse` accepts a directory and a `--inventory` flag, and
  no longer prints secrets when writing an inventory (fixes a key leak into the
  add-on log). Flags are now honoured after the path argument too.

### Changed
- The Home Assistant add-on image is now **amd64-only** and built with cgo +
  OpenSSL (`-tags tlspsk`), so it supports both AES and TLS-PSK appliances out of
  the box. The CGo-free default `go build` still cross-compiles for AES-only
  standalone use; TLS devices there report `ErrTLSPSKUnsupported`.

## [0.1.1] - 2026-06-27

### Added
- Home Assistant add-on manifest (`addon/config.yaml`) so the Supervisor can
  discover, install and configure the add-on (options/schema, per-arch GHCR
  image, `map: share:rw`, `services: mqtt:want`, Ingress web UI).
- `/share/homeconnect/` drop folder, created on add-on start, as the place to
  copy the profile ZIP or pre-parsed `<haId>.json` files.

### Fixed
- `.gitignore` no longer swallows `addon/config.yaml`: the `config.yaml` /
  `devices.yaml` rules are anchored to the repo root.
- Dockerfile `InvalidDefaultArgInFrom` warning — `BUILD_FROM` now has a default
  (overridden by the Supervisor / CI per arch).

## [0.1.0] - 2026-06-27

### Added
- Initial implementation of the local Home Connect to MQTT bridge:
  AES app-layer crypto transport, WebSocket protocol session/handshake,
  tolerant profile (DeviceDescription/FeatureMapping) parser, reconnect
  state machine, entity model, MQTT publish/command bridge, Home Assistant
  discovery, optional TLS-PSK transport (cgo `tlspsk` build), `hc-util` CLI
  and an optional status/health web UI.

### Quality
- All packages tested under `go test -race` (≈78% total statement coverage);
  `go vet`, `gofumpt` and the strict `golangci-lint` config pass with zero
  findings. Cross-compiles for linux/amd64+arm64 and darwin/arm64.
