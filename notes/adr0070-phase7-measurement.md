# ADR 0070 phase 7 — measurement for go-homeconnect2mqtt

- Status: measurement (steps 0), plus the step 1+2 outcome and the F1/F10
  decisions — see [Step 2 outcome](#step-2-outcome--what-was-fixed-what-was-decided-what-stays)
- Date: 2026-09-13
- Subject: [ADR 0070](https://github.com/SukramJ/openccu-loom/blob/main/docs/adr/0070-shared-ha-discovery-model-module.md)
  and its rollout table, row *"7 | `go-homeconnect2mqtt` (716) | Proves the
  profile-derived `EntitySource`"*
  (`notes/concepts/shared-ha-discovery-model.md:869`)
- Measured against: this repository at `origin/main` (`99e3e59`),
  `github.com/SukramJ/go-hamqtt` v0.32.0 (`7efac2f`),
  `github.com/SukramJ/go-mqtt` v1.3.0 (consumed before this PR) / v1.5.1
  (required by go-hamqtt, and bumped to in this PR)
- Precedents: `go-zendure2mqtt`'s `docs/adr0070-pilot-measurement.md`
  (phase 5) and `go-mtec2mqtt`'s `notes/adr0070-phase6-measurement.md`
  (phase 6) with its findings F1–F13

This document measures what phase 7 costs, before any code moves. No
production Go file in this repository was modified by the measurement.
Defects found while reading are recorded in [Findings](#findings) and
were **not** fixed — the precedent from phases 5 and 6 is that defects
are corrected in their own step *before* the migration, so the later
byte-equality proof compares against corrected bytes.

Every count below is measured and the method is stated. Where the
"after" cannot be established without a live Home Assistant, the section
says so and names what would settle it.

> **Why `notes/`.** This repository does have a `docs/` tree, but it is a
> curated, numbered design series (`00-research.md` … `09-implementation-plan.md`
> plus `README.md` and `connecting-devices.md`) that `README.md` and
> `CLAUDE.md` both treat as shipped reference material, and that source
> comments cite by section (`// per docs/01-protocol.md §3.3`). A working
> measurement dropped into that sequence would read as document 10 of the
> design. So this follows phase 6's convention instead: `notes/`, a new
> directory whose only member is working material. Nothing in `notes/` is
> part of `RELEASE_PAYLOAD` (`Makefile:41`).

---

## 1. What is actually there

### 1.1 The whole repository

Measured on the worktree at `origin/main`, excluding the files this PR
adds:

```sh
find . -name '*.go' -not -name '*_test.go' | xargs cat | wc -l   # 8 403
find . -name '*_test.go'                   | xargs cat | wc -l   # 5 479
```

| | Lines |
| --- | ---: |
| Go, non-test | **8 403** |
| Go, test | **5 479** |
| `mapping.yaml` (the enrichment catalogue) | 2 794 (92 803 bytes), 679 features |
| `internal/i18n/catalog_gen.go` (the enum catalogue) | 743 |

Per package, `-maxdepth 1` per directory:

| Package | Non-test | Test |
| --- | ---: | ---: |
| `cmd/homeconnect2mqtt` | 222 | 123 |
| `cmd/hc-util` | 280 | 237 |
| **`internal/bridge`** | **1 117** | **1 682** |
| `internal/config` | 449 | 153 |
| **`internal/hass`** | **713** | **1 297** |
| `internal/homeconnect` | 2 618 | 1 799 |
| `internal/i18n` | 824 | 43 |
| `internal/mapping` | 124 | 69 |
| `internal/profile` | 1 304 | 883 |
| `internal/state` | 322 | 134 |
| `internal/version` | 28 | 0 |
| `internal/web` | 402 | 280 |

Two shapes worth naming before the numbers are used:

- **`internal/hass` is two files, not one.** `discovery.go` (292) owns
  the publish loop, the topic composition, the identity policy and the
  ownership test; `payload.go` (421) owns classification, the per-platform
  payload body, the slug and `humanize`. Unlike mtec (one file) the split
  matters: the migration replaces `payload.go` almost entirely and
  `discovery.go` only partly.
- **The MQTT plane is spread over four packages, not one.** mtec's
  correction from "the discovery package" to "the addressable surface"
  was 591 → 942 (1.6×). Here it is larger again, because this bridge has
  a per-device worker model with its own topic builder.

### 1.2 The 716 LOC figure

**716 is `internal/hass`, non-test, and it is now 713.** Measured
directly:

```sh
for c in $(git log --format=%h -10 -- internal/hass/); do
  echo "$c $(git show $c:internal/hass/discovery.go $c:internal/hass/payload.go | wc -l)"
done
```

| Commit | Lines | Subject |
| --- | ---: | --- |
| `99e3e59` (`origin/main`) | **713** | `fix(hass): stop publishing the dead object_id discovery key (#38)` |
| earlier | 716 | — |

So the figure is **right about what it covers and three lines stale**,
the staleness caused by the same commit class that made zendure's 375 and
mtec's 591 stale: dropping the `object_id` key, which `go-hamqtt`
removed by construction.

The real addressable surface, brace-matched function extents:

| In scope for the migration | File:lines | Lines |
| --- | --- | ---: |
| Publish loop, topic composition, identity, device block, ownership test, filters | `internal/hass/discovery.go` (whole file) | 292 |
| Classification, per-platform payload, slug, humanize, platform sanitiser | `internal/hass/payload.go` (whole file) | 421 |
| Device topic builder + state payload rendering | `internal/bridge/publish.go` (whole file) | 98 |
| Command subscribe + birth subscribe + inbound dispatch + entity resolve | `internal/bridge/command.go:24-124` | 101 |
| Discovery refresh, orphan reconcile, ownership filter | `internal/bridge/reconcile.go` (whole file) | 154 |
| Async publish drain (queue, overflow, run) | `internal/bridge/device.go:153-244` | 92 |
| State/availability/discovery publish + panic isolation | `internal/bridge/device.go:246-336` | 91 |
| MQTT bootstrap, will, lifecycle, breaker, status announce | `cmd/homeconnect2mqtt/main.go:93-152` | 61 |
| `mqttSession` (the hand-rolled `SplitClient`) | `main.go:181-189` | 9 |
| **Total addressable surface** | | **1 318** |
| Its tests | `internal/hass/*_test.go` (1 297) + the MQTT half of `internal/bridge/*_test.go` | ≥ 1 800 |

**1 318, not 716** — 1.8×, the same ratio as zendure's (672/375 = 1.8×)
and a little above mtec's (942/591 = 1.6×). The rollout table's figures
are consistently "the discovery package only" across all rows and should
be read that way.

### 1.3 Dependency state

`go.mod` before this PR:

```
require (
	github.com/coder/websocket v1.8.15
	golang.org/x/sync v0.23.0
	gopkg.in/yaml.v3 v3.0.1
)
require github.com/SukramJ/go-mqtt v1.3.0
```

**go-mqtt v1.3.0; go-hamqtt v0.32.0 requires go-mqtt v1.5.1.** A
two-minor gap, the same one mtec had:

- **v1.4.0** added `mqtt.SplitClient(p Publisher, s Subscriber) Client`.
  This repository hand-rolls it as `mqttSession`
  (`cmd/homeconnect2mqtt/main.go:181-189`) — nine lines including the
  compile-time contract assertion. It deletes itself on adoption, but
  **this PR does not delete it**: the brief for step 0 is to measure and
  change no behaviour, and a struct-embedding swap in the bootstrap is
  not a measurement. It is step 1 work.
- **v1.5.1** is required because through v1.5.0 an identifier-less
  delivery was matched by topic against stamped subscriptions, so a
  consumer's own broad subscription could run a handler twice per
  published message. **This bridge is the one most exposed to that
  defect of the three measured so far**, because its command
  subscription is not a narrow filter but the entire device sub-tree:
  `homeconnect/<device>/#` (`internal/bridge/command.go:27`). Its four
  filters are

  | Filter | Segments | Source |
  | --- | ---: | --- |
  | `homeconnect/<device>/#` | 2 + `#` | `command.go:27` |
  | `homeassistant/status` | 2 | `discovery.go:72` via `command.go:55` |
  | `homeassistant/+/+/+/config` | 5 | `discovery.go:274` (transient) |
  | `homeassistant/+/<device>/+/config` | 5 | `discovery.go:269` (transient) |

  The two transient config filters overlap each other exactly — the
  device-scoped one is a strict subset of the global one — but they are
  never installed at the same time (`refreshDiscoveryOnce` is a one-shot
  at startup, `reconcileOrphans` runs per device after it). The
  device sub-tree filter overlaps nothing else. So the bridge is
  probably not exposed in practice; the bump is required regardless, and
  **this PR does it.**

There is no `go-hamqtt` and no `go-ha-catalog` in `go.mod`, and no CI
preparation for either. `.github/workflows/dependabot-auto-merge.yml`
has no `go-ha-catalog` exclusion (mtec added one before it had the
dependency); that is phase-7 step 1 work, not step 0.

---

## 2. What it publishes

### 2.1 The shape

**Hand-built `map[string]any`, one retained config per entity**, built in
two places:

- `payloadFor` (`internal/hass/payload.go:230`) — one function with a
  `switch platform` rather than mtec's five per-platform builders — then
  `applyEnrichment` (`discovery.go:123`), `localizeOptions`
  (`discovery.go:155`) and `sanitizeForPlatform` (`payload.go:323`) in
  that order;
- `publishProgramControls` (`discovery.go:222`) — a **second, separate**
  builder for the two synthetic Start/Stop buttons, which shares none of
  the above. This is [F6](#f6).

Marshalled with `encoding/json` at `discovery.go:203` and `:252`; a
marshal error is logged and the entity skipped, never a panic (better
than mtec, which panics). The ADR's payload-style column is correct.

### 2.2 The entity census

Measured by driving the real builder over the pin catalogue (§5) —
the 679 feature names in the shipped `mapping.yaml` plus nine edge
entries — for a single appliance:

| Configuration | Entities | sensor | binary_sensor | select | button | switch | number |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `HASS_DISCOVERY: full`, `LANGUAGE: en` | **687** | 499 | 103 | 36 | 20 | 15 | 14 |
| `HASS_DISCOVERY: full`, `LANGUAGE: de` | **687** | 499 | 103 | 36 | 20 | 15 | 14 |
| `HASS_DISCOVERY: full`, no `mapping.yaml` | **687** | 499 | 103 | 36 | 20 | 15 | 14 |
| **`HASS_DISCOVERY: curated`** (the shipped default) | **176** | 93 | 43 | 17 | 6 | 6 | 11 |

Three things follow.

- **687 entities per appliance, 176 in the shipped default.** Both
  numbers matter: 176 is what a default install produces, 687 is the
  addressable surface the migration must reproduce, and an operator who
  sets `HASS_DISCOVERY: full` gets the larger one. This is an order of
  magnitude above mtec's 100 and zendure's, and **it is per device** —
  an installation with four appliances publishes four times this.
- **The platform mix is completely unlike the two earlier bridges.**
  mtec is 86 % sensor with one select; this is 73 % sensor with 36
  selects, 20 buttons and 103 binary sensors. Six platforms are live
  here against mtec's five, and `button` is new to the rollout.
- **The count does not move with the language.** 687 in both, and the
  identity strings are byte-identical across them (§3). Only `name` and
  a select's/enum-sensor's `options` are localised.

Both shipped languages, `de` (the default) and `en`, are pinned. The
`LANGUAGE` whitelist is exactly those two (`internal/config/validate.go:13`).

### 2.3 The topic form — **five segments, and this inverts the precedent**

```go
// internal/hass/discovery.go:167
func (d *Discovery) configTopic(platform, device string, e *homeconnect.Entity) string {
	return d.baseTopic + "/" + platform + "/" + sanitize(device) + "/" + featureKey(e) + "/config"
}
```

A measured topic, verbatim from the pin:

```
homeassistant/sensor/geschirrspuler/bsh_common_status_operationstate/config
```

**Five segments with a node id**, `<prefix>/<platform>/<node_id>/<object_id>/config`.
Both earlier bridges turned out to be four-segment and needed
`publisher.LegacyTopicByUniqueID`; **this one needs
`publisher.LegacyTopicWithNodeID`, which is the library default**, so
`SupersededTopics(prefix, bundle)` with no `forms` argument is already
correct here. That is the single fact step 6 of this phase turns on: get
it wrong and the superseded per-entity configs are never retracted, the
bundle is published alongside 687 still-retained per-entity configs, and
Home Assistant answers with one
`WARNING [mqtt.entity] Received a conflicting MQTT discovery message`
and no entities at all.

Verified against real published topics rather than read off the source:
`TestGoldenPinsTheTopicForm` splits all 687 topics the real builder
emitted and asserts the segment count, the prefix, the platform
vocabulary, the node id, and that
`unique_id == "homeconnect_" + node_id + "_" + object_id` for every one
of them. The synthetic buttons obey the same rule
(`homeassistant/button/geschirrspuler/start_program/config` ↔
`homeconnect_geschirrspuler_start_program`), even though they are built
by the other code path.

The transient collection filters agree with that shape:
`homeassistant/+/+/+/config` and `homeassistant/+/<slug>/+/config` are
both five-segment, so a four-segment foreign config is invisible to the
orphan sweep — correct, since this bridge never publishes one.

### 2.4 Two measured payloads, verbatim

From `internal/hass/testdata/discovery_full_de.json`, device
`Geschirrspüler`, `LANGUAGE: de`, `HASS_DISCOVERY: full`, enrichment on:

```json
{
  "topic": "homeassistant/sensor/geschirrspuler/bsh_common_status_operationstate/config",
  "qos": 1,
  "retain": true,
  "payload": {
    "availability_topic": "homeconnect/Geschirrspüler/availability",
    "default_entity_id": "sensor.geschirrspuler_bsh_common_status_operationstate",
    "device": {
      "identifiers": ["homeconnect_geschirrspuler"],
      "manufacturer": "BOSCH",
      "model": "SMV6ZCX49E",
      "name": "Geschirrspüler"
    },
    "device_class": "enum",
    "name": "Betriebszustand",
    "options": ["Auto", "Aus", "Ein"],
    "state_topic": "homeconnect/Geschirrspüler/BSH/Common/Status/OperationState/state",
    "unique_id": "homeconnect_geschirrspuler_bsh_common_status_operationstate"
  }
}
```

```json
{
  "topic": "homeassistant/button/geschirrspuler/start_program/config",
  "qos": 1,
  "retain": true,
  "payload": {
    "availability_topic": "homeconnect/Geschirrspüler/availability",
    "command_topic": "homeconnect/Geschirrspüler/_control/start_program/set",
    "default_entity_id": "button.geschirrspuler_start_program",
    "device": { "identifiers": ["homeconnect_geschirrspuler"], "manufacturer": "BOSCH",
                "model": "SMV6ZCX49E", "name": "Geschirrspüler" },
    "name": "Programm starten",
    "payload_press": "PRESS",
    "unique_id": "homeconnect_geschirrspuler_start_program"
  }
}
```

Note in the first payload that the node id is `geschirrspuler` while
every topic inside the payload spells the device `Geschirrspüler`. That
is [F2](#f2), and it is visible in a single payload once you look for it.

### 2.5 The whole topic tree

Pinned in `internal/bridge/testdata/topics.json`, one device:

| Plane | Count | Shape |
| --- | ---: | --- |
| Discovery config (retained) | 687 | `homeassistant/<platform>/<slug>/<key>/config` |
| Entity state | 687 | `homeconnect/<device>/<Dotted/Feature/Path>/state` |
| …of which no entity reads | 19 | command features, the raw program node, the protection port |
| Advertised state topics | 667 | the 687 minus the 20 buttons |
| Advertised command topics | 86 | `…/set`, plus the two `_control/<x>/set` |
| Device availability | 1 | `homeconnect/<device>/availability` |
| Device connection state | 1 | `homeconnect/<device>/connection_state` — read by no entity |
| Bridge status (+ Last Will) | 1 | `homeconnect/status` — read by no entity ([F1](#f1)) |
| Subscriptions | 4 | see §1.3 |

The feature path is the dotted feature name with `.` → `/`
(`BSH.Common.Status.OperationState` → `BSH/Common/Status/OperationState`),
not the slug — so the state tree preserves the original casing while the
discovery tree does not. Unnamed features fall back to `_uid/<n>` on the
topic and `uid_<n>` in the key.

### 2.6 Retain and QoS

| Publish | QoS | Retain | Site |
| --- | --- | --- | --- |
| Discovery config | `MQTT_QOS` (default **1**) | **true** | `discovery.go:210`, `:259` |
| Discovery retraction (empty payload) | `MQTT_QOS` | true | `reconcile.go:58`, `:126` |
| Entity state | `MQTT_QOS` | `MQTT_RETAIN` (default **true**) | `device.go:333` |
| Device availability / connection state | `MQTT_QOS` | `MQTT_RETAIN` | `device.go:333` |
| Bridge status `online`/`offline` | `MQTT_QOS` | true | `main.go:114`, `:137` |
| Bridge Last Will | **0** | true | `main.go:99-103` |
| Command subscribe | `MQTT_QOS` | — | `command.go:28`, `:55` |
| Config-collection subscribe | **0** | — | `reconcile.go:35`, `:99` |

**No retain defect here.** mtec's F3 — 96 state topics published
non-retained, so a restarting Home Assistant saw nothing — does not
apply: `RetainEnabled()` defaults to true (`internal/config/config.go:112`)
and there is no code path that publishes state non-retained by default.

Two QoS observations that do matter for the migration:

- **`MQTT_QOS` is validated to 0..1 and defaults to 1**
  (`internal/config/defaults.go:11`, `validate.go:49`). It is passed
  straight to `mqtt.QoS(cfg.MQTTQoS)`, where `0` means QoS 0. Under
  `go-hamqtt`, `publisher.QoS(0)` is **`QoSUnset`**, which resolves to
  **QoS 1**. An operator who set `MQTT_QOS: 0` deliberately would be
  silently upgraded on migration. The sentinel for a deliberate QoS 0 is
  `publisher.QoSAtMostOnce` (`0x80`), and the mapping must be explicit:
  `0 → QoSAtMostOnce`, `1 → QoSAtLeastOnce`. See [F9](#f9).
- **The Last Will is published at QoS 0 while the matching `online` is
  at `MQTT_QOS`.** `mqtt.Will` has no QoS field set at `main.go:99`, so
  it defaults to 0. Cosmetic today because nothing reads the topic
  ([F1](#f1)); it stops being cosmetic the moment F1 is fixed.

---

## 3. Identity — what the library can reproduce byte for byte

Home Assistant keys the entity registry on `(domain, platform, unique_id)`
and the device registry on `device.identifiers`, and **neither has a
migration path**. An identity string that cannot be reproduced exactly
orphans every entity in every installation.

### 3.1 The five strings

| String | Built by | Value |
| --- | --- | --- |
| `unique_id` | `payload.go:233` | `homeconnect_` + `slugify(device)` + `_` + `featureKey(e)` |
| `device.identifiers[0]` | `discovery.go:105` | `homeconnect_` + `slugify(device)` |
| node id (topic segment 3) | `discovery.go:168` | `slugify(device)` |
| object id (topic segment 4) | `payload.go:360` | `slugify(featureName)`, or `uid_<n>` when unnamed |
| `default_entity_id` | `payload.go:235` | `<platform>.` + `slugify(device + "_" + featureKey(e))` |

Everything is a function of exactly two inputs: the operator's device
name from `devices.yaml`, and the feature name from the appliance
profile. Both go through the same `slugify`
(`payload.go:396`; `sanitize` at `:413` is an alias).

`device.identifiers` carries **no serial and no `haId`** — only the
operator-chosen name. Two consequences, both measured:

- Renaming a device in `devices.yaml` re-keys the device registry and
  all 687 entities. There is no stability anchor.
- Two daemon instances with different `MQTT_TOPIC` roots but a
  same-named device produce **identical** `unique_id`s and **identical**
  config topics. See [F8](#f8).

### 3.2 Slug agreement — measured over the real catalogue

`TestSlugAgreesWithLibrarySlug` re-slugs every seed under this bridge's
`slugify` and under a verbatim transcription of go-hamqtt v0.32.0's
`topic.Slug` (`topic/topic.go:146-185`) and diffs:

| Seed set | Seeds | Diverged |
| --- | ---: | ---: |
| Every named feature in `mapping.yaml` + the pin edges | 686 | **0** |
| Device-name probes | 7 | **5** |

**Zero of 686 feature names change.** Every `unique_id` suffix, every
object id, every `default_entity_id` tail is reproducible byte for byte
by `topic.Slug` — because Home Connect feature names are pure ASCII
`[A-Za-z0-9.]`, where the two slugs cannot disagree. This is a much
better result than mtec's (8 of 100 changed, plus an empty-slug case
that changed all 100, which is why swapping the slug was refused there).

The divergence is entirely in the **device name**, and it is total when
it happens:

| Device name | This bridge | `topic.Slug` |
| --- | --- | --- |
| `Geschirrspüler` | `geschirrspuler` | `geschirrspueler` |
| `Küche` | `kuche` | `kueche` |
| `Café` | `caf` | `cafe` |
| `ÜÄÖ` | `uao` | `ueaeoe` |
| `""` | `""` | `x` |
| `Dishwasher` | `dishwasher` | `dishwasher` |
| `Waschmaschine / Trockner` | `waschmaschine_trockner` | `waschmaschine_trockner` |

This bridge's `umlautReplacer` (`payload.go:392`) folds `ä ö ü` to
`a o u`; the library expands them to `ae oe ue`, which is what Home
Assistant's own `slugify` does. The library is **right** and this bridge
is **wrong** — and it does not matter, because correctness is not the
question: the question is whether the installed base's registry keys can
be reproduced, and for any German device name they cannot.

`LANGUAGE` defaults to `de` (`internal/config/defaults.go:23`), so a
German device name is the expected case, not an edge case.

### 3.3 What this means for the phase

**Reproducible:** `unique_id`'s feature half, object id,
`default_entity_id`'s feature half, every state topic, every command
topic, the availability topics, the whole `device` block's non-identity
fields.

**Not reproducible with `topic.Slug`:** the node id,
`device.identifiers[0]`, and the device half of `unique_id` and
`default_entity_id` — for any device whose name contains a character
outside `[A-Za-z0-9]` that the two slugs transliterate differently.
That is [F10](#f10) and it **gates the migration**, not a detail of it.

The resolution is not in doubt — the library's `discovery.Context` is an
interface (`discovery/render.go:20`) with `NodeID`, `ObjectID` and
`UniqueID` methods, and `StdContext` is one implementation among
possible others. A consumer-supplied `Context` that calls this bridge's
own `slugify` reproduces every string exactly, at the cost of not
adopting `topic.Slug`. That is the same answer phase 6 reached, and it
should be taken deliberately rather than discovered at step 6.

What I could **not** determine: whether any deployed installation
actually has a non-ASCII device name. `devices.yaml` is operator-written
and nothing is collected. The measurement assumes at least one does,
because the default language is German and the template file
(`devices-template.yaml`) does not steer operators towards ASCII.

---

## 4. The availability model

**Device-level only, single topic, no `availability_mode`.**

Every one of the 687 payloads carries exactly
`"availability_topic": "homeconnect/<device>/availability"`
(`payload.go:236`, `discovery.go:249`) and nothing else: no
`availability` list, no `availability_mode`, no `payload_available` /
`payload_not_available` (it relies on Home Assistant's `online`/`offline`
defaults, which match what `device.go:293-297` writes). Asserted for all
687 by `TestGoldenPinsTheBridgeAvailabilityGap`.

The daemon **does** publish a bridge-level topic — `homeconnect/status`,
retained, with the Last Will attached (`main.go:93-137`) — and **no
entity references it**. That is [F1](#f1), and it is the exact inverse
of mtec's shape: mtec was bridge-only and needed `model.BridgeOnly()`
over the library's `{LevelBridge, LevelDevice}` default; this bridge is
device-only, so the library's **default is the fix**, not the trap.

Concretely, with go-hamqtt v0.32.0:

- `model.Availability{}.Resolved()` returns `{LevelBridge, LevelDevice}`
  and mode `all` (`model/description.go:107`).
- `LevelBridge`'s topic comes from `topic.Layout.Bridge()`;
  `topic.Default.Bridge()` is `<root>/bridge/status`
  (`topic/topic.go:86`), **not** `homeconnect/status`. So a custom
  `Layout` is required regardless of the availability decision.
- `LevelDevice`'s topic comes from `Layout.Availability(slot)`, which
  must be made to yield `<root>/<raw device name>/availability`.

So the availability work in this phase is: write the `Layout`, and then
decide separately whether to keep device-only (byte-equal, F1 stays) or
take the default (two levels, F1 fixed, every payload changes). Those
must not be the same step — one is a migration, the other is a
behaviour change to an installed base.

---

## 5. The pins

Before this PR, nothing pinned the published surface. Measured:
`find . -name testdata` returned nothing, and
`grep -rn golden --include='*.go' .` returned nothing. The 1 297 lines
of `internal/hass` tests are real and good, but they assert a handful of
keys on a handful of hand-built entities; `device.identifiers` was
asserted nowhere, QoS was discarded by every stub, and the two
state-topic builders had never been compared.

This PR adds three things.

### 5.1 `internal/pincatalog` — the fixture

A non-test package (so both pin packages can use it) that builds the
catalogue the pins are measured against: **one `profile.Entry` per
feature name in the shipped `mapping.yaml`** — 679 of them, real names,
generated to mirror the official Home Assistant `home_connect`
integration — plus nine explicitly-constructed edge entries (unnamed
feature, raw program node, protection port, read-only selected program,
a selected program with no programs, a writable boolean, a writable
number, a counter, an unavailable element).

What is synthesised is the per-feature **wire descriptor** — kind,
access, protocol type, content type, enumeration — because an
appliance's `DeviceDescription` XML is device-bound and not shipped with
this repository. The synthesis is deterministic and derived only from
the feature name and `mapping.yaml`'s own hints: kind from the third
dotted segment (which is how a real description groups the same features
into its `statusList`/`settingList`/`eventList`/`commandList`), access
from the kind, type from the `unit`/`device_class` hints and otherwise
from the leaf name's shape. It is documented in the package comment.

**The fixture is an input, not a stub.** The entries go through the
production `profile.Entry → homeconnect.NewAppliance → Entity` chain and
then through `hass.Discovery.PublishDevice` unmodified.

### 5.2 `internal/hass/golden_test.go` + `testdata/`

One flag, `-update-discovery-golden`. Five files:

| File | Rows | Bytes |
| --- | ---: | ---: |
| `discovery_full_en.json` | 687 | 570 822 |
| `discovery_full_de.json` | 687 | 593 523 |
| `discovery_curated_de.json` | 176 | 152 901 |
| `discovery_plain_en.json` (no `mapping.yaml`) | 687 | 574 242 |
| `identity_en.json` | 687 | 235 346 |

Each row is `{topic, qos, retain, payload}` — **topic and payload stored
together**, payload stored **decoded** so a reviewer reads JSON in the
diff rather than an escaped blob, and compared on a **canonical
re-encoding of both sides** so a reformatting can never look like a
payload change. Identity is a separate file so an identity change and a
payload change produce two different diffs.

The test **never reads back the file it just regenerated**: under the
flag `readOrUpdateGolden` writes and returns `(nil, false)`, and the
caller returns. And the assertions that matter most hold their expected
values as **literals in Go** and never consult `testdata` at all —
the device block, the topic form, the availability model, the platform
census, the duplicate-`unique_id` count, the slug divergence. The
cross-language identity check compares `en` against `de` directly; the
file participates once.

2.1 MB of testdata is a lot. It is 687 entities × four configurations,
and the alternative — pinning a subset — pins nothing about the 500
sensors nobody looks at, which is exactly where an identity regression
would hide.

### 5.3 `internal/bridge/topics_golden_test.go` + `testdata/topics.json`

One flag, `-update-topics-golden`. One file (124 KB) holding the state
tree, the advertised state and command topics, the orphan state topics,
the availability topics no entity reads, the subscribe filters **with
their QoS**, and the delivery guarantee of every publish plane.

Its most important test is not the golden. `TestStateTopicBuildersAgree`
compares **builder against builder** — `hass.topicsFor` against
`bridge.deviceTopics.state` — and never consults the file, so it fails
even immediately after a regeneration. That is [F3](#f3).

### 5.4 Mutation proof

Every pin was perturbed and watched to fail. Fifteen mutations, all
caught, all reverted:

| # | Mutation | Caught by |
| --- | --- | --- |
| M1 | stray `platform` key in `payloadFor` | `TestDiscoveryGolden` (all four files) |
| M2 | `homeconnect_` → `hc_` in the device id | golden + identity + device-block + topic-form |
| M3 | node id / object id swapped in `configTopic` | golden + 4 pre-existing tests |
| M4 | `hass` state-topic suffix `/state` → `/value` | golden + `TestTopicGolden` (`advertised_state_topics`) |
| M4b | same, isolated | `TestTopicGolden` names all 667 |
| M5 | `bridge` state-topic suffix `/state` → `/value` | `TestTopicGolden` (`state_topics`) + 3 pre-existing |
| M6 | discovery `retain` true → false | `TestDiscoveryGolden` |
| M7 | `slugify` transliterates `ü` → `ue` | golden + device-block + F2 pin + `TestDeviceConfigFilter` |
| M8 | drop `enabled_by_default` | golden + `TestCuratedModeSkipsDisabled` |
| M9 | button availability → bridge topic | golden + F1 pin + F2 pin |
| M10 | command filter narrowed to `/+/+/set` | `TestTopicGolden` + F4 pin + command-coverage |
| M11 | drop the two synthetic program buttons | golden + identity + F6 pin + `TestTopicGolden` |
| M12 | `hass` unnamed path `_uid/` → `uid/` | **`TestStateTopicBuildersAgree`** + golden |
| M13 | `bridge` unnamed path `_uid/` → `uid/` | **`TestStateTopicBuildersAgree`** + golden |
| M14 | `DeviceConfigFilter` stops sanitising | `TestTopicGolden` (`subscribe_filters`) |

M12 and M13 are the ones worth noting: each changes **one** of the two
state-topic builders, which is invisible to a golden regeneration and is
precisely the failure mode F3 describes. Both are caught by the
builder-against-builder test, not by the file.

---

## Findings

Ranked by severity. None was fixed in the measurement PR (#40). Their
status after steps 1+2 is in
[Step 2 outcome](#step-2-outcome--what-was-fixed-what-was-decided-what-stays).

<a name="f1"></a>
### F1 — the Last Will is attached to a topic no entity reads · **high**

`cmd/homeconnect2mqtt/main.go:93-103` sets the client's Will to
`<MQTT_TOPIC>/status` = `offline`, retained, and `OnConnect` publishes
`online` to the same topic. **No discovery payload references it**: all
687 declare `homeconnect/<device>/availability` and nothing else
(`payload.go:236`, `discovery.go:249`).

The device availability topic is only ever written by the daemon itself
(`device.go:290-298`). So when the daemon is killed, crashes, or loses
the broker without a clean shutdown, the broker delivers the Last Will
to a topic that no entity is bound to, and all 687 entities stay
`available` in Home Assistant, showing their last retained value
indefinitely. This is the same defect ADR 0070's own survey already
noted for this bridge
(`notes/concepts/shared-ha-discovery-model.md:115`).

Pinned as current behaviour by `TestGoldenPinsTheBridgeAvailabilityGap`.
Fix: a second availability entry at bridge level, `availability_mode: all`
— which is the library's default, so the fix and the migration point the
same way. It changes all 687 payloads and must be its own step.

<a name="f10"></a>
### F10 — the device slug is not reproducible by `topic.Slug` · **high (gates the phase)**

§3.2. Zero of 686 feature seeds diverge; five of seven device-name
probes do, including the two most likely real ones (`Geschirrspüler`,
`Küche`). Because the device slug is embedded in the node id, in
`device.identifiers[0]`, and in the device half of both `unique_id` and
`default_entity_id`, a divergence re-keys **the device and all 687 of
its entities at once**, with no migration path in Home Assistant.

This is not a defect in this bridge — the library's slug is the more
correct one — but it is the constraint the phase has to be designed
around. Either the migration supplies its own `discovery.Context` that
keeps `slugify`, or it accepts a one-time re-registration for every
non-ASCII device name in the field. The decision belongs before step 3,
not inside it.

<a name="f3"></a>
### F3 — two independent state-topic builders that nothing compared · **high**

The state topic is composed twice, in two packages, from two separate
`featurePath` implementations:

```go
// internal/hass/discovery.go:87   -> goes into the retained config as state_topic
func featurePath(e *homeconnect.Entity) string
// internal/bridge/publish.go:48   -> where the value is actually published
func featurePath(name string, uid int) string
```

They agree today — measured, 667 of 667 — but nothing enforced it. A
change to either alone leaves every entity pointing at a topic nobody
writes: permanently `unknown`, nothing in the log, nothing in Home
Assistant's registry to notice. The same shape as mtec's F5.

Now pinned builder-against-builder by `TestStateTopicBuildersAgree`.
Fix (a later step): one builder, or one of them delegating to the other.

<a name="f2"></a>
### F2 — the config topic sanitises the device name; every other topic does not · **medium**

`configTopic` (`discovery.go:168`) and `DeviceConfigFilter`
(`discovery.go:269`) use `sanitize(device)`. `topicsFor`
(`discovery.go:94`), `publishProgramControls` (`discovery.go:233`),
`newDeviceTopics` (`publish.go:37`) and the command subscription
(`command.go:27`) use the **raw** device name. For `Geschirrspüler` the
daemon therefore publishes

```
homeassistant/sensor/geschirrspuler/…/config       # sanitised
homeconnect/Geschirrspüler/BSH/Common/…/state      # raw
homeconnect/Geschirrspüler/#                       # raw, the subscription
```

Non-ASCII in an MQTT topic is legal (UTF-8 topic names, §1.5.4), so this
works. But it is a trap: a device name containing `/`, `+` or `#` would
produce a malformed topic or a subscription that swallows a sibling's
tree, and nothing validates the name. `isTopicSafe`-style validation
exists in the sister project; here there is none.

Pinned by `TestGoldenPinsTheSanitizedNodeIDAsymmetry`. The migration
forces a decision here anyway, because `topic.Layout` takes a
`model.Slot` and the same scope segment feeds both trees.

<a name="f9"></a>
### F9 — `MQTT_QOS: 0` becomes QoS 1 on migration · **medium**

§2.6. `mqtt.QoS(0)` is QoS 0; `publisher.QoS(0)` is `QoSUnset` and
resolves to QoS 1. An operator who deliberately set `MQTT_QOS: 0` gets a
silent upgrade. The mapping must be written explicitly
(`0 → publisher.QoSAtMostOnce`, `1 → publisher.QoSAtLeastOnce`) and
tested. Pinned today by `publish_qos_retain` in `topics.json` and by the
`qos` field on all 687 golden rows.

<a name="f4"></a>
### F4 — the command subscription swallows the daemon's own state tree · **medium**

`command.go:27` subscribes `<root>/<device>/#`, which matches all 687 of
the daemon's own state topics plus its availability and connection-state
topics — 689 of its own publications, measured. The broker echoes every
one of them back. They are dropped, but only by the retained-bit check
at `command.go:29`, and only *after* the broker has delivered them: with
`MQTT_RETAIN: false` the check no longer fires and **every state publish
spawns a goroutine that immediately returns** at `command.go:78`.

At QoS 1 with 687 entities per device this is also a real wire cost on
every reconnect: the broker replays the whole retained state tree to the
daemon that wrote it.

Pinned by `TestCommandFilterSwallowsTheDaemonsOwnStateTree`. Fix: a
narrow filter (`<root>/<device>/+/+/+/set` does not fit the variable-depth
feature path, so `<root>/<device>/#` with a `/set` suffix check *before*
the goroutine, or MQTT 5 No-Local, which go-mqtt exposes and
`publisher.CommandRouter` uses).

<a name="f8"></a>
### F8 — two instances collide on identity when a device name repeats · **medium**

`unique_id` and the config topic depend only on `HASS_BASE_TOPIC` and the
device name; `MQTT_TOPIC` appears in neither
(`discovery.go:105`, `:168`, `payload.go:233`). Two daemons on one
broker with different roots but a same-named appliance publish the same
687 config topics and the same 687 `unique_id`s, overwriting each other.
`IsOwnConfig` (`discovery.go:282`) does check the state-topic prefix, so
the orphan sweep will not retract the other instance's configs — but the
retained config itself is last-writer-wins, and Home Assistant sees one
entity whose `state_topic` flips between the two roots.

<a name="f6"></a>
### F6 — the two synthetic buttons are built by a second, divergent code path · **medium**

`publishProgramControls` (`discovery.go:222`) builds its payloads
directly and shares nothing with `payloadFor`. Measured differences
against the 18 command-derived buttons:

| | derived (18) | synthetic (2) |
| --- | --- | --- |
| `entity_category` | `config` | *absent* |
| `enabled_by_default` | `false` | *absent* (so: enabled) |
| `payload_press` | `"true"` | `"PRESS"` |
| enrichment / exclusion | applied | not applied |
| `curated` filter | applies | does not apply |

The `payload_press` difference is correct (the synthetic control is
handled by `handleProgramControl`, not written to a feature). The other
three are drift: the two synthetic buttons are the only always-enabled,
uncategorised, un-excludable, un-curatable entities this bridge
publishes. This is mtec's F7 in a different shape.

Pinned by `TestGoldenPinsTheProgramButtonInconsistency`.

<a name="f5"></a>
### F5 — a selected-program select can be published with an empty options list · **low**

`classify` (`payload.go:87`) routes a **writable** `selectedProgram`
element to `select` on its kind alone, without asking whether it has any
programs. The parser normally folds the `programGroup` into the
enumeration (`internal/profile/parser.go:334-337`), but when the appliance
exposes no programs there is nothing to fold, and the payload gets
`"options": []` — a dropdown with nothing in it, visible, writable and
impossible to set.

Pinned as current behaviour by `TestGoldenPinsTheEmptyOptionsSelect`,
which also asserts that **every** select carries an `options` key at all.

<a name="f7"></a>
### F7 — 19 state topics no entity reads, and one availability topic nobody reads · **low**

Measured from `topics.json`: the device worker publishes state for all
688 entities, including the 18 command features (write-only buttons, no
`state_topic`), the raw program node and the protection port — 19
distinct topics with no reader. Plus
`homeconnect/<device>/connection_state`, which is published retained and
referenced by no discovery payload; it feeds only the web UI, which
reads it from the in-process `state.Store` rather than from MQTT.

Harmless, but it is retained broker state that nothing ever retracts,
and it widens the `#` subscription of [F4](#f4).

<a name="f11"></a>
### F11 — localised `options` are sorted in English · **low**

`enumOptions` (`payload.go:350`) sorts the enum member names, and
`localizeOptions` (`discovery.go:155`) translates them afterwards. The
German dropdown for `OperationState` therefore reads
`["Auto", "Aus", "Ein"]` — sorted by `Auto/Off/On`, not alphabetically
in German. Cosmetic, visible in every enum entity, and pinned.

<a name="f12"></a>
### F12 — changing `LANGUAGE` strands retained enum state · **low**

The published state payload for an enum is localised
(`bridge/publish.go:80`), and so is the `options` list. Switching
`LANGUAGE` republishes discovery with the new `options` but leaves the
retained *state* in the old language until the feature next changes, so
the entity briefly holds a value that is not one of its own options.
`HASS_DISCOVERY_REFRESH` does not help — it clears configs, not state.

---

## Step 2 outcome — what was fixed, what was decided, what stays

This section was written by phase 7 steps 1+2 (the PR that fixes the
defects). The measurement above is unchanged except where it was measured
wrong; those corrections are marked.

### Decisions taken in writing

The sequencing table below puts F10 and F1 at step 3, "decided, not
discovered". Both are decided here, one step early, because step 2 had to
touch the code they govern and a fix that contradicts a later decision is
worse than an early decision.

**F10 — the slug: keep this bridge's `slugify`.** Decided. The migration
will supply its own `discovery.Context` (and its own `topic.Layout`)
calling this bridge's `slugify`, exactly as phases 5 and 6 did; it will
not adopt `topic.Slug`.

The reasoning, once, so step 4 does not reopen it:

- The question is not which slug is correct. `topic.Slug` **is** the more
  correct one — it expands `ä ö ü` to `ae oe ue` the way Home Assistant's
  own `slugify` does, and this bridge folds them to `a o u`. The question
  is whether the installed base's registry keys can be reproduced, and
  for any German device name they cannot (§3.2: 5 of 7 device-name probes
  diverge, and `LANGUAGE` defaults to `de`).
- The device slug is embedded in the node id, in `device.identifiers[0]`,
  and in the device half of both `unique_id` and `default_entity_id`.
  Home Assistant keys the entity registry on `(domain, platform,
  unique_id)` and the device registry on `identifiers`, and **neither has
  a migration path**. A divergence re-keys the device and all 687 of its
  entities at once — every automation, dashboard card, history series and
  area assignment that names them breaks, silently, with the old entities
  left behind as unavailable orphans.
- The win from adopting `topic.Slug` is zero on the feature half: 0 of
  686 feature seeds diverge, because Home Connect feature names are pure
  ASCII. So the entire cost buys nothing but consistency with a sibling
  module's transliteration.
- **Do not "fix" the transliteration either.** `Geschirrspüler` →
  `geschirrspuler` is wrong and stays. An umlaut in a device name is not
  a reason to break someone's entity and device registry.
- The `""` → `""` row of §3.2 is unreachable in production: `LoadDevices`
  rejects an empty device name, and since F2 it also rejects a name with
  no ASCII letter or digit, which is the only other way to reach an empty
  slug.

**F1 — availability: take both levels.** Decided and implemented in this
step, deliberately not deferred to the migration. The measurement's own
advice was "do not fix F1 in the same step as the migration"; step 2 is
that separate step.

- Every payload declares `{bridge, device}` with `availability_mode: all`
  — which is `go-hamqtt`'s `model.Availability{}.Resolved()` default, so
  the migration reproduces this shape rather than changing it.
- Mode `all` is safe here **because both sources are genuinely
  published** — the bridge topic by the will, the birth and the shutdown
  path, the device topic by the device worker. That is the trap
  go-mtec2mqtt had to avoid in the opposite direction: a declared source
  nobody publishes is not neutral under `all`, it is a permanently
  unavailable entity.
- **The topic does not move.** `<MQTT_TOPIC>/status` is already in the
  daemon's own publish root. `topic.Default.Bridge()` would render
  `<root>/bridge/status`, but a custom `Layout` is required regardless
  (§4: `Availability(slot)` must yield `<root>/<raw device name>/availability`,
  which no default renders), so matching the library default buys one
  line and costs an operator-visible topic move plus a retraction.
- Consequently there is **no retained copy to retract** and no operator
  step. An automation watching `<MQTT_TOPIC>/status` keeps working.

### Fixed in step 2

| Finding | Fix | Bytes moved |
| --- | --- | --- |
| **F1** | both availability levels, mode `all`, attached in the one funnel both payload builders pass through; will QoS matched to the birth publish | all 1 728 payload rows: `availability_topic` → `availability` + `availability_mode`. Identity untouched |
| **F3** | new `internal/topic` package; every topic composed once | none |
| **F4** | `mqtt.WithNoLocal()` + a `Relative()` check before dispatch | `topics.json`: one filter gains `options` |
| **F6** | both button builders share `basePayload` and `sanitizeForPlatform` | none |
| **F2** (the defect half) | `validateDeviceName` rejects `+`, `#`, control characters, invalid UTF-8 and a name that slugifies to empty | none |
| **F5** | a writable selected-program with no programs is a sensor, not an empty select | 1 row per file, `select/` → `sensor/` |
| **F9** | pinned at both translation points, off the transport call | none |
| **F11** | localised options sorted in the display language | `options` on 91 + 19 German rows |

### Deliberately not fixed

**F7 — 19 unread state topics, and `connection_state`.** Left as
published. Removing topics an installed base may already consume is a
decision about the operator contract, not a defect fix, and it does not
belong inside a migration programme — the same call go-mtec2mqtt made
twice for its equivalent (its F10, five unread topics, left at step 1 and
again at step 2). The 19 are not noise either: the command features, the
raw program node and the protection port are all observable state that a
user's own automation may read even though no entity does, and
`connection_state` is the only MQTT-visible signal distinguishing "the
appliance is unreachable" from "the daemon is down". Since F1 the
availability half of the finding is smaller:
`availability_topics_no_entity_reads` is now `connection_state` alone.
Still pinned in `topics.json` as `state_topics_without_entity`.

**F12 — changing `LANGUAGE` strands retained enum state.** Left. The fix
is not cheap: the daemon keeps no state across restarts, so it cannot
know the language changed; making it know means persisting the last
language somewhere, and republishing every entity's state on a mismatch —
new persistent state and a new startup side effect, to correct a value
that self-corrects the next time the feature changes. `LANGUAGE` is
changed approximately once per installation. Not worth new machinery
inside a migration programme.

**F8 — two instances collide on identity.** Not fixed, by the same
reasoning go-mtec2mqtt applied to its duplicate `unique_id`: the stakes
change at step 6, and the decision belongs to the step that publishes
bundles. See the next section.

**F2's asymmetry itself.** The raw/slugified split stays, pinned by
`TestGoldenPinsTheSanitizedNodeIDAsymmetry`. Both forms are
load-bearing — the slug is half of every `unique_id` and of
`device.identifiers`, the raw name is in every topic an installed base
subscribes to — so neither can move without re-keying a registry or
orphaning retained state.

**F6's un-excludable synthetic buttons.** The two program buttons remain
outside the operator catalogue's reach. Enrichment and exclusion are
keyed on a feature name and these back no feature; there is nothing to
match. That is a gap in the catalogue's vocabulary, not a divergence
between two builders.

### F8 — what step 6 needs to know

Measured precisely, so the bundle step does not have to re-derive it.

**What collides.** `unique_id`, `device.identifiers[0]`, the discovery
config topic's node id and `default_entity_id` are all functions of
`HASS_BASE_TOPIC` and the **device name** only. `MQTT_TOPIC` appears in
none of them (`discovery.go` `deviceBlockFor`/`configTopic`,
`payload.go` `basePayload`).

**Under which configuration.** Two daemon instances on one broker, with
**different `MQTT_TOPIC` roots** (so their state trees do not collide and
neither notices the other), the **same `HASS_BASE_TOPIC`** (the default,
`homeassistant`), and **any device name in common** — including two
physically different appliances an operator happened to call
`Geschirrspüler`. Same `MQTT_TOPIC` is not required and does not make it
worse. A single instance cannot collide with itself: `LoadDevices`
rejects a duplicate device name.

**What happens today.** Both instances publish the same 687 retained
config topics with the same 687 `unique_id`s. Last writer wins per topic.
Home Assistant sees one set of entities whose `state_topic` flips between
the two roots, so the entity shows whichever instance published its
config most recently and goes stale when that instance's appliance does.
`IsOwnConfig` checks the state-topic prefix, so the orphan sweep does
**not** retract the other instance's configs — the two do not fight, they
overwrite.

**Why the stakes change at step 6.** Today the damage is spread over 687
independent retained topics, each individually last-writer-wins. A device
bundle is **one** retained topic per device carrying all 687 components.
Two instances then alternate publishing a single document, and each
publish replaces the other's entire entity set rather than one entity of
it — and `SupersededTopics` retraction runs against a per-entity form
both instances also own. The failure goes from "some entities point at
the wrong root" to "the whole device flips".

**What step 6 must decide.** Whether the identity namespace gains a
discriminator, and if so which. The candidates, with their costs:

- `MQTT_TOPIC` in `unique_id` and `identifiers` — correct, and it
  re-keys every entity in every existing installation (the default root
  is `homeconnect`, so it is not even a no-op for defaults unless the
  discriminator is omitted when the root is the default, which is a rule
  nobody will remember).
- An `INSTANCE_ID` config key, empty by default — additive, no default
  installation changes, and it only helps operators who know to set it.
- Leave it. Two instances with a shared device name on one broker is
  plausible but not common, and the collision is at least deterministic.

Nothing here is decided. It is measured so the step that publishes
bundles can decide it in one sitting.

### Corrections to the measurement above

- §2.2's census is one off in the curated row: the builder publishes
  **177** entities in `HASS_DISCOVERY: curated`, not 176. The
  `TestGoldenPlatformCensus` doc comment claimed to pin the per-platform
  counts as literals; its body only logged them. It does now, which is
  how the discrepancy surfaced. (The full-set 499/36 sensor/select split
  the document reports is right again after F5, coincidentally: it was
  498/37 before.)
- §2.5's "Advertised command topics | 86" is 85 after F5.

---

## Sequencing — the rest of phase 7

Ordered so each step de-risks the next, following the shape phases 5 and
6 converged on.

| Step | Work | Why here |
| ---: | --- | --- |
| **0** | **This PR.** Bump `go-mqtt` v1.3.0 → v1.5.1; add `internal/pincatalog`; pin all 687 payloads × four configurations, the identity plane, and the whole topic tree; measure and record F1–F12. | Nothing can be proved byte-equal against a surface that was never captured. |
| 1 | Housekeeping the bump enables: delete `mqttSession` for `mqtt.SplitClient`; exclude `go-ha-catalog` from Dependabot auto-merge. Payloads untouched — the goldens must not move. | Small, mechanical, and a free check that the pins do not fire on a non-payload change. |
| 2 | **Fix the defects, one commit per finding, goldens regenerated with the diff reviewed.** F3 (one state-topic builder) and F4 (`/set` check before the goroutine) first — neither changes a byte on the wire. Then F6, F5, F11, F7. | The byte-equality proof in step 4 must compare against *corrected* bytes, not against bugs. |
| 3 | **Decide the two questions that are not implementation.** (a) F10: consumer-supplied `discovery.Context` keeping `slugify`, or accept re-registration for non-ASCII device names. (b) F1/availability: keep device-only and stay byte-equal, or take the library's `{LevelBridge, LevelDevice}` default and change all 687 payloads. Written down, not discovered. | Both are irreversible for an installed base. Phase 6 hit (a) at step 3 and paid for it. |
| 4 | **Render through go-hamqtt, publishing nothing.** Add `internal/hass/hamqtt.go` building a `discovery.Bundle` from the same entities, behind no config flag, and a test that compares its per-component output against `testdata/discovery_*.json` — **against the files, not against the builder it replaces**. Neither pin regenerated. | This is where a `Layout`, `Context` or `Slot` mismatch surfaces, at zero risk. |
| 5 | Adopt the library on the **state and command** planes (`publisher.StatePublisher`, `publisher.CommandRouter`, `publisher.AvailabilityPublisher`), discovery still per-entity from the old path. | The state plane has no registry keys to orphan; it is the cheap half. |
| 6 | **Switch discovery to the device bundle.** `publisher.PublishBundle` + `SupersededTopics(prefix, bundle)` with the **default** `LegacyTopicWithNodeID` form (§2.3), retracting all 687 per-entity configs before the bundle lands. Verify against a live Home Assistant that no `Received a conflicting MQTT discovery message` warning appears. | The one step that cannot be proved by a unit test. Everything above exists to make it a small diff. |
| 7 | Apply the step-3 decisions, changelog + `addon/CHANGELOG.md`, version bump across the five spots `CLAUDE.md` names. | Operator-visible last. |

### What I would not do

- **Do not swap `slugify` for `topic.Slug` as a convenience.** Zero
  feature names change and every non-ASCII device name does; the win is
  cosmetic and the cost is the whole device registry.
- **Do not fix F1 in the same step as the migration.** It changes all
  687 payloads. A migration step whose golden diff is empty is provable;
  one whose diff is 687 rows is not.
- **Do not shrink the pins.** The 500 sensors nobody looks at are
  exactly where an identity regression hides.
- **Do not regenerate a golden to make a step pass.** Every regeneration
  in steps 2 and 7 must be accompanied by a reviewed diff; steps 1, 4, 5
  and 6 must regenerate nothing.

---

## Appendix — how the measurement was taken

Everything in this document was produced by the two pin test files it
describes, run in a worktree of `origin/main` with `go test`. There was
no throwaway scratch copy and no transcription of production code into a
test, with one exception: `libraryStyleSlug` in
`internal/hass/golden_test.go` is a verbatim transcription of
go-hamqtt v0.32.0's `topic.Slug`, kept there so §3.2 can be counted
without taking a dependency on the library one step early. It is
unreachable from any production path and deletes itself at step 4.

The entity census, the platform mix, the slug divergence, the duplicate
count, the availability shape and the topic form are all `t.Logf` output
of tests that also assert them:

```sh
go test ./internal/hass  -run 'TestGolden|TestSlug|TestIdentity' -v
go test ./internal/bridge -run 'TestTopicGolden|TestStateTopicBuilders|TestCommand' -v
```

Duplicate `unique_id`s: **zero**. `TestGoldenPinsDuplicateUniqueIDs`
measured 687 distinct ids across 687 config topics, none published
twice. mtec publishes nine twice, under two platforms each; that is now
known to be legal (Home Assistant keys the entity registry on
`(domain, platform, unique_id)` — `entity_registry.py`'s `_index` — and
go-hamqtt v0.32.0 narrowed its validator to match), but the question does
not arise here. The test still asserts that no `unique_id` is published
twice **on the same platform**, which would silently drop one of the two.
