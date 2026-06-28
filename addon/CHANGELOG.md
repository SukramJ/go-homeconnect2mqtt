# Changelog

All notable changes to this project are documented here. The format loosely
follows Keep a Changelog; versions track `internal/version/version.go`.

## [Unreleased]

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
