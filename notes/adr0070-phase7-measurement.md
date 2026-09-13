# ADR 0070 phase 7 — measurement for go-homeconnect2mqtt

- Status: measurement (step 0), plus the step 1+2 outcome and the F1/F10
  decisions — see [Step 2 outcome](#step-2-outcome--what-was-fixed-what-was-decided-what-stays)
  — plus the step 4 byte-equality result and F13, see
  [Step 4 outcome](#step-4-outcome--the-byte-equality-experiment) — plus the
  step 5 (F13) outcome, see
  [Step 5 outcome](#step-5-outcome--f13-fixed-687-of-687-accepted) — plus
  the step 6 (runtime) outcome, see
  [Step 6 outcome](#step-6-outcome--the-runtime-publishes-through-go-hamqtt)
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
| **F3** | new `internal/layout` package; every topic composed once | none |
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

## Step 4 outcome — the byte-equality experiment

This section was written by phase 7 step 4, the PR that renders the same
catalogue through `go-hamqtt` and publishes none of it. The measurement and
the step-2 outcome above are unchanged; one new finding is added below as
F13.

### The result

**Byte-equality holds, for 2 238 of 2 238 payloads.**

| Configuration | Payloads | Reproduced |
| --- | ---: | ---: |
| `discovery_full_en.json` | 687 | **687** |
| `discovery_full_de.json` | 687 | **687** |
| `discovery_curated_de.json` | 177 | **177** |
| `discovery_plain_en.json` | 687 | **687** |
| `identity_en.json` (the four registry keys) | 687 | **687** |

No golden was regenerated — `-update-discovery-golden` and
`-update-topics-golden` were never passed, the new test file cannot reach
either flag, and **all six SHA-256 literals are untouched**. The comparison
runs against the files as #41 left them.

Nothing reaches a broker. `Discovery.mqtt` is never called from the new
path; `TestHamqttRenderPathPublishesNothing` drives every entry point with a
`Publisher` that fails the test on contact.

### What it took

`internal/hass/hamqtt.go`, ~420 lines including its argument:

- **`hamqttLayout`** — a `topic.Layout` delegating to `internal/layout`, so
  the library path and the daemon's own publish path cannot disagree about a
  topic. `topic.Default` is unusable for two independent reasons: its
  `Bridge()` is `<root>/bridge/status` and this daemon's is `<root>/status`
  (§4), and its `State()` inserts a bucket segment and addresses the device
  by its identity rather than by its raw name (F2).
- **`hamqttContext`** — `discovery.StdContext` with `NodeID`, `ObjectID` and
  `UniqueID` overridden onto this bridge's `slugify`. That is F10 applied,
  not reopened.
- **`describe` / `enrichDescription` / `localizeDescriptionOptions` /
  `sanitizeDescriptionForPlatform`** — `payloadFor`, `applyEnrichment`,
  `localizeOptions` and `sanitizeForPlatform` projected onto a
  `model.Description` in the same order.
- Rendering itself is `discovery.RenderComponent` + `Component.EntityJSON`,
  and the config topic is `publisher.EntityConfigTopic` from the library's
  own `NodeID` — deliberately not `configTopic`, so the step finds out
  whether the library agrees rather than assuming it.

The classification stays put. `classify`, `deviceClassAndUnit`,
`stateClassFor`, `entityCategoryFor`, `enabledByDefault`, `humanize`,
`enumOptions` and the enrichment chain are this bridge's domain logic over
the Home Connect profile; the library replaces the *rendering*, not them.

Two library settings are load-bearing and would each have been a silent
2 238-row diff:

- **`discovery.RawEncoding`.** The zero `Encoding` is `EnvelopeEncoding`,
  which attaches a `value_template` to every entity with a state topic —
  667 of them.
- **`discovery.Origin{}` on the per-entity form.** `RenderComponent`
  attaches an origin block only when the name is non-empty; this bridge's
  per-entity configs carry none.

### The topic form — verdict, with evidence

**Confirmed: five segments with a node id, go-hamqtt's DEFAULT
`publisher.LegacyTopicWithNodeID`.** Step 6 passes no `forms` argument to
`SupersededTopics`.

The evidence is not a reading of `configTopic`.
`TestHamqttTopicFormIsTheFiveSegmentNodeIDForm` takes all 687 **pinned**
config topics from `identity_en.json`, runs each through the library's own
`publisher.ParseConfigTopic`, and re-renders it:

| Form | Reproduces |
| --- | ---: |
| `publisher.LegacyTopicWithNodeID` (the default) | **687 of 687** |
| `publisher.LegacyTopicByUniqueID` (what both siblings needed) | **0 of 687** |

The two are unambiguously distinguishable here, which is what makes the
verdict safe rather than merely correct: the object id is `featureKey(e)`
and the unique id is `homeconnect_<node>_<featureKey>`, so no topic could
be produced by both.

### F13 — a new finding, and the reason step 4 exists

<a name="f13"></a>
#### F13 — the operator catalogue assigns binary-sensor device classes to features that become sensors · **high**

`discovery.ValidateBody` refuses **11 of 687** payloads in each of the three
enriched configurations and **0 of 687** in the unenriched one, which places
the cause in `mapping.yaml` rather than in the heuristic:

```
sensor: device_class "battery_charging" is not valid for platform "sensor"
sensor: device_class "plug" is not valid for platform "sensor"
sensor: device_class "door" is not valid for platform "sensor"   (x9)
```

The mechanism is `deviceClassAllowed` (`payload.go`): `case platformSensor:
return true`. `sensor` and `number` carry Home Assistant's open-ended value
classes, so the filter trusts the operator catalogue absolutely there — and
`mapping.yaml` is generated to mirror the official `home_connect`
integration, where these features **are** binary sensors. Measured at the
source, independently of the pin fixture: **13 catalogued features carry a
device class that Home Assistant's `sensor` platform does not declare**, all
13 of them legal `binary_sensor` classes.

| Feature | `device_class` |
| --- | --- |
| `BSH.Common.Appliance.Connected` | `connectivity` |
| `BSH.Common.Status.BatteryChargingState` | `battery_charging` |
| `BSH.Common.Status.ChargingConnection` | `plug` |
| `BSH.Common.Status.InteriorIlluminationActive` | `light` |
| `Refrigeration.Common.Status.Door.*` (9) | `door` |

Whether a given one lands on `sensor` depends on how the appliance models
the element: a read-only boolean becomes a `binary_sensor`, where the class
is legal, and anything else — an enumeration, a string — becomes a `sensor`,
where it is not. The pin fixture's synthesised descriptors put 11 of the 13
on `sensor`; a real appliance that reports a door status as an enumeration
rather than a boolean reaches the same place. **13 is the bound, 11 is what
this catalogue measures.**

A second, quieter consequence rides along: `sanitizeForPlatform`'s enum
branch is keyed on `device_class == "enum"`, so an enum sensor whose class
the catalogue has overridden falls through to `delete(p, "options")` and
loses its options list too.

What Home Assistant does with it is what makes it worth a finding: nothing
visible. The config is dropped during schema validation, before the entity
is constructed — no error on the wire, no log line naming the cause, and an
entity indistinguishable from one the bridge never published.

**FIXED — see [Step 5 outcome](#step-5-outcome--f13-fixed-687-of-687-accepted).**
The rest of this section is the finding as step 4 recorded it and is left
as written.

**Not fixed here (at step 4).** A fix moves bytes in three of the four
goldens, and step 4 must regenerate nothing. It is pinned exactly instead — by entity key and
class in `TestHamqttPayloadsPassDiscoveryValidate`, and by feature name and
class in `TestCatalogueAssignsBinarySensorClassesToThirteenFeatures` — so
the step that fixes it produces a diff a reviewer can read, and so it cannot
silently grow. The fix belongs at step 7 (operator-visible) or in a step of
its own: either `deviceClassAllowed` stops trusting the catalogue on
`sensor` and filters against `go-ha-catalog`'s per-platform tables, or the
13 catalogue entries move to a platform-aware form. The first is the smaller
change and the one that closes the class of defect rather than these 13
instances.

**Why the stakes change at step 6, again.** Today the damage is 11 entities
per appliance quietly missing. A device bundle is validated as one document
and `discovery.Validate` reports it `Blocking()` — and a blocking bundle
publishes **nothing at all**, so the same 11 rows would cost the device all
687 of its entities. `discovery.ValidateIgnoring` is not the escape: the
class is not a key Home Assistant is known to drop, it is a value it
refuses. **F13 must be fixed before step 6**, and it is the one finding in
this phase with that ordering constraint.

For contrast: go-mtec2mqtt's equivalent step validated clean, and that it
passed was previously unknown. Neither sibling bridge had ever validated its
own output against Home Assistant's schemas. This is what the check buys.

### Two things asserted because no golden can see them

The standard #41 set — every new assertion verified to fail under mutation —
turned up two places where nothing could be made to fail. Both are named
rather than left as coverage nobody checked:

- **`hamqttLayout` ignores `Slot.Address`, `Channel` and `Bucket`.**
  `Address` is `model.Device.UID()`, i.e. `homeconnect_geschirrspuler` — the
  *identity*, which F2 measured to be a different string from the topic
  segment `Geschirrspüler`. A layout that started reading it would move
  every state, command and availability topic of a non-ASCII device name.
  `Channel` and `Bucket` have no counterpart in this tree at all.
  `TestHamqttLayoutIgnoresAddressChannelAndBucket` asserts the inertness in
  both directions: perturbing those three changes nothing, perturbing
  `Scope` or `Path` changes everything.
- **`sanitizeForPlatform`'s button branch is inert over this catalogue.** No
  command feature in `mapping.yaml` carries a `device_class`, and the
  heuristic derives none for a command, so across all 2 238 pinned payloads
  the branch is never taken.
  `TestButtonDeviceClassFilterIsInertOverThisCatalogue` asserts the premise
  (no button in the pin carries a class) and then the behaviour directly, on
  both renderers.

### Mutation proof

Twenty-one mutations, applied one at a time to `hamqtt.go`, `hamqtt_test.go`
and `payload.go`, each run against the whole `internal/hass` and
`internal/bridge` suite and reverted. **Nineteen caught.**

| # | Mutation | Caught by |
| --- | --- | --- |
| M1 | `NodeID` uses `topic.Slug` instead of `slugify` | byte (de + curated) + bundle |
| M2 | `UniqueID` drops the device-id prefix | byte (all four) + identity + buttons |
| M3 | `ObjectID` slugifies the two halves separately | **equivalent mutant — see below** |
| M4 | `Layout.Bridge` takes `topic.Default`'s `<root>/bridge/status` | byte + layout-agreement |
| M5 | `Layout.State` suffix `/state` → `/value` | byte + layout-agreement + inertness |
| M6 | `Layout.Command` suffix `/set` → `/write` | byte + layout-agreement + inertness |
| M7 | `Layout.Availability` → `ConnectionState` | byte + layout-agreement + inertness |
| M8 | `Layout` addresses the device by `Slot.Address` (the identity) | byte + layout-agreement + inertness |
| M9 | `Encoding` left at the library default (envelope) | byte (all four) |
| M10 | the per-entity form gains an origin block | byte (all four) |
| M11 | availability narrowed to `model.BridgeOnly()` | byte (all four) |
| M12 | the two synthetic program buttons are dropped | byte (all four) + buttons |
| M13 | synthetic buttons use `commandPressPayload` | byte (all four) + buttons |
| M14 | `sanitizeDescriptionForPlatform` stops filtering `device_class` | validate + bundle + button-inertness |
| M15 | it keeps `options` on a non-enum sensor | validate + bundle |
| M16 | `enabled_by_default` is never emitted | byte (all four) |
| M17 | a button gains a readable state binding | **equivalent mutant — see below** |
| M18 | the config topic is built from the four-segment unique-id form | compile |
| M19 | the F13 rejection set loses one row | validate + bundle |
| M20 | `deviceClassAllowed` accepts everything on `button` | button-inertness |
| M21 | the verdict test accepts the four-segment form | topic-form |

Two could not be made to fail, and both are **equivalent**, not missed. #41's
standard is that such a thing is named rather than left as coverage nobody
checked, so each has an assertion of its own:

- **M3.** `slugify(device + "_" + key)` and `slugify(device) + "_" +
  slugify(key)` are equal for every input the daemon can reach — `slugify`
  collapses a run of separators to one underscore and trims the ends, so the
  two can only differ when one half slugifies to empty, and
  `profile.validateDeviceName` refuses a device name with no ASCII letter or
  digit for exactly that reason.
  `TestObjectIDCompositionIsAnEquivalentMutant` asserts the equivalence over
  6 183 device × key pairs *and* the one input that breaks it; it fails if
  `slugify` stops trimming.
- **M17.** Giving a button a readable state binding changes no byte, because
  go-hamqtt projects `state_topic` only onto the platforms whose schema
  declares it and `button` is one of the ten that do not. That is the
  library's guarantee, and a test that only ever renders correct input never
  exercises it.
  `TestGoHamqttRefusesAStateTopicOnAWriteOnlyPlatform` renders the wrong
  input deliberately and asserts the refusal.

### `button` — the platform new to this rollout

Neither sibling bridge publishes one. All **20** per appliance are
reproduced byte for byte — 18 command-derived (`payload_press: "true"`,
`entity_category: config`, `enabled_by_default: false`) and 2 synthetic
(`payload_press: "PRESS"`, neither key), which is F6's three deliberate
differences intact. The library's projection is right for the platform
without help: it declines to project a `state_topic` onto a platform whose
schema does not declare one, and it takes the command topic from a
write-only binding rather than requiring a readable one.

### F8 — nothing new

The rendering work revealed nothing that changes F8's analysis. It did
confirm the mechanism from the library's side: `discovery.Bundle.Topic` is
`<prefix>/device/<node_id>/config` and the node id is `slugify(device)` with
no `MQTT_TOPIC` anywhere in it, so two instances with a device name in
common publish to one topic and each publish replaces the other's entire
component set. Still decided-nothing, as #41 left it.

---

## Step 5 outcome — F13 fixed, 687 of 687 accepted

This section was written by the step that fixes F13. The measurement, the
step-2 outcome and the step-4 outcome above are unchanged; F13's own
section keeps the wording step 4 gave it and is marked fixed at its head.

F13 is out of sequence on purpose. The table below ordered it "step 7
(operator-visible) or a step of its own"; it is a step of its own, taken
BEFORE the bundle steps, because it is the one finding in this phase whose
cost changes character at step 6. Today it drops eleven entities per
appliance; in a bundle, which is validated as one document and publishes
nothing at all when `discovery.Validate` reports it `Blocking()`, the same
eleven rows cost the device all 687.

### What was decided, and what it costs a user

Three answers were on the table.

- **Move the affected features to `binary_sensor`**, matching the official
  `home_connect` integration. **Rejected**, on two independent grounds.
  It is the expensive one — a platform change moves the entity's
  `unique_id`-to-domain binding, Home Assistant creates a new entity and
  strands the old one, and `unique_id` and `identifiers` have no migration
  path. But it is also the *wrong* one here, which matters more: these
  eleven land on `sensor` precisely BECAUSE the appliance does not model
  them as read-only booleans. Nine doors and a charging connection arrive
  as strings and the battery-charging state as an enumeration. A
  `binary_sensor` needs a two-valued `payload_on`/`payload_off` mapping
  this bridge does not have and cannot derive, and forcing one would throw
  away a tri-state door reading ("Open"/"Closed"/"Ajar") to gain an icon.
  The platform is not the defect; the class is.
- **Drop the offending `device_class`, keep the platform.** **Taken**, as
  the effect.
- **Stop `deviceClassAllowed` trusting the catalogue absolutely.**
  **Taken**, as the mechanism. It is the structural half and it belongs in
  the change whichever of the first two is chosen: without it, the next
  `mapping.yaml` edit reintroduces the same class of defect, and this
  catalogue is generated.

Concretely, two changes:

1. **`deviceClassAllowed` reads go-ha-catalog's per-platform tables.** It
   said `case platformSensor: return true` and `case platformNumber: return
   dc != deviceClassEnum`. Neither platform has an open vocabulary:
   `sensor` declares 62 classes, `number` 58, `binary_sensor` 28, `switch`
   2, `button` 3 and `select` none, and the two largest are not supersets
   of the smallest. The three hand-maintained sets it replaces were exactly
   Home Assistant's, so the swap moves nothing on `binary_sensor`,
   `switch`, `select` or `button`; only `sensor` and `number` change, and
   `TestTableDrivenAllowanceMatchesTheSetsItReplaced` is what proves the
   first claim rather than asserting it in prose. A failed table decode
   fails **closed** — no entity carries a class at all — because publishing
   none is recoverable by an operator and publishing a refused one is not
   visible at all.
2. **The enrichment step refuses an override the platform does not
   declare, and logs it.** The refusal leaves the HEURISTIC class in place
   rather than clearing the key — the same contract an absent catalogue
   entry has. Home Assistant says nothing when it drops such an entity,
   which is the whole reason F13 survived in a shipped catalogue;
   `hass.device_class_refused` names the feature, the platform and the
   class.

**The rider, and which half was fixed.** `sanitizeForPlatform`'s enum
branch is keyed on `device_class == "enum"`, so an enum sensor whose class
the catalogue overrode fell through to `delete(p, "options")` and lost its
options list too. Falling back to the heuristic fixes that half by
construction: `BSH.Common.Status.BatteryChargingState` keeps `enum` and
keeps its three options. The other half is **pinned as correct, not
fixed**: `BSH.Common.Status.BatteryLevel` is an enum sensor whose catalogue
class is `battery`, which the sensor platform DOES declare, so the override
applies — and Home Assistant's sensor schema permits `options` only
alongside device_class `enum`, so keeping both would produce exactly the
refused config this finding is about. Dropping `options` is the only legal
resolution of an override the operator is entitled to make.
`TestValidDeviceClassOverrideStillDropsOptions` says so, so a later reader
does not "fix" it.

### What an existing user sees

**Eleven entities per refrigeration-capable appliance change; none is
created, destroyed, renamed or re-keyed; no history is lost; nothing must
be done by hand.** Enumerated, because this project's precedent is that
identity moves are listed rather than summarised — and the point of the
list is that *nothing on it is an identity move*:

| Entity key (`sensor.<device>_…`) | Before | After |
| --- | --- | --- |
| `bsh_common_status_batterychargingstate` | `device_class: battery_charging`, no `options` | `device_class: enum`, `options: [Auto, Off, On]` |
| `bsh_common_status_chargingconnection` | `device_class: plug` | no `device_class` |
| `refrigeration_common_status_door_bottlecooler` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_chiller` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_chillercommon` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_chillerleft` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_chillerright` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_flexcompartment` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_freezer` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_refrigerator` | `device_class: door` | no `device_class` |
| `refrigeration_common_status_door_winecompartment` | `device_class: door` | no `device_class` |

For each row the config topic, the `unique_id`, the `default_entity_id`,
the `device.identifiers` and the platform are **unchanged**, so Home
Assistant updates the existing entity in place: same entity id, same
recorder history, same dashboard cards, same automations.

The practical change is smaller than the table looks, because **these
eleven entities did not exist for the user before.** Home Assistant was
discarding each config during schema validation, so what an installed base
actually sees is eleven entities *appearing* where nothing was — as
disabled-by-default diagnostics in the full set, which is where the
catalogue puts them. The ten that lose a class lose an icon and its
semantics with it; the eleventh gains a proper enum sensor with a
localized options list. On a dishwasher, which is the pin fixture's
appliance, every one of the nine refrigeration doors is a feature the real
appliance does not have at all.

Two further refusals are logged and move **no** published byte:
`LaundryCare.Washer.Setting.IDos1BaseLevel` and `…IDos2BaseLevel` carry
`volume` and classify onto `select`, which declares no device class, so
`sanitizeForPlatform` already deleted it at the end of the chain. They are
pinned as `f13AlreadyStrippedDownstream` so a reader counting thirteen log
lines against eleven moved rows does not have to work out why.

### F13's full extent over the catalogue, not the fixture

The fixture is one appliance mix and the next one differs, so the bound is
measured at the source: every catalogued `device_class` against
go-ha-catalog's table for every platform `classify` can produce.
`TestCatalogueDeviceClassesAgainstEveryPlatform` pins all six counts.

**35 features in the shipped `mapping.yaml` carry a `device_class`.** Of
those, the number the target platform refuses is:

| If the appliance models it as… | Platform | Catalogued classes refused |
| --- | --- | ---: |
| a read-only string / enumeration / number | `sensor` | **13** |
| a writable number | `number` | 18 |
| a read-only boolean | `binary_sensor` | 21 |
| a writable enumeration | `select` | 35 |
| a writable boolean | `switch` | 35 |
| a command | `button` | 35 |

The thirteen `sensor` rows — F13 proper, pinned by feature name in
`f13CatalogueRows`:

| Feature | `device_class` |
| --- | --- |
| `BSH.Common.Appliance.Connected` | `connectivity` |
| `BSH.Common.Status.BatteryChargingState` | `battery_charging` |
| `BSH.Common.Status.ChargingConnection` | `plug` |
| `BSH.Common.Status.InteriorIlluminationActive` | `light` |
| `Refrigeration.Common.Status.Door.BottleCooler` | `door` |
| `Refrigeration.Common.Status.Door.Chiller` | `door` |
| `Refrigeration.Common.Status.Door.ChillerCommon` | `door` |
| `Refrigeration.Common.Status.Door.ChillerLeft` | `door` |
| `Refrigeration.Common.Status.Door.ChillerRight` | `door` |
| `Refrigeration.Common.Status.Door.FlexCompartment` | `door` |
| `Refrigeration.Common.Status.Door.Freezer` | `door` |
| `Refrigeration.Common.Status.Door.Refrigerator` | `door` |
| `Refrigeration.Common.Status.Door.WineCompartment` | `door` |

The fixture reaches eleven of them: `BSH.Common.Appliance.Connected` and
`BSH.Common.Status.InteriorIlluminationActive` are modelled as a read-only
boolean and land on `binary_sensor`, where `connectivity` and `light` are
legal and always were. **13 is the bound, 11 is what this fixture
measures** — and an appliance that reports a door status as an enumeration
reaches the same place, which is why the catalogue-level pin exists
alongside the fixture-level one.

The `number` row adds one feature the sensor row does not:
`BSH.Common.Option.RemainingProgramTime` carries `timestamp`, which
`sensor` declares and `number` does not. Nothing in the pin reaches it —
the old `dc != "enum"` test let it through and would have published it —
so it is a defect the structural fix closes before anyone met it.

### What moved, and which literal says so

Diffed as builder **output** — `Discovery.PublishDevice` run in a worktree
at `origin/main` against the same builder run on this branch, into two
files compared row by row — not as fixture files:

| File | Rows changed | Rows |
| --- | ---: | ---: |
| `discovery_full_en.json` | **11** | 687 |
| `discovery_full_de.json` | **11** | 687 |
| `discovery_curated_de.json` | **11** | 177 |
| `discovery_plain_en.json` (unenriched control) | **0** | 687 |
| `identity_en.json` | **0** | 687 |
| `internal/bridge/testdata/topics.json` | **0** | — |

The same eleven rows in all three enriched files, and the same eleven
`discovery.Validate` refused. Ten lose one key; one changes a key's value
and gains `options`. No topic, `unique_id`, `default_entity_id` or platform
moves anywhere, which is why the identity pin and the topic pin are
untouched.

**Three of the six SHA-256 literals move**, each with the reason written
next to it in `golden_digest_test.go`; the other three are asserted
unchanged and a mutation reverting either kind fails.

### The re-proof

| Assertion | Step 4 | Now |
| --- | --- | --- |
| `discovery.ValidateBody`, `discovery_full_en` | 676 of 687 | **687 of 687** |
| `discovery.ValidateBody`, `discovery_full_de` | 676 of 687 | **687 of 687** |
| `discovery.ValidateBody`, `discovery_curated_de` | 166 of 177 | **177 of 177** |
| `discovery.ValidateBody`, `discovery_plain_en` | 687 of 687 | **687 of 687** |
| `discovery.Validate(bundle)`, enriched | `Blocking()`, 11 issues | **clean** |
| `discovery.Validate(bundle)`, unenriched | clean | **clean** |

`TestHamqttPayloadsPassDiscoveryValidate` and `TestHamqttBundleValidates`
are the same two tests step 4 wrote; they now assert the fixed expectation
instead of pinning the refusal, which is the precedent
`go-mtec2mqtt`'s step-3 pin set when the library changed its mind — the
pin is updated to the new truth, not deleted and not left contradicting
reality.

**Byte-equality between the two rendering paths still holds, 2 238 of
2 238.** Both paths took the same change (`applyEnrichment` and
`enrichDescription`), and the goldens they are both compared against were
regenerated from the map path alone, so the go-hamqtt path agreeing with
them is an independent check rather than a tautology.

### Mutation proof

Nineteen mutations, applied one at a time to `payload.go`, `discovery.go`,
`hamqtt.go` and the three test files, each run against the whole
`internal/hass` and `internal/bridge` suite and reverted. The first pass
left two survivors; **neither was equivalent**, so neither was named and
left — both were closed and the pass re-run. **Nineteen of nineteen
caught.**

| # | Mutation | Caught by |
| --- | --- | --- |
| M1 | `deviceClassAllowed` trusts `sensor` again (the original defect) | validate + bundle + goldens + refusal log |
| M2 | `deviceClassAllowed` trusts `number` again | the per-platform catalogue counts |
| M4 | `select` accepts any class | goldens + the replaced-sets test |
| M5 | `applyEnrichment` applies a refused override anyway | validate + bundle + goldens |
| M6 | the refusal CLEARS the key instead of keeping the heuristic | goldens (the enum row) + the rider tests |
| M7 | the refusal is not logged | the refusal-log test |
| M8 | `enrichDescription` applies a refused override anyway | validate + bundle + byte-equality |
| M9 | `sanitizeForPlatform` stops filtering `device_class` | validate + bundle + button inertness |
| M10 | it keeps `options` on a non-enum sensor | validate + bundle + the valid-override pin |
| M11 | the refused-override set loses one row | the fixture-level F13 test |
| M12 | the `sensor` catalogue count is off by one | the per-platform counts |
| M13 | the `number` catalogue count is off by one | the per-platform counts |
| M14 | the go-ha-catalog snapshot size for `sensor` is off by one | the table-loads test |
| M15 | a moved golden digest is reverted to `origin/main`'s | the digest pin |
| M16 | an UNMOVED golden digest is changed | the digest pin |
| M17 | a refused-override row is given the wrong class | the literal cross-check (added to kill it) |
| M18 | `deviceClassAllowedIn` fails OPEN when the table is missing | the fail-closed test (added to kill it) |
| M19 | a catalogue row is given the wrong class | the literal cross-check |
| M20 | the already-stripped set names the wrong class | the literal cross-check |

The two that survived the first pass, and what was done about them:

- **The fail-closed branch was unreachable.** go-ha-catalog's snapshot is
  embedded, so `LoadDeviceClasses` cannot fail at runtime and flipping
  `return false` to `return true` there changed no byte and failed no test.
  The lookup is now `deviceClassAllowedIn(table, platform, dc)` and
  `TestDeviceClassAllowanceFailsClosedWithoutTheTable` exercises the branch
  directly.
- **`f13RefusedOverrides`' VALUES were never read.** The eleven rows are
  checked for the ABSENCE of a class, and absence looks the same whichever
  class was named, so `"plug"` → `"door"` passed. They are now
  cross-checked against `f13CatalogueRows` and against `mapping.yaml`
  itself.

---

## Step 6 outcome — the runtime publishes through go-hamqtt

This section was written by the step that switches the state plane, the
command plane, birth/LWT and the orphan sweep onto
`github.com/SukramJ/go-hamqtt`'s `publisher` package. It is the step the
sequencing table below numbers **5**; it is written up here as the sixth
outcome section because it is the sixth PR of the phase (F13 took a step
of its own).

Step 4 proved the shared library reproduces this bridge's published bytes
exactly, while publishing none of them. **This is where that path starts
actually publishing.** No bundle is published — that is still the next
step — but every plumbing decision the bundle step depends on is made
here.

### What moved, and what did not

| Plane | Before | Now |
| --- | --- | --- |
| Entity state, device availability, `connection_state` | `Bridge.publish` -> `mqtt.Client.Publish(topic, payload, MQTT_QOS, MQTT_RETAIN)` | `publisher.StatePublisher` via `haplane.Plane.PublishState` |
| Discovery configs | `hass.Discovery.mqtt.Publish(..., MQTT_QOS, true)` | `publisher.Runtime.Publish` behind the `hass.ConfigWriter` interface |
| Discovery retraction | `mqtt.Client.Publish(topic, nil, MQTT_QOS, true)` | `publisher.Runtime.Retract` |
| Birth / death / Last Will | three literals in `main.go` | `publisher.Runtime.Will` / `AnnounceOnline` / `AnnounceOffline` |
| Command subscription | `mqtt.Client.Subscribe(<root>/<device>/#, MQTT_QOS, handler, WithNoLocal())` + `go handleSet` | `publisher.CommandRouter`, one route per device |
| Orphan reconcile + `HASS_DISCOVERY_REFRESH` | two hand-rolled snapshot subscriptions | `publisher.Runtime.Sweep`, `ReportOnly`, plus this daemon's own retraction |

**Not moved, deliberately: `publisher.AvailabilityPublisher`.** The
sequencing table names it alongside the state and command planes, and it
is the one piece that does not fit. `AvailabilityPublisher` publishes
unconditionally retained — correct in general, and the reason the library
has the type — while this daemon's device availability topic goes out at
the operator's `MQTT_RETAIN`, which §2.6 pins and which an operator can
set to `false`. Adopting it would change the retain flag of an installed
base inside a migration step whose purpose is something else, which is
exactly the class of silent change this phase exists to avoid. Device
availability therefore travels on the state plane, where `MQTT_RETAIN`
selects between `StatePublisher.Publish` (retained) and
`StatePublisher.Pulse` (not), and the two are byte-identical to what the
daemon published before. Making availability unconditionally retained is
an operator-visible decision and belongs to step 7 or later. Neither
sibling bridge adopted `AvailabilityPublisher` either, for the unrelated
reason that both are bridge-availability-only.

### The pins

**Every discovery payload golden is byte-identical and every SHA-256
literal outside them is untouched**, except one. No golden was
regenerated: `-update-discovery-golden` and `-update-topics-golden` were
never passed.

| Pin | Status |
| --- | --- |
| `discovery_full_en.json`, `discovery_full_de.json`, `discovery_curated_de.json`, `discovery_plain_en.json`, `identity_en.json` | unchanged, all five digests unchanged |
| `internal/bridge/testdata/topics.json` | **two rows hand-edited**, digest moved with a written declaration |

The topic-tree pin's two rows, and why each is the pin that was wrong:

- **`subscribe_filters`.** `homeassistant/+/+/+/config` and
  `homeassistant/+/geschirrspuler/+/config`, both hard-wired to QoS 0,
  are replaced by `homeassistant/#` at `MQTT_QOS`.
  `publisher.Runtime.Sweep` parses all three Home Assistant discovery
  topic forms out of ONE snapshot window rather than encoding one of them
  in a filter, so the narrower pair cannot be expressed at all. This is
  the same single pinned value go-mtec2mqtt's equivalent step moved, for
  the same reason, and it is the whole of that step's topic-tree diff
  too. What the window is willing to DELETE got narrower, not wider — see
  the ownership section below.
- **`publish_qos_retain.bridge_will`.** It said `qos=0 retain=true`. The
  will has gone out at `MQTT_QOS` since #41 fixed F1
  (`cmd/homeconnect2mqtt/main.go`, asserted by
  `TestWillIsTheAvailabilitySourceEveryEntityReads`), so **the golden was
  wrong, not the code** — the row is prose that nothing measured, and it
  went stale in the very commit that changed the thing it describes. It
  is corrected here and `TestWillRowMatchesTheWillTheRuntimeStates` now
  measures it against `publisher.Will` at both MQTT_QOS values.

No published topic, no QoS of any PUBLISH and no retain flag moved.

### QoS — what is passed, and how it was verified

`MQTT_QOS` maps through exactly one function, `haplane.QoS`:

    0 -> publisher.QoSAtMostOnce   (0x80, the deliberate at-most-once sentinel)
    1 -> publisher.QoSAtLeastOnce  (the wire's own 1)
    2 -> publisher.QoSExactlyOnce  (unreachable from config, mapped anyway)
    anything else -> panic at wiring time

That value is stated on **five** fields, none left to a zero value:
`publisher.Config.QoS` (discovery publishes, retractions, birth, death,
Last Will and the sweep's snapshot window), `StateConfig.QoS`,
`StateConfig.PulseQoS`, `CommandConfig.QoS`. `StateConfig.PulseQoS` is
the one that would not have shown up in a single-configuration test: it
is the only field in the package whose default is QoS 0 rather than
QoS 1, so a plane that stated `StateConfig.QoS` and forgot it would
publish a whole `MQTT_RETAIN: false` fleet at a level nobody chose, and
only at `MQTT_QOS: 1`.

**Verified off the transport call, not off the constant**, which is how
#41 wrote the pin and why it survived this whole plane moving:

- `TestQoSZeroReachesTheTransportAsQoSZero` (#41, untouched) drives a
  real `Bridge` and a real `hass.Discovery` at `MQTT_QOS: 0` and asserts
  the QoS on **every** recorded publish and subscribe. It now measures a
  chain three layers deeper than when it was written — `Discovery` ->
  `publisher.Runtime` -> `gomqtt` adapter -> `mqtt.Client` — and still
  reads the same byte.
- `TestStatePublishesCarryMQTTQoSAndMQTTRetain` runs the state plane over
  all four combinations of `MQTT_QOS` x `MQTT_RETAIN` and reads both
  flags off the recorded call.
- `TestEveryPublishCarriesTheStatedQoS` does the same for the discovery
  publish, the retraction, the birth marker and the will.
- `TestSweepWindowIsTheOnlyDiscoverySubscription` reads the sweep's
  snapshot filter and its QoS off the transport, which is the only place
  they are observable: the library composes the filter internally and
  takes the subscription down again.

The mutation that IS the finding — `haplane.QoS(0)` returning
`publisher.QoSUnset` — is caught by all of these. That is F9 closed.

### The command plane, and a guard this bridge structurally cannot use

#41 fixed the sub-tree echo (F4) with `mqtt.WithNoLocal()` plus a
side-effect-free `shouldDispatch()` before the goroutine. All of that
survives; what changed is where it runs.

**The filter is unchanged and cannot change.** The feature path is a
dotted name of variable depth, so no fixed-arity filter covers the
command tree and `<root>/<device>/#` is forced. Measured again here:
that filter matches **all 687** of the device's state topics and all 85
of its command topics. Only the `/set` suffix separates the two, and no
MQTT topic filter can express a suffix.

That has a consequence worth writing down, because it is the one place
this bridge cannot take a guard the library offers:

> **`publisher.CommandRouter.CheckDisjoint` and
> `publisher.StateConfig.CommandFilters` are both unusable here, and
> stating either would break the daemon outright.** Both decide by
> matching a topic against a command FILTER. Handing this daemon's filter
> to the state plane refuses all 687 state publishes per appliance with
> `ErrStateCommandCollision`; `CheckDisjoint` fails the boot for the same
> reason. The disjointness this daemon has is by suffix, enforced by
> `layout.Device.Relative` before dispatch and by MQTT 5.0 No Local at
> the broker. `TestCommandFilterCannotBeStatedToTheStatePlane` asserts
> that rather than leaving it as a comment, and it is written so that it
> FAILS if the filter ever becomes narrow enough for the library guards
> to be usable — at which point they should be stated.

**The double-dispatch question, re-asked.** A broker sends one PUBLISH
copy per matching subscription, and a client that re-matches each copy
against its whole local filter list then runs every matching handler per
copy; openccu-loom measured exactly that against Mosquitto and needed a
separate connection to fix it. #41 established that this daemon's two
overlapping transient config filters are never installed together. Both
halves of that are re-checked here and both still hold, for new reasons:

- The two transient config filters no longer exist. The sweep opens one
  window, and it lives under the discovery prefix, not in the command
  tree.
- The command routes are `<root>/<name>/#` per configured device, and
  `profile.LoadDevices` refuses a duplicate device name, so any two
  differ in a literal level. `TestCommandRoutesAreUnambiguous` asserts
  `CommandRouter.Attributed() == false` — the router accepted no
  overlapping pair and therefore needs no MQTT 5.0 Subscription
  Identifier to tell deliveries apart — and that every advertised command
  topic is claimed by exactly one route.
- The birth subscription (`homeassistant/status`) is under the discovery
  prefix as well. Nothing this daemon subscribes to can match a command
  topic twice.

**What the router buys.** Handlers now run on a worker pool instead of a
fresh unbounded goroutine per delivery, which the old code needed because
`handleSet` blocks for seconds on a Home Connect write and go-mqtt
delivers inline on the goroutine that also decodes PUBACK and PINGRESP.
Order is now preserved for two commands on the same topic, which the
goroutine gave up. `DeliverRetained: false` states as policy what
`shouldDispatch`'s retained check did by hand; both are kept, because a
retained command re-fires a stale write on every reconnect and one
statement of that is not enough.

### Birth, LWT and the one function both sides read

`<MQTT_TOPIC>/status` is unchanged. What changed is that the will is no
longer spelled at the composition root: `publisher.Runtime.Will()`
answers, and `mqttClientConfig` copies all four fields verbatim. The same
runtime's `AnnounceOnline`/`AnnounceOffline` write that topic, so the
broker's death marker and the daemon's own markers agree by construction
rather than by three literals staying in step.

`publisher.Config` is given **both** `StatusTopic` and `Layout`, and that
IS the assertion: `publisher.New` fills an empty `StatusTopic` from the
layout and **refuses** one that disagrees with it. The layout is
`hass.NewLayout`, the same `topic.Layout` step 4 wrote and the same one
`discovery.StdContext` renders every payload's `availability` list from
— so the string the Last Will clears and the string 687 entities wait on
cannot drift. Under `availability_mode: all` a typo there greys out the
whole fleet with nothing on the wire naming the cause;
`TestStatusTopicDisagreementIsRefused` drives the refusal.

`Discovery.BirthTopic` now calls `publisher.BirthTopic(prefix)` rather
than concatenating. It renders the same string here because the prefix is
already trimmed at construction, so this is a second lock rather than a
fix — but the defect it locks is real and was shipped by a sibling:
`prefix + "/status"` against an operator prefix of `"homeassistant/"`
subscribes `homeassistant//status`, which is a legal and DIFFERENT topic
from the one Home Assistant announces on, so after every Home Assistant
restart the entities were gone until the daemon restarted, silent in both
logs.

### One asymmetry that had to be rebuilt, not inherited

The composition root's old comment recorded that "the status-topic
publishes intentionally bypass the breaker", and moving the availability
markers onto `publisher.Runtime` would have quietly lost it: the runtime
has ONE transport, and wiring it through `hagomqtt.Split(breaker,
client)` puts the birth and death markers behind the same circuit as the
687 discovery configs.

That is not cosmetic. `mqtt.Breaker` counts `ErrNotConnected` and
`ErrConnectionLost` as failures, so **a connection drop is exactly what
opens it** — and the first thing a reconnected daemon does is announce
itself online. A birth marker refused there leaves every entity
unavailable under `availability_mode: all` until a recovery probe 30
seconds later happens to succeed, and a daemon that is demonstrably
connected and reporting nothing is the worse of the two states. At
shutdown it is worse still: a graceful DISCONNECT suppresses the Last
Will, so the offline marker is the only one that goes out at all, and a
breaker that refused it leaves a retained `online` standing forever.

`haplane.BypassFor` routes the status topic around the breaker and
everything else through it, and
`TestBypassForKeepsTheAvailabilityMarkersOffTheBreaker` pins the split in
both directions. The asymmetry is right for the same reason it was right
before: the breaker exists for volume, and these are one publish each.

### Rebuild-on-(re)connect — the mistake step 6 must not have available

This PR publishes no bundle. It nevertheless builds the plumbing that
makes the bundle step's ordering hazard unreachable, because that hazard
is not about bundles:

> A per-entity config retained for a `unique_id` and a bundle carrying
> the same id cannot coexist; the second is refused, symmetrically, with
> `WARNING [mqtt.entity] Received a conflicting MQTT discovery message`
> as the only evidence. So: retract first, publish second — and the
> retraction has to be COMPLETE before the document goes out.
> go-mtec2mqtt learned that "complete" is per-CONNECTION, not
> per-process: `publisher.Runtime` remembers which legacy topics it has
> superseded, which configs it has declared and which are in flight, and
> every one of those is a statement about a BROKER. A QoS 0 publish
> "succeeds" when the bytes reach a socket; if that socket then died, the
> broker may have applied none of them. Its in-process retry skipped the
> retractions and published the document anyway, into a tree still
> holding all 100 per-entity configs.

The fix there was structural and it is structural here:
`haplane.Plane.Reconnect()` swaps in a **fresh** `publisher.Runtime` and
`Close()`s the old one, and the bridge calls it at the head of every
`OnConnect` before anything is published on the new connection. A runtime
never outlives the connection it describes, so there is no per-field
question to get wrong and a field the library adds later is covered the
day it is added.

The same call opens the state plane's dedup gate
(`StatePublisher.Reset`), for the same reason on the other plane: a
broker that came back without a persistent retained store holds nothing
while the cache still answers "already published", and every entity would
sit blank until its datapoint next changed — which for a static feature
is never. `TestReconnectOpensTheDedupGateAndRebuildsTheDiscoveryRuntime`
pins both halves.

`OnConnect` is also now registered BEFORE `Lifecycle.Start`, and it
announces online on every connect rather than only the first: the broker
publishes the will on the drop, so a reconnected daemon that did not
re-announce would sit offline in Home Assistant while happily publishing
state nobody displays.

### The sweep — what a ReportOnly pass reported

`publisher.Runtime.Sweep` can retract by itself, judging a retained
config on `SweepRequest.Owns` alone. `Owns` sees the parsed TOPIC and
nothing else, and for this daemon that is the weaker of the two ownership
rules it has always had. The stronger one reads the retained PAYLOAD:
ours is a config whose `unique_id` is in the `homeconnect_` namespace AND
whose `state_topic` sits under this instance's own `MQTT_TOPIC`.

So the pass runs **`ReportOnly: true`**, the payload check runs in
`SweepRequest.Inspect`, and the retraction is this daemon's own
`Runtime.Retract` over the list it narrowed. That is not a stylistic
choice; see F8 below.

`TestReportOnlySweepOverTheRealFleet` runs the real builder over the real
`mapping.yaml` against a deliberately hostile retained tree and logs:

```
ReportOnly over the real fleet: 10 retained configs offered, 3 owned by
topic, 687 claimed, 1 would be retracted:
[homeassistant/sensor/geschirrspuler/retired_feature/config]
```

Every survivor, and the reason each survived — the fan-out is the point,
because a pass that spared everything for one reason has only been shown
to work in one direction:

| retained topic | verdict | declined by |
| --- | --- | --- |
| `sensor/geschirrspuler/retired_feature/config` | **retract** | — ours, and nothing claims it |
| `binary_sensor/geschirrspuler/bsh_common_appliance_connected/config` | keep | claimed: this batch just published it |
| `sensor/geschirrspuler/sibling_only/config` | keep | **payload**: its `state_topic` is under another instance's root (F8) |
| `sensor/zigbee2mqtt_bridge/state/config` | keep | node id: not a configured device |
| `binary_sensor/tasmota_ABC/status/config` | keep | node id: not a configured device |
| `device/geschirrspuler/config` | keep | the device-document form, which this daemon does not publish |
| `sensor/homeconnect_geschirrspuler_x/config` | keep | the four-segment, node-id-less form — a sibling bridge's shape, and Tasmota's |
| `climate/geschirrspuler/thermostat/config` | keep | a platform this daemon never emits (6 of Home Assistant's 32) |
| `sensor/backofen/other_device/config` | keep | device scope: a second appliance of ours that has not connected yet |
| `sensor/geschirrspuler/emptied/config` | keep | already empty; retracting it again would be a message for nothing |

**The device scope is not a refinement.** This daemon mirrors several
appliances that connect independently, so the window in which only the
first has published is the normal case rather than a race; a fleet-wide
predicate would call the second appliance's configs orphans there and
clear them. `TestSweepDoesNotRetractASecondAppliancesConfigs` drives it.
The one pass whose scope IS the fleet is `HASS_DISCOVERY_REFRESH`, and it
is safe precisely because it runs before any worker has published:
nothing is claimed, so "owned and unclaimed" is "owned", which is what
the flag means to clear.

**Both claim sets are subtracted and neither is redundant.** The batch's
own `published` map covers a config whose publish FAILED — an open
circuit breaker leaves it out of the runtime's declared set, and
retracting it would delete an entity this daemon is actively trying to
create. `Runtime.Declared()` covers a config published on an earlier,
larger batch that a transient classification shrank. Each half has an
isolating test.

### F8 — the sweep cannot reach a sibling instance, and here is the proof

F8 stays decided-nothing, as #41 left it. The question this step owed it
is narrower and it is answered: **does the `Owns` predicate preserve
today's protection?**

Today the protection is `IsOwnConfig`: two instances with different
`MQTT_TOPIC` roots, the same `HASS_BASE_TOPIC` and an appliance name in
common publish the same 687 config topics with the same 687 `unique_id`s
— `MQTT_TOPIC` appears in none of the four identity strings — and they
overwrite each other's retained configs, but neither retracts the
other's.

**`Owns` alone does NOT preserve it, and cannot.** It sees a topic, and
the two instances' topics are byte-identical: same prefix, same platform,
same node id (`slugify` of the same device name), same object id
(`featureKey`). There is no narrowing available at the topic level that
separates them. That is precisely why the pass is `ReportOnly` and why
`IsOwnConfig` runs in `Inspect` — the `state_topic` in the payload is the
only place the two instances differ, and it is the rule this daemon has
always used.

The protection is therefore preserved, and it is pinned by DRIVING the
sweep rather than by asking the predicate — which is the distinction that
matters, because go-mtec2mqtt's equivalent step asserted its predicates
directly, recorded a two-instance overlap as harmless, and one PR later
the same overlap deleted a sibling instance's entire fleet:

- `TestSweepSparesASiblingInstancesConfigs` runs the whole pass with an
  EMPTY claim set — the worst case, where every owned topic is unclaimed
  and only the payload check stands between the sibling's fleet and
  deletion — and asserts nothing is retracted.
- `TestAStaggeredUpgradeDoesNotDeleteTheSiblingsFleet` is the shape that
  falsified mtec's "harmless": instance A migrates to a device bundle and
  stops publishing per-entity configs, so its published set no longer
  names the topics instance B still owns. Simulated the only way it can
  be before step 6 exists — by publishing nothing at all, which is
  exactly what an upgraded instance's per-entity set looks like.
- The mutation that removes `IsOwnConfig` from `Inspect`, and the
  mutation that turns `ReportOnly` off, are both caught by those two
  tests. Either one would ship a sweep that deletes a sibling's fleet.

**A note for step 6.** `OwnsConfigTopic` refuses `t.Bundle` today because
this daemon publishes no device document. The day it does, that line
becomes load-bearing in the other direction and has to be revisited
deliberately — it is not a guard to delete on sight.

### Mutation proof

Forty-three mutations, applied one at a time to a filesystem COPY of the
committed tree (never `git checkout --`; an agent in this family lost
uncommitted work to a revert in its own harness), each run against the
whole suite and reverted.

**Thirty-nine caught. Four survivors, and all four are equivalent** —
none is coverage nobody checked. The first pass left six survivors; two
of them were NOT equivalent and were closed rather than explained away,
which is the standard #43 set:

- **M30** (the router delivers retained messages) survived because
  `shouldDispatch`'s own retained check masks the router's policy, and
  because the test that watched the router built its own
  `CommandConfig` rather than the daemon's. The policy moved into
  `Bridge.commandConfig`, and
  `TestTheRouterItselfDropsARetainedDelivery` now builds a router from
  that value.
- **M41** (the availability markers go through the circuit breaker)
  survived because the wiring lived inline in `serve()`, which needs a
  broker and cannot be driven. It moved into `haTransport`, and
  `TestHATransportKeepsTheAvailabilityMarkersOffTheBreaker` drives it
  against a deliberately tripped breaker — which is the state a
  reconnect actually finds.

| # | Mutation | Caught by |
| --- | --- | --- |
| M1 | QoS(0) returns the zero value (the F9 defect itself) | TestEveryPublishCarriesTheStatedQoS, TestMQTTQoSZeroStaysQoSZero, TestQoSTranslatesZeroToTheDeliberateSentinel (+3) |
| M2 | QoS(1) returns at-most-once | TestEveryPublishCarriesTheStatedQoS, TestMQTTQoSZeroStaysQoSZero, TestPublishOnlineRebuildsThePlaneAndAnnounces (+3) |
| M3 | QoS coerces an out-of-range level instead of panicking | TestQoSRefusesAValueNobodyChose |
| M4 | StateConfig.QoS left at the zero value | TestEveryPublishCarriesTheStatedQoS, TestQoSZeroReachesTheTransportAsQoSZero, TestStatePublishesCarryMQTTQoSAndMQTTRetain |
| M5 | StateConfig.PulseQoS left at the zero value | TestEveryPublishCarriesTheStatedQoS, TestStatePublishesCarryMQTTQoSAndMQTTRetain |
| M6 | publisher.Config.QoS left at the zero value | TestEveryPublishCarriesTheStatedQoS, TestMQTTQoSZeroStaysQoSZero, TestQoSZeroReachesTheTransportAsQoSZero (+1) |
| M7 | PublishState ignores MQTT_RETAIN and always retains | TestNonRetainedStateIsNeverDeduplicated, TestPublishStateHonoursMQTTRetain, TestStatePublishesCarryMQTTQoSAndMQTTRetain |
| M8 | PublishState never retains | TestPublishStateDeduplicatesARetainedRepeat, TestPublishStateHonoursMQTTRetain, TestStatePublishesCarryMQTTQoSAndMQTTRetain |
| M9 | an empty state payload goes to Publish instead of Evict | TestPublishStateHonoursMQTTRetain |
| M10 | Reconnect keeps the discovery runtime | TestPublishOnlineRebuildsThePlaneAndAnnounces, TestReconnectOpensTheDedupGateAndRebuildsTheDiscoveryRuntime |
| M11 | Reconnect does not open the dedup gate | TestReconnectOpensTheDedupGateAndRebuildsTheDiscoveryRuntime |
| M12 | Encoding left at the library default (envelope) | **equivalent — see below** |
| M13 | the deferred transport silently succeeds before it is wired | TestTransportRefusesUseBeforeItIsWired |
| M14 | the will is spelled again at the composition root | TestWillIsCopiedFromTheRuntimeNotSpelledAgain |
| M15 | the will loses its retain flag | TestWillIsTheAvailabilitySourceEveryEntityReads |
| M16 | the sweep retracts on the topic namespace alone (ReportOnly off) | TestRefreshDiscoveryOnceIsFleetWide, TestReportOnlySweepOverTheRealFleet, TestSweepRetractsOurOwnOrphan (+1) |
| M17 | Inspect drops the payload ownership check (F8's protection) | TestAStaggeredUpgradeDoesNotDeleteTheSiblingsFleet, TestRefreshDiscoveryOnceIsFleetWide, TestReportOnlySweepOverTheRealFleet (+1) |
| M18 | the per-device reconcile judges the whole fleet | TestSweepDoesNotRetractASecondAppliancesConfigs |
| M19 | orphanTopics forgets what this batch minted | TestSweepSparesAConfigWhoseOwnPublishFailed |
| M20 | orphanTopics forgets what this process declared | TestSweepSparesADeclaredConfigOutsideThisBatch |
| M21 | the refresh is device-scoped instead of fleet-wide | TestRefreshDiscoveryOnceIsFleetWide |
| M22 | the retraction becomes a no-op publish | TestRefreshDiscoveryOnceIsFleetWide, TestSweepRetractsOurOwnOrphan |
| M23 | Owns accepts a platform this daemon never emits | TestOwnsConfigTopic, TestReportOnlySweepOverTheRealFleet |
| M24 | Owns accepts any node id | TestOwnsConfigTopic, TestReportOnlySweepOverTheRealFleet, TestSweepDoesNotRetractASecondAppliancesConfigs |
| M25 | Owns accepts the device-document form | **equivalent — see below** |
| M26 | Owns accepts a node-id-less (four-segment) config | **equivalent — see below** |
| M27 | ConfigTopicFor drops the platform segment | TestConfigTopicForRebuildsTheTopicItParsed, TestReportOnlySweepOverTheRealFleet |
| M28 | the command route loses MQTT 5.0 No Local | TestCommandFilterIsGuardedAgainstTheDaemonsOwnTree, TestTopicGolden |
| M29 | the router subscribes at the library default instead of MQTT_QOS | TestQoSZeroReachesTheTransportAsQoSZero |
| M30 | the router delivers retained messages | TestTheRouterItselfDropsARetainedDelivery (added to kill it) |
| M31 | shouldDispatch stops dropping a retained replay | TestCommandFilterIsGuardedAgainstTheDaemonsOwnTree |
| M32 | shouldDispatch stops checking that the topic is a command topic | TestCommandFilterIsGuardedAgainstTheDaemonsOwnTree, TestCommandRouterRoutesEveryAdvertisedCommandTopic |
| M33 | the routes are registered but never started | TestARoutedCommandReachesTheHandlerOffTheReadLoop, TestTheDaemonsOwnStateEchoIsNotDispatched |
| M34 | PublishOnline does not rebuild the plane | TestPublishOnlineRebuildsThePlaneAndAnnounces |
| M35 | PublishOnline does not announce | TestPublishOnlineRebuildsThePlaneAndAnnounces |
| M36 | the state publish bypasses the plane's retain policy | TestStatePublishesCarryMQTTQoSAndMQTTRetain |
| M37 | the birth topic is composed locally instead of by the library | **equivalent — see below** |
| M38 | a nil plane is accepted at construction | TestNewValidations |
| M39 | the topics golden digest is reverted to origin/main's | TestTopicsGoldenMatchesItsPinnedDigest |
| M40 | a discovery golden digest is changed | TestGoldenFilesMatchTheirPinnedDigests |
| M41 | the availability markers go through the circuit breaker | TestHATransportKeepsTheAvailabilityMarkersOffTheBreaker (added to kill it) |
| M42 | BypassFor routes everything around the breaker | TestBypassForKeepsTheAvailabilityMarkersOffTheBreaker |
| M43 | BypassFor routes nothing around the breaker | TestBypassForKeepsTheAvailabilityMarkersOffTheBreaker |

The four that remain are equivalent, and each has an assertion of its
own so a later reader can tell "deliberately stated" from "silently
ignored":

- **M12 — `StateConfig.Encoding`.** Inert over every call path this
  daemon has: it renders its own state payload and hands the BYTES to
  `Publish`, which never consults the encoding. It is stated anyway
  because the zero value is `EnvelopeEncoding`, and the day a call site
  reaches for `PublishValue` that would wrap every payload in JSON the
  667 configs on the broker have no `value_template` to read.
  `TestStateEncodingIsInertAndStatedAnyway` asserts the inertness in
  both directions — `Publish` ignores the encoding, and the two
  encodings genuinely differ through the renderer.
- **M25 and M26 — two guards in `OwnsConfigTopic`.** `t.Bundle` is
  subsumed by `t.Platform == ""` (a device document's topic carries no
  platform segment), and `t.NodeID == ""` is subsumed by the node scope
  (the node-id-less forms parse with an empty node id, and
  `validateDeviceName` refuses a name that slugifies to empty, so
  `nodes[""]` can never be true). Both are kept because each states an
  intent the next does not, and because **`t.Bundle` stops being
  redundant the day this daemon publishes a device document** — step 6
  does exactly that. `TestOwnsGuardsAreSubsumedByTheNodeScope` asserts
  the subsumption and turns red the day either stops holding.
- **M37 — `Discovery.BirthTopic` calling `publisher.BirthTopic`.** It
  renders the same string as a local concatenation because `New`
  already trims the prefix, so this is a second lock rather than a fix.
  The defect it locks is real and was shipped by a sibling: against an
  operator prefix of `"homeassistant/"`, `prefix + "/status"`
  subscribes `homeassistant//status`, a legal and DIFFERENT topic.
  `TestBirthTopicSurvivesATrailingSlashPrefix` asserts the property
  through the raw operator value, so removing BOTH locks turns it red.

### Findings

<a name="f15"></a>
#### F15 — two library guards are structurally unusable on this bridge · **medium, unfixable at the filter**

`publisher.CommandRouter.CheckDisjoint` and
`publisher.StateConfig.CommandFilters` both decide by matching a topic
against a command filter, and this bridge's command filter necessarily
matches its own entire state tree (F4: the feature path is
variable-depth). Stating either would refuse all 687 state publishes per
appliance or fail the boot.

This is not a defect in the library and not one in this bridge; it is a
consequence of the topic shape, and it means the two cheapest
self-echo guards in the package do not apply here. What does apply is
`layout.Device.Relative`'s `/set` suffix check before dispatch and MQTT
5.0 No Local at the broker, both of which #41 put in place. It is
recorded as a finding so that a later reader who notices the fields are
unset does not "fix" it, and
`TestCommandFilterCannotBeStatedToTheStatePlane` fails the day the filter
becomes narrow enough for them to be usable.

<a name="f14"></a>
#### F14 — a pinned row of `topics.json` was prose nothing measured · **low**

`publish_qos_retain.bridge_will` said `qos=0 retain=true` from #40 until
this step. #41's F1 fix changed the will to go out at `MQTT_QOS` in the
same commit that made every entity read the topic, and the row did not
move with it, because the whole `publish_qos_retain` map is descriptive
strings compared against a copy of themselves. A pin that nothing
measures is a pin that documents whatever was true when it was typed.

Corrected here, and `TestWillRowMatchesTheWillTheRuntimeStates` now reads
the value off `publisher.Will` at both `MQTT_QOS` levels. The other five
rows of the map remain prose; they are all `MQTT_QOS`/`MQTT_RETAIN`
statements that `TestStatePublishesCarryMQTTQoSAndMQTTRetain` and
`TestQoSZeroReachesTheTransportAsQoSZero` do measure, off the transport,
so the class is closed even though the strings are not derived.

<a name="f16"></a>
#### F16 — the sweep window overlaps the Home Assistant birth subscription · **low, measured harmless**

The sweep's snapshot window is `<HASS_BASE_TOPIC>/#`, which matches Home
Assistant's own birth topic `<HASS_BASE_TOPIC>/status`. The two filters
it replaced (`homeassistant/+/+/+/config` shapes) did not, so this
overlap is new. A broker sends one copy per matching subscription and
go-mqtt re-matches each copy locally, so while a window is open a birth
message reaches the birth handler more than once.

Measured harmless here, for three reasons that are stated rather than
assumed — the general case is not harmless, and openccu-loom measured a
doubled physical action from exactly this shape:

- The sweep's handler discards anything that is not a parseable discovery
  config topic, and `<prefix>/status` deliberately is not one.
- The birth handler's effect is `publishDiscovery` per device, which is
  idempotent: the per-device reconcile is gated against re-entrancy and
  `publisher.Runtime` deduplicates a config against what it already
  published, so a second run writes nothing.
- **Neither subscription is in the command tree**, which is the property
  that actually matters — no command handler can be reached twice.

`TestTheSweepWindowOverlapsTheBirthSubscriptionHarmlessly` pins all
three, and fails the day a third subscription appears under the discovery
prefix, which is the point at which the reasoning has to be redone.

### What step 6 still has to do

Unchanged from the sequencing table below, minus the plumbing this step
built:

- `publisher.Runtime.PublishBundle` with the **default**
  `LegacyTopicWithNodeID` form and no `forms` argument (§2.3, proved
  687/687 at step 4). `publisher.Config.LegacyEntityTopics` stays nil,
  which is that default.
- The retraction ordering is already available and already
  connection-scoped: `PublishBundle` supersedes before it publishes, and
  the runtime it does that on is rebuilt on every (re)connect.
- `OwnsConfigTopic`'s `t.Bundle` refusal has to be revisited the day a
  bundle is published.
- F8's decision (a discriminator in the identity namespace, or not) is
  still open, and its stakes still change at that step: one retained
  document per device instead of 687 independent topics.

---

## Step 7 outcome — one retained device document per appliance

This section was written by the step the sequencing table below numbers
**6**: discovery switches from 687 retained per-entity configs to one
retained device document per appliance. It is the last step of the phase and
the only one that can destroy an installed Home Assistant setup, because the
configs it retracts and the document it publishes cannot coexist and the
window between them is an appliance with no discovery config at all.

### The measurement

| | |
| --- | --- |
| Components in the document | **687** (full), 177 (curated) |
| Document payload | **472 847 bytes** (`de`, full), 459 067 (`en`), 122 900 (curated `de`) |
| One retained PUBLISH of it | **472 953 bytes** on the wire |
| Per-entity configs retracted first | **687**, one per component |
| Topic | `homeassistant/device/geschirrspuler/config` |

Measured by `TestTheMigrationRetractsEveryPerEntityConfigBeforeTheDocument`,
which drives a real `Bridge`, a real `hass.Discovery` and the real publish
plane over the pin catalogue and reads the numbers off the recorded
transport calls. It is roughly **nine times** go-mtec2mqtt's ~50 KB, which
is why the packet-size preflight is not a formality here.

### Retraction completeness, and why it is a statement about a connection

`publisher.Runtime.PublishBundle` retracts every superseded per-entity
config and then publishes the document. It remembers which topics it has
retracted, so a steady-state boot does not re-send 687 empty payloads
forever — a memo that is correct **per connection** and wrong per process. A
QoS 0 publish "succeeds" when the bytes reach a socket; if that socket then
died, the broker applied none of them.

go-mtec2mqtt shipped a process-lifetime runtime and measured the
consequence: `retractions re-sent = 0, document published = true, configs
still retained = 1`. #44 built the structural answer here in advance —
`haplane.Plane.Reconnect` swaps in a fresh `publisher.Runtime` and closes
the old one, and `Bridge.PublishOnline` calls it at the head of every
(re)connect — and this step **verifies** it rather than assuming it:

`TestTheRetractionsAreReSentAfterAReconnect` drives the defect and the fix
against the same code, as two subtests of one table. Connection 1 gets the
retractions out and has the document refused; the link comes back; then

- **without** `Plane.Reconnect`, the retry re-sends **0** retractions and
  publishes the document anyway — mtec's measurement, reproduced here;
- **with** it, all 687 (3 in the unit fixture) are re-sent, and every one of
  them lands **before** the document.

That is the pin the prompt asked for: a **reconnect** between the
retractions and the bundle, not a process restart. The process restart is
pinned separately by `TestTheCrashWindowHealsOnTheNextBoot`, because the two
catch different things — mtec had only the second, which is why the first
survived review.

### The four questions mtec's review left for this bridge

**1. The dedup gate.** Half of it was already right and half of it did
nothing. `Plane.Reconnect` throws the runtime away, so the gate is OPEN on
the new connection — but **nothing walked through it**: `onState` publishes
when an APPLIANCE connects, the birth handler when HOME ASSISTANT restarts,
and a broker reconnect is neither. A broker that came back without its
retained store therefore kept the fleet missing until the daemon itself was
restarted. `Bridge.PublishOnline` now re-drives the publish for every
appliance, off the hook's goroutine (`mqtt.Lifecycle` runs `OnConnect`
inline on its reconnect loop).

`publisher.Runtime.Republish` is deliberately **not** the mechanism, for the
reason mtec reached independently: it re-sends the bytes the runtime cached,
and a brand-new runtime has cached none — and a cached replay would skip the
supersede step whose per-connection completeness is the whole point.

It waits for `Run` to finish the one-shot `HASS_DISCOVERY_REFRESH`
migration, because `OnConnect` fires before `Run` on the first connect and
the republish would otherwise write the document the refresh is about to
clear. And the flag is **coalesced**, not skipped: a pass interrupted by a
drop has published some appliances against a dead connection's runtime, and
a later pass that gave up because one was in flight would leave exactly
those unpublished.

**2. The sweep guard.** `publishDiscovery` now tests that the document was
**published**, twice and independently: `PublishDeviceBundle` returned no
error, and `publisher.Runtime`'s own `Declared()` names the topic. mtec's
guard asked whether the document had been BUILT while its log line said
"published", and a build-succeeds-publish-fails run swept the previous
release's whole fleet away and put nothing back.
`TestTheSweepIsSkippedWhenTheDocumentWasNotPublished` refuses the document
at the stub and asserts **no snapshot window was opened** — read off the
SUBSCRIBE list, not the live handler map, because the window unsubscribes on
the way out.

**3. The preflight.** `haplane.Plane.PublishBundle` measures the document
against the broker's advertised Maximum Packet Size and refuses **before**
the retraction. It has to be one layer above `PublishBundle`, because
go-mqtt raises `mqtt.ErrPacketTooLarge` from inside the PUBLISH, with the
687 retractions already gone.

The limit read is `mqtt.ConnectResult.MaximumPacketSize` — MQTT 5.0's
property 0x27 off the CONNACK, the broker's **outbound** limit. It is not
`mqtt.TCPConfig.MaximumPacketSize`, which is the largest packet this CLIENT
accepts INBOUND and defaults to 1 MiB whatever the broker will take; mtec's
notes conflated the two.

Unknown is unknown: a nil hook, a connection that has not answered, an MQTT
3.1.1 link with no property block, and a broker that set no limit all
publish. `TestAnUnknownBrokerMaximumIsNotASmallOne` drives all three
spellings, and `TestADocumentTooLargeForTheBrokerRetractsNothing` asserts
that a refusal writes **nothing at all**.

At 473 KB the document clears Mosquitto (unlimited by default) and EMQX
(1 MB) and does not clear a hardened `max_packet_size 262144`. That is an
operator-visible outcome and it is documented as one.

**4. The crash window.** Retractions out, document not, daemon dead: the
appliance has no discovery config — absent, not unavailable. It heals on the
next boot precisely because nothing is remembered across one, and
`TestTheCrashWindowHealsOnTheNextBoot` asserts **both** halves, because a
boot that trusted the dead process's retraction would publish into a tree
still holding every per-entity config.

### Tombstones — deferred, and what that costs

**Decided: deferred.** An omitted component is not removed from Home
Assistant; removal needs the key present carrying a platform and nothing
else, plus `Bundle.Tombstones` holding the `unique_id` outside the payload
so the old per-entity config can still be retracted. Under the per-entity
form the orphan sweep did that job; under a document it does not, and the
stranded entity reads **available** — its availability list names the bridge
status topic and the device availability topic, and this daemon goes on
publishing both.

The trigger surface here is **larger** than mtec's, and that is stated
rather than glossed: a feature excluded in `mapping.yaml`, `HASS_DISCOVERY`
switched from `full` to `curated` (510 of 687 components on the pin
fixture), an appliance replaced by one with a different feature list, and a
firmware update that changes that list.

It is deferred anyway, for three reasons:

- **The daemon has no memory of the previous document.** The catalogue is
  compiled in, nothing is persisted, and the sweep runs AFTER the publish so
  its snapshot holds the document this boot just wrote. The only
  restart-surviving source is the broker, which means a **new pre-publish
  read-back window** inserted immediately before the one publish of the
  release that cannot be undone.
- **Its failure mode is the wrong one.** A read-back that mis-parses writes
  tombstones for LIVE components and deletes working entities. The deferral's
  failure mode is a stale entity an operator can delete. In the step whose
  whole discipline is "fail in the recoverable direction", that settles it.
- **This PR already changes that path twice** — the per-connection ordering
  and the size preflight. A third change to the same three lines, with no
  way to prove the read-back against a real broker from a unit test, is more
  risk than the capability is worth in this step.

`TestOmittingAComponentDoesNotRemoveIt` is the record, and it asserts both
halves: that omission is inert, and that `Bundle.RemoveComponents` produces
exactly `{"platform":"…"}`, carries no `unique_id`, keeps the identity in
`Tombstones` and does then render the legacy retraction. The day tombstones
are implemented, that is the test that already describes them.

**The workaround, with the step mtec's documentation was missing:** Home
Assistant only offers the delete affordance once the entity is no longer
being *provided*, so **restart Home Assistant (or reload the MQTT
integration) first**, then delete the entity from the device page.

### The downgrade

A user who rolls back finds the document retained, and by the same symmetry
the old release's per-entity configs are refused. One command per appliance,
documented in **four** places (`changelog.md`, `addon/CHANGELOG.md`,
`README.md`, `addon/DOCS.md`):

```sh
mosquitto_pub -h <broker> -u <user> -P <password> \
  -t 'homeassistant/device/geschirrspuler/config' -r -n
```

`-u`/`-P` are not optional: `script/run.sh` takes the username and password
from the Supervisor MQTT service, so every add-on user is on an
authenticated broker. mtec's add-on copy omitted them, and got the topic
wrong in three of five places besides, because the node id is
`slugify(<device name>)` — `Geschirrspüler` becomes `geschirrspuler`, which
is neither the raw name nor its lower case.

So the topic is not trusted to prose. `TestTheDocumentedDowngradeTopicMatchesTheCode`
extracts it from all four files, compares every copy against the string
`Discovery.BundleTopic` actually addresses, requires the copies to agree
with each other, and requires `-h`/`-u`/`-P` in each. The mutation that
writes the raw appliance name into one file turns it red.

### F8 revisited — a live defect this step would have made unconditional

`OwnsConfigTopic` sees a topic and the two instances' topics are identical,
so `IsOwnConfig` is the whole of F8's protection. It read:

```go
strings.HasPrefix(cfg.UniqueID, "homeconnect_") &&
    (cfg.StateTopic == "" || strings.HasPrefix(cfg.StateTopic, d.rootTopic+"/"))
```

**A `button` carries no `state_topic`**, and there are 20 per appliance (6
of the curated 177). For those the rule collapsed to the bare
`homeconnect_` prefix, which a sibling instance shares. Driven through this
repo's own sweep harness with an empty claim set, it cleared three of a
sibling's buttons. Reachable with no bundle at all — one instance `curated`
and one `full`, or any `HASS_DISCOVERY_REFRESH`, which runs fleet-wide with
nothing claimed — and **unconditional at this step**, because an upgraded
instance publishes no per-entity configs and every one of a sibling's is
unclaimed at once.

The rule now gathers every MQTT topic the payload names — `state_topic`,
`command_topic`, `availability_topic` and the `availability` list — requires
at least one, and requires all of them under this instance's root. Both
directions are deliberate: a payload naming no topic cannot be proven ours
and is not claimed; a payload mixing roots is not ours whatever else it
says. `TestSweepSparesASiblingInstancesConfigs` and
`TestAStaggeredUpgradeDoesNotDeleteTheSiblingsFleet` now carry `button`
payloads, and `TestSweepRetractsOurOwnOrphan` carries one of **ours**, so
the fix cannot pass by refusing everything.

`OwnsConfigTopic`'s `t.Bundle` refusal was flagged by #44 as "not a guard to
delete on sight" the day a document is published. It was revisited and
**kept**: the document is now the single retained topic holding a whole
appliance, and `IsOwnConfig` reads a top-level `unique_id` and `state_topic`
a document does not have — so a widened predicate would not be narrowed
again by the payload check that protects a sibling.
`TestTheSweepNeverOffersTheDocumentItJustPublished` drives the pass with an
empty claim set, which removes the other lock.

### The pins

**Every SHA-256 literal is untouched and no golden was regenerated.**
`-update-discovery-golden` and `-update-topics-golden` were never passed;
`git diff origin/main --stat -- '*testdata*' internal/hass/golden_digest_test.go internal/bridge/topics_digest_test.go`
is empty. `internal/bridge/testdata/topics.json` does not move either: the
document's topic is not in it, the subscribe filters do not change, and
`publish_qos_retain.discovery_config` still describes a retained publish at
`MQTT_QOS`.

The five per-entity pins are instead **re-used against the new artefact**.
`TestTheDocumentsComponentsArePinnedPayloads` compares every component of
every document against the pinned per-entity payload of the same entity, in
all four configurations — 2 238 rows — under exactly two enumerated
differences: `device` is hoisted to the document, and `platform` moves out
of the topic into the entry. `TestTheDocumentCarriesThePinnedDeviceBlock`
asserts the key the first test deletes. Everything Home Assistant keys a
registry on is therefore proved equal to bytes written before go-hamqtt
existed.

### Three more findings from the same review, taken here

- **The breaker-bypass topic was spelled twice at the composition root**
  (`haplane.Config.StatusTopic` and the `haTransport` argument), and
  appending `"/x"` to the second survived the whole suite because the test
  supplied its own constant. #44 caught exactly this as M41 and it returned
  one line away. `haPlaneTransport` now takes the **plane** and derives the
  topic from `Plane.StatusTopic()` inside the function the test drives — the
  derivation is the part that can be wrong, so it has to be inside what is
  driven.
- **`availOnline`/`availOffline` could be swapped with the suite green**:
  the three tests that touched them compared the published byte against the
  same constants they were testing.
  `TestTheDeviceAvailabilityPayloadsAreTheOnesEveryConfigDeclares` crosses
  the two planes instead — the worker's byte against the
  `payload_available` in the rendered config.
- **The shutdown comment described a drain that does not exist.**
  `publisher.Runtime.Close` drains the birth-replay worker, and that worker
  exists only after `Runtime.WatchBirth`, which this daemon never calls (it
  watches the birth topic itself). So the hazard the comment named was real
  and unhandled. `Bridge.StopDiscovery` closes it, before the offline
  marker; `Close` is kept and its prose corrected.

### Mutation proof

Thirty-one mutations, applied one at a time to a filesystem COPY of the
committed tree (never a `git checkout --`) and each run against the whole
suite. **Twenty caught on the first pass, ten survived**, and the survivors
are the useful half of the exercise: one of them was not a gap in coverage
but a defect in a pin, and four were guards masked by other guards.

**The first pass's ten survivors, and what each turned out to be:**

| # | Mutation | First pass | After the follow-up |
| --- | --- | --- | --- |
| M6 | the error guard alone is removed from the sweep gate | survived | **masked by M7** — see below |
| M7 | the claim guard alone is removed | survived | **masked by M6** — see below |
| M6b | **BOTH sweep guards removed at once** | — | caught by `TestTheSweepIsSkippedWhenTheDocumentWasNotPublished`, once it stopped racing its own sweep |
| M8 | an empty document is published | survived | caught — the test asserted a non-nil error, and `discovery.Validate` produces one too, whose text even contains "bundle has no components". The discriminator is now the TYPE |
| M11 | ownership claimed for a payload naming no topic | (invalid mutant: it did not compile) | caught by `TestIsOwnConfig` |
| M12 | a topic outside our root no longer disqualifies | survived | caught — a row whose topics disagree about their root |
| M13 | `command_topic` is not read | survived | caught — a row carrying `command_topic` alone, under both roots |
| M14 | the availability sources are not read | survived | caught — a row carrying the availability list alone |
| M17 | the reconnect republish no longer waits for the one-shot refresh | survived | **a defect in the pin** — see below |
| M22 | the shutdown no longer stops discovery | survived | caught, after the ordering moved into `shutdownHAPlane` |
| M29 | a nil document is published | survived | caught — the test asserts haplane's own refusal, not the library's |
| M30 | the migration budget is cut to one publish's | survived | caught — asserted against `publishTimeout`, which is the only thing an in-process stub can compare it to |

**M17 was the finding.** `TestTheReconnectRepublishWaitsForTheOneShotRefresh`
asserted the gate by sleeping 50 ms and then checking the document had not
been published. The pass it was watching for is a 687-message retraction and
a 460 KB document, so it had not finished — and had published nothing yet —
**whether the gate held or not**. Removing the gate entirely left the test
green. A negative asserted against slow work is a negative asserted by
nothing. The fix is to make the observation cheap rather than to wait
longer: the test empties the fleet, so the un-gated pass returns in
microseconds and "it has not returned" can only mean "it is still on the
gate".

**M6/M7 are a masking pair, and they are kept as two.** A publish that
failed is also a publish `publisher.Runtime` does not claim, so removing
either alone changes no verdict and a mutation report reads both as
survivors. They answer different questions and log different reasons, and
the claim check is the one that would still hold if `PublishDeviceBundle`
ever grew a path reporting success without writing. What is pinned is that
removing BOTH is caught — and it was not, at first, for a reason worth
recording: `reconcileOrphans` registers its device synchronously and then
sweeps on a goroutine, so a test reading the window list the instant
`publishDiscovery` returned was racing the subscribe, and losing that race
looks exactly like a sweep that correctly did not run.

**The twenty caught on the first pass:**

| # | Mutation | Caught by |
| --- | --- | --- |
| M1 | `Plane.Reconnect` keeps the old runtime | TestTheRetractionsAreReSentAfterAReconnect, TestPublishOnlineRepublishesEveryAppliancesDocument, TestReconnectOpensTheDedupGateAndRebuildsTheDiscoveryRuntime (+1) |
| M2 | the preflight's verdict is ignored | TestADocumentTooLargeForTheBrokerRetractsNothing, TestTheRefusalBoundaryIsThePacketSizeItMeasures |
| M3 | an unknown limit is read as zero bytes | TestAnUnknownBrokerMaximumIsNotASmallOne |
| M4 | `PacketSize` forgets the topic and the overhead | TestTheRefusalBoundaryIsThePacketSizeItMeasures — **an unnamed masking pair; see [the review round](#adversarial-review-of-454647--five-findings-and-what-they-cost). Split into M4a/M4b/M4c and pinned by derivation.** |
| M5 | the refusal boundary is doubled | TestADocumentTooLargeForTheBrokerRetractsNothing, TestTheRefusalBoundaryIsThePacketSizeItMeasures |
| M9 | a blocking document is published | TestABlockingDocumentIsWithheld |
| M10 | `IsOwnConfig` falls back to the bare namespace with no state topic (**the defect itself**) | TestAStaggeredUpgradeDoesNotDeleteTheSiblingsFleet, TestSweepSparesASiblingInstancesConfigs, TestIsOwnConfig |
| M15 | `HASS_DISCOVERY_REFRESH` no longer clears the device documents | TestRefreshDiscoveryOnceIsFleetWide |
| M16 | a reconnect does not republish discovery | TestPublishOnlineRepublishesEveryAppliancesDocument, TestTheReconnectRepublishWaitsForTheOneShotRefresh |
| M18 | `StopDiscovery` does nothing | TestStopDiscoveryClosesTheShutdownWindow |
| M19 | the device availability payloads are swapped | TestTheDeviceAvailabilityPayloadsAreTheOnesEveryConfigDeclares |
| M20 | the composition root drops the packet-size hook | TestThePlaneConfigStatesEveryFieldTheMigrationDependsOn |
| M21 | the breaker bypass addresses a topic nothing writes | TestHATransportKeepsTheAvailabilityMarkersOffTheBreaker |
| M23 | the retraction form becomes the node-id-less one | TestThePlaneStatesTheDefaultLegacyTopicForm, TestTheMigrationRetractsEveryPerEntityConfigBeforeTheDocument, TestTheCrashWindowHealsOnTheNextBoot (+1) |
| M24 | the document carries no origin block | TestHamqttBundleValidates |
| M25 | `BundleTopic` uses the raw device name | TestAnEmptyDocumentIsWithheld, TestPublishOnlineRepublishesEveryAppliancesDocument, TestRefreshDiscoveryOnceIsFleetWide (+1) |
| M26 | `orphanTopics` forgets what the process declared | TestSweepSparesADeclaredConfigOutsideThisBatch |
| M27 | the republish no longer coalesces | TestTheReconnectRepublishDoesNotOverlapItself — **caught 2 of 12 on re-run; see [the review round](#adversarial-review-of-454647--five-findings-and-what-they-cost). The pin now measures overlap inside the transport and catches it 12/12.** |
| M28 | the documented downgrade topic uses the raw appliance name | TestTheDocumentedDowngradeTopicMatchesTheCode |
| M31 | a nil document reaches the library | TestANilDocumentIsRefusedRatherThanPublished |

M10 is the one to read twice: it is finding F-A written as a mutation, and
it is caught by two tests that DRIVE the sweep and one that questions the
predicate — in that order of value.


---

## Adversarial review of #45/#46/#47 — five findings, and what they cost

Reviewed at `ad80482`. The review found one driven race, two unnamed
masking pairs, a pin that caught its own mutation two times in twelve, and
an understated deferral. All five are closed in one PR. **No golden was
regenerated and no SHA-256 literal moved**; `git diff origin/main --stat --
'*testdata*' '*digest_test.go'` is empty.

The lesson the review itself names, and the one this round was run under:
**a mutation run once can pass once by luck.** Every mutation below was run
5–12 times, and two of the fixes had to be rewritten because their first
version caught the mutation only sometimes — or, worse, caught nothing
while looking like it did.

### F2 — the birth handler was subscribed before the gate it had to respect

#45 added `started`, closed once the one-shot `HASS_DISCOVERY_REFRESH`
migration has finished, and stated the invariant: the first connect's
republish cannot publish a device document into the window the refresh is
about to clear. `republishDiscovery` waited on it. The **other**
asynchronous discovery publisher — the one `StopDiscovery`'s own comment
names as the second of two — did not.

It is not a race that needs bad luck. Home Assistant publishes
`homeassistant/status` **retained** and the handler deliberately keeps
retained deliveries, so the broker replays `online` inline on the SUBSCRIBE
that `Run` performs in `subscribeCommands` — *before* `refreshDiscoveryOnce`
and before `started` closes. With the flag on, the boot became: birth
replay writes 687 retractions and the ~473 KB document → Home Assistant
creates 687 entities → the refresh **retracts the document** → Home
Assistant removes the device and all 687 → 3 s settle → the republish
re-creates them. The end state is correct, which is why nothing failed. The
cost is a whole extra migration per appliance per boot, two concurrent
fleet-wide snapshot windows, and a materially wider window in which the
appliance has **no** discovery config — made permanent by a shutdown or a
dropped link inside it.

The narrow interleaving the reviewer constructed but did not drive is
closed by the same fix rather than argued about: `Runtime.Publish` writes
the transport and *then* takes `r.mu` to record `declared`, so a birth
pass's document write landing before the refresh's retract while its
`declared` update lands after leaves the broker holding a retraction the
runtime claims as a document — `PublishBundle` then dedups, writes nothing,
returns `(false, nil)`, `documentIsDeclared` is true, and the sweep clears
the per-entity leftovers too. Fleet absent, with
`hass.bundle_published written=false` as the only evidence. Both callers now
run the one pass, and `republishMu` serialises them, so that interleaving
has no two writers to build itself out of.

**The test drives `Run`, in `Run`'s order, and reads the verdict at publish
time.** `subRecorder` asks a gate closure on every PUBLISH and records the
answer on the call, because the question "was this published before the
gate opened?" cannot be answered by reading the call list afterwards: a
snapshot taken when the gate opens races every legitimate publish that
follows it, and both outcomes of that race look like a pass.

Two things had to be got right for the pin to fail reliably, and the first
version got neither:

- **Attribution.** Everything written before the gate is compared against
  the migration's own output. The test seeds a retained tree holding no
  discovery config, so the refresh's entire output is one retraction per
  device document; anything else in that window came from the birth replay.
- **Fixture size.** Over the 687-entity catalogue an un-gated birth pass
  spends longer *rendering* its document than the whole migration takes, so
  its writes land after the gate by accident and the defect hides behind
  its own cost — the mutation survived 8 of 8 runs. With a two-entity
  appliance the un-gated pass is writing within microseconds of the
  SUBSCRIBE, which is where the defect actually lives.

| # | Mutation | Runs | Result |
| --- | --- | ---: | --- |
| M32 | the birth handler loops over `b.devices` itself again (the defect) | 10 | caught 10/10 by `TestTheBirthReplayCannotPublishIntoTheRefreshWindow` |
| M32 | as above, both F2 pins running | 10 | caught 10/10 |

`TestRefreshDiscoveryOnceIsFleetWideAndOnlyBeforeAnythingIsPublished` named
the "only before anything is published" half in its title and never drove
`Run` — it called `refreshDiscoveryOnce` in isolation. That half was prose
nobody measured, and the code contradicted it. The test is renamed
`TestRefreshDiscoveryOnceIsFleetWide`, and the half it was claiming now
lives in a test that drives `Run`.

### F1 — `publishOverhead` was the whole safety margin and nothing read it

`const publishOverhead = 64` is the entire margin between "the preflight
says it fits" and go-mqtt's `mqtt.ErrPacketTooLarge`, which is raised inside
`writeFrame` **after `supersede` has put all 687 retractions on the wire**.
Setting it to `0` left all thirteen packages green;
`git grep publishOverhead -- '*_test.go'` returned nothing.
`TestTheRefusalBoundaryIsThePacketSizeItMeasures` asserted only
`PacketSize(topic, n) > n`, which the `len(topic)` term satisfies alone — so
**M4 ("PacketSize forgets the topic *and* the overhead") was an unnamed
masking pair of the same shape as M6/M7, recorded as a clean catch.**

It was also **spelled twice**: `haplanePacketSize(topic, payloadLen) =
payloadLen + len(topic) + 64` in `internal/bridge/bundle_test.go`, declared
in its own doc comment as a deliberate copy, feeding the `472 953 on the
wire` figure in the notes and the PR body. That copy is *why* the constant
was unpinned: the only other place the number appeared could not disagree
with it. Third instance of this shape in the programme, so it is fixed **by
derivation, not by a second literal**:

- `TestPublishOverheadCoversARealPublishOnTheWire` encodes a real retained
  QoS 1 MQTT 5.0 PUBLISH with **go-mqtt's own encoder** and requires
  `PacketSize` to be at least as large, over five shapes including the
  measured document and the varint boundary.
- `TestPacketSizeCountsTheTopicAndTheOverheadSeparately` splits M4 into its
  two terms, each asserted by the difference only it can explain.
- The test-local copy is deleted; the measured figure now comes from
  `haplane.PacketSize` and is unchanged at **472 953**.

| # | Mutation | Runs | Result |
| --- | --- | ---: | --- |
| M4a | `publishOverhead` = 0 | 5 | caught 5/5 |
| M4b | `publishOverhead` = 8 (below the real ~10-byte cost) | 5 | caught 5/5 |
| M4c | `PacketSize` drops the `len(topic)` term | 5 | caught 5/5 |

### F3 — the coalescing pin caught its own mutation 2 times in 12

`M27` was recorded as caught. Re-run twelve times, deleting the
`republishPending`/`republishMu` block survived **10 of 12**: the assertion
was `len(discoveryWindows) > 2` after four concurrent passes over a
one-appliance fixture, and two other mechanisms suppress that concurrency
already — `b.reconciling`'s per-device gate and `PublishBundle`'s dedup — so
the pin was inert ~83 % of the time.

**The coalescing itself is correct**; the reviewer attacked the flag/lock
ordering for lost wake-ups and could not break it. What was fixed is the
pin, and the fix is to measure the property directly: **overlap lives in
the transport**, so each publish of the device document is now held inside
the stub and the test counts how many are in there at once. Two passes that
ran together are two publishes inside it; two that ran in sequence are
never more than one. The 687-entity fixture had to go for the same reason
as in F2 — four passes each spend a second rendering before they write, so
they arrive a second apart and the first one's declaration deduplicates the
rest.

| # | Mutation | Runs | Result |
| --- | --- | ---: | --- |
| M27 | the republish no longer coalesces — old pin, old fixture | 12 | **survived 10/12** |
| M27 | the same mutation, overlap measured in the transport, 687-entity fixture | 12 | caught 8/12 |
| M27 | the same mutation, overlap measured in the transport, two-entity fixture | 12 | **caught 12/12** |

### F4 — the tombstone deferral, quantified and acted on

The trigger is not firmware. Entities come from a local JSON description
file, so replacing the file or the appliance moves it — and the cheap
trigger is one add-on option: `HASS_DISCOVERY` is `full | curated`, pinned
at 687 and 177, so **flipping `full` → `curated` leaves 510 phantom
components per appliance**.

They are not inert. `curated` is read only inside `internal/hass`; the state
plane never sees it. So each of the 510 keeps its retained per-entity
config, keeps **both** availability sources (`<root>/status` and
`<root>/<device>/connected`, both `online`) **and keeps receiving live
state** — indistinguishable in Home Assistant from a real entity. The
operator who set `curated` to reduce clutter sees **no change at all**,
which is the worst outcome an option can have: it appears to do nothing, so
it is set again.

**Decision: document it and warn at boot; do not implement tombstones in
this PR.** The middle option of the three the review names, and the
reasoning is the constraint plus the failure direction:

1. **The best option moves bytes this PR may not move.** Tombstoning the
   omitted components means writing 510 extra keys into the curated
   document — a different payload, so the curated golden and its digest
   move. This PR is a review fix run under "no golden may be regenerated
   and no SHA-256 literal may move". A change that rewrites the document
   Home Assistant reads deserves its own PR with the golden diff reviewed
   line by line, not a rider on a race fix.
2. **The warning needs nothing the deferral lacked.** #45 deferred
   tombstones because there is no memory of the previous document and the
   only restart-surviving source is the broker — a read-back whose
   mis-parse writes tombstones for *live* components. The curated omission
   needs none of that: both sets are computed locally from the same
   entities in the same pass, so the number is exact, costs no wire traffic
   and cannot delete anything.
3. **It is the half that was actually missing.** "An omitted component is
   not removed" understates the cost; a count per appliance, in the log and
   in the option's own documentation, is the part an operator can act on.

So `hamqttModel` counts what the curated filter dropped and
`warnCuratedOmissions` says it once per appliance per process —
`WARN hass.curated_components_omitted device=… omitted=510 published=177`,
naming the manual removal — and the option's own documentation in
`addon/DOCS.md`, `README.md` and both changelogs now says what flipping it
costs. `addon/DOCS.md`'s manual remedy was already accurate, including the
restart step go-mtec2mqtt's documentation missed; it is kept and the price
is stated next to it.

The test derives the number rather than writing it down: it renders the
full and the curated document from the same entities, subtracts, and
requires the warning to carry exactly that. On the pin catalogue it is
**510 of 687**.

| # | Mutation | Runs | Result |
| --- | --- | ---: | --- |
| M33 | the omission count is computed and never reported | 6 | caught 6/6 (whole `internal/hass` suite) |
| M34 | the once-per-appliance guard is dropped (a warning per republish) | 6 | caught 6/6 |
| M35 | `omitted` and `published` are swapped in the warning | 6 | caught 6/6 |

### F5 — the two smaller ones

**The third unnamed masking pair.** Removing `|| b.discoveryStopped.Load()`
from `republishDiscovery` survived the suite, masked by the same check in
`publishDiscovery`. It is genuinely equivalent for what is *published* — and
not equivalent at all for what is *waited on*, which is the part nobody had
named: `republishDiscovery` reads the flag **before** it waits on `started`,
and `publishDiscovery` can only read it after. A shutdown that arrives
before `Run` has finished the one-shot migration therefore leaves the pass
parked on a gate that will never open, holding the birth handler's
goroutine and, behind `republishMu`, every later pass, for the life of the
process. `TestAStoppedDiscoveryRepublishDoesNotParkOnTheStartGate` asserts
that the call **returns**, which is the one thing the inner check cannot
provide.

**A whole-file `strings.Contains`.** The `-h `/`-u `/`-P ` check in
`TestTheDocumentedDowngradeTopicMatchesTheCode` ran over the entire file
rather than the `mosquitto_pub` line, so any unrelated `-u ` elsewhere in a
document would mask a credential going missing from the command an operator
pastes. It now extracts each `mosquitto_pub` invocation, folding shell
line-continuations, and checks the flags inside it. The masking was
confirmed, not assumed: the same mutation run against the pre-fix test
survived.

| # | Mutation | Runs | Result |
| --- | --- | ---: | --- |
| M36 | `\|\| b.discoveryStopped.Load()` removed from `republishDiscovery` | 8 | caught 8/8 |
| M37 | `addon/DOCS.md` loses `-u`/`-P` from the command, gains an unrelated `-u ` in prose | 6 | caught 6/6 |
| M37 | the same mutation against the pre-fix whole-file check | 1 | **survived** (the masking, confirmed) |



---

## Step 8 outcome — tombstones, and the read-back that makes them possible

This is the change #48 deferred *to* its own PR, and the deferral's three
stated reasons are answered here rather than restated.

### What an operator sees

Before: flipping `HASS_DISCOVERY` from `full` to `curated` left **510
phantom components per appliance**. Not inert — `curated` is read only
inside `internal/hass`, so each of the 510 kept its retained per-entity
registry entry, kept **both** availability sources publishing `online` and
kept **receiving live state**. In Home Assistant they were
indistinguishable from real entities, and the operator who set the option to
reduce clutter saw no change at all.

After: the same 510 are removed. And the trigger surface is the general one
— a feature excluded in `mapping.yaml`, a replaced description file, a
different appliance — not just the option flip.

### The mechanism, and the one place it diverges from go-daikin2mqtt

`discovery.Bundle.RemoveComponents` writes `{"platform":"…"}` and keeps the
`unique_id` in `Bundle.Tombstones` (`json:"-"`), which is what lets
`publisher.SupersededTopics` still retract the removed entity's old
per-entity config. That half is go-daikin2mqtt PR #80's, unchanged.

The read-back is **not**. daikin reads its prior documents through a
`ReportOnly` `publisher.Runtime.Sweep`, whose filter is hard-wired to
`<prefix>/#`. Two reasons that does not transfer:

- On this bridge `<prefix>/#` replays all 687 retained per-entity configs
  and the ~473 KB document itself, per appliance, **on the one path this
  release cannot undo**. `<prefix>/device/+/config` replays one message per
  appliance.
- It is the SAME filter the post-publish orphan sweep uses, and the
  SUBSCRIBE list is what pins that sweep as *not having run*
  (`TestTheSweepIsSkippedWhenTheDocumentWasNotPublished`). A read-back
  sharing the filter makes that pin unable to fail — confirmed: it is what
  M11 mutates, and it turns the sweep pin red.

So `haplane.Plane.Snapshot` is `Runtime.snapshot` without the sweep: one
narrow window, the same teardown discipline (`context.WithoutCancel`, 5 s),
the same QoS resolution — plus one thing the sweep's has no use for. Its
visitor reports whether the caller has everything it came for, and the
window closes the moment it does. The read-back sits in front of the
migration, so a steady-state boot must not hold a subscription open for two
seconds after its documents have already arrived. A window nothing completes
still runs out its time, because MQTT has no end-of-retained signal and that
is the only answer available when the broker holds nothing.

### Can a live component be tombstoned? — the question, answered

An adversarial review of go-daikin2mqtt #78/#79/#80 proved the "fewer, never
different" claim **breaks in one direction**: where two instances share a
device node id, instance B read A's document, did not recognise it as
foreign, and tombstoned A's **live** components — removed from the entity
registry, with dashboards, automations, area and rename lost, then restored
and removed again in a permanent ping-pong.

This bridge meets the same precondition. F8: two daemons with different
`MQTT_TOPIC` roots, the same `HASS_BASE_TOPIC` and an appliance name in
common produce byte-identical node ids, `unique_id`s, `identifiers[0]` and
`default_entity_id`s, so they address the **same** document topic and
nothing about the topic can tell them apart.

What this bridge has that daikin did not is `MQTT_TOPIC` **inside every
component**. `state_topic` and `command_topic` are `<root>/…`, and since F1
every component carries the two-source availability list, both under
`<root>`. Measured on the pin catalogue: every one of the 687 components
names at least one topic (667 a `state_topic`, the 20 buttons a
`command_topic`), and all of them carry the availability list. So the
document is attributable component by component, by exactly the rule the
orphan sweep already uses — `Discovery.IsOwnConfig`, as fixed for F8's
`button` hole.

`Discovery.BundleComponents` applies it per component, so a component that
is not provably ours never becomes prior state. Three answers follow:

1. **A live component of THIS instance is never tombstoned**, because
   `ApplyTombstones` subtracts against `b.Components` — a key the new
   document declares is never in the gone set, so `RemoveComponents` is
   never handed one.
2. **A live component of a SIBLING instance is never tombstoned**, because
   its topics are under another root.
3. **Every other failure direction produces fewer tombstones, never
   different ones**: a window that sees nothing, a document that does not
   parse, a truncated one, one with no `components` key, an empty map, a
   component that is not an object, a component with no platform, a
   tombstone this daemon wrote itself, a foreign `unique_id` namespace, and
   a component naming no topic at all — all yield nil prior state, and the
   publish proceeds exactly as it would have without the read.

Both sibling cases are DRIVEN, not asserted against the predicate
(`TestTheReadBackIgnoresASiblingsDocument`, `TestASiblingsWholeDocumentIsDeclined`),
which is the lesson every bad defect in this programme has taught.

### Where the state lives

On the `publisher.Runtime` it was read through, not on a flag. That runtime
is rebuilt by `haplane.Plane.Reconnect` at the head of every (re)connect, so
the memo is **self-invalidating** on the next connection: no flag for a
caller to forget, and no window in which a reconnect races a load. It is
also why `publishDiscovery` reads `Plane.Runtime()` ONCE and carries it
through — a pass that straddles a reconnect must not write its result into
the new connection's memory, and re-reading the runtime afterwards is
exactly how it would.

### The gates run on the document that is published

The same review found that daikin's size preflight and validator inspected
the document *before* `RemoveComponents` added its entries — a gate on the
wrong artefact, and the miss is precisely the fleet-wide deletion the gate
exists to prevent. Here `PublishDeviceBundle` applies the tombstones and
*then* calls `publishBundle`, so `discovery.Validate` and
`haplane.Plane.preflight` both marshal the finished shape.
`TestThePreflightMeasuresTheDocumentTheRemovalsProduced` drives a broker
limit chosen **between** the two sizes, which is the only interval in which
the two orderings give different answers. Measured: **39.8 bytes per
removal** on the small fixture.

The empty-document gate moved with it. It read `len(b.Components) == 0`, and
a tombstone IS an entry — an appliance that classified to zero entities
against a prior document of 687 would render 687 entries declaring nothing
and walk straight through. It now counts LIVE components.

### The measurement

| | full (`de`) | curated (`de`) | curated after the flip |
| --- | ---: | ---: | ---: |
| Components declared | 687 | 177 | 177 |
| Removals carried | 0 | 0 | **510** |
| Document payload | 472 847 | 122 900 | **157 548** |
| On the wire | 472 953 | 123 006 | **157 654** |
| `discovery.Validate` | clean | clean | **clean** |
| Per-entity configs superseded | 687 | 177 | **687** |

The removals are carried on **one** connection: the document that went out
is the memo, and a tombstone is not prior state, so the next pass declares
177 and the document falls back to ~123 KB. The full document does not move
at all, because a `full` render omits nothing.

`discovery.Validate` accepts **687 of 687** in all four configurations,
unchanged, and the tombstoned document validates clean rather than merely
non-blocking.

### The pins — nothing moved, and that is proved as OUTPUT

**No golden was regenerated and no SHA-256 literal moved.** #48's stated
reason for deferring — "510 extra keys in the curated document moves the
curated golden and its digest" — turns out not to hold, and the reason is
worth recording: the five files in `internal/hass/testdata/` pin the
**per-entity** form, and a tombstone lives only in the bundle artefact,
which this repository has no golden for.

Proved as output rather than as files: `-update-discovery-golden` and
`-update-topics-golden` were run in a worktree of `origin/main` and in a
copy of this branch, and the six regenerated files are byte-identical across
the two — and equal to the six literals already pinned:

```
f1bcd7c4…  discovery_curated_de.json
ad39e602…  discovery_full_de.json
248d2d26…  discovery_full_en.json
9120a863…  discovery_plain_en.json
a725b3b2…  identity_en.json
37e5bc78…  topics.json
```

### #48's warning

Kept, and its text corrected. The count is more consequential than it was,
not less — flipping the option now **deletes** that many entities, with
their recorder history and everything that references them — so the line
stays at WARN. What was removed is the remedy it named: "restart Home
Assistant, then delete them on the device page" is advice for a hazard that
no longer exists, and following it now does nothing.
`TestTheCuratedWarningDescribesTheBehaviourThisReleaseHas` pins the absence
of the stale text as well as the presence of the new, because a warning is
only worth what its text is worth (M12).

### Mutation proof

Sixteen mutations, applied one at a time to a `cp -a` copy of the committed
tree and each run against the whole suite. **Thirteen caught on the first
pass, three survived, and all three survivors were defects in a pin rather
than gaps in coverage.**

| # | Mutation | Runs | Result |
| --- | --- | ---: | --- |
| M1 | a tombstone may overwrite a LIVE component | 1 | caught |
| M2 | the read-back does not check ownership (a sibling's document is tombstoned) | 8 | **survived the driven tests on the first pass**; caught 8/8 after the fixture was fixed |
| M3 | a tombstone is carried forward as prior state (the platform filter) | 3 | **survived 1/1**, masked by M2's guard; caught 3/3 once isolated |
| M4 | `LiveComponents` keeps tombstones | 1 | caught |
| M5 | the empty gate counts entries again, not live components | 1 | caught |
| M6 | the gates run on the document BEFORE the removals are written in | 6 | caught 6/6 |
| M7 | the prior memo is cached across connections | 6 | caught 6/6 |
| M8 | `record` does not check which connection produced the result | 8 | **survived 0/5**; caught 8/8 after the pin was rewritten |
| M9 | a refused document is remembered as the previous one | 5 | caught 5/5 |
| M10 | the read-back runs AFTER the migration | 8 | caught 8/8 |
| M11 | the read-back shares the sweep's `<prefix>/#` filter | 1 | caught |
| M12 | the curated warning keeps #48's remedy for a hazard that is gone | 1 | caught |
| M13 | a tombstone carries the `unique_id` back into the payload | 1 | caught |
| M14 | a document that proved nothing is reported as an empty map | 1 | caught |
| M15 | the window never stops early and always pays its full budget | 5 | caught 5/5 |
| M16 | the read-back stops at the FIRST document, whoever it belongs to | 6 | **survived 0/6**; caught 6/6 after the fixture gained a second appliance and an asynchronous replay |

**M2 is the one to read twice.** It was caught 8/8 by the predicate table
and **not at all** by either of the two tests that DRIVE a sibling's
document through `publishDiscovery` — the exact inversion of the lesson this
programme keeps learning. The cause was the fixture: `siblingConfig` carries
no `platform`, because it was written for per-entity configs whose platform
lives in the topic. Inside a document the platform is a key, so both sibling
tests were declining their fixture for the wrong reason and would have
passed with the ownership rule deleted. `siblingComponent` carries one, and
the two driven tests now fail 8/8.

**M8 was inert for an ordering reason.** The first version of
`TestAPassThatStraddlesAReconnectDoesNotWriteTheNewConnectionsMemo` recorded
the dead connection's result *before* the new connection had read anything,
so the read simply overwrote it and the guard was never consulted. The
hazard is the other order — a straddling pass that returns after the new
connection has already loaded — and that is what the test now drives.

**M16 needed two fixtures to become visible.** A window that stops at the
first delivery is indistinguishable from a correct one over ONE appliance,
and the cost of the defect is in the safe direction — the second appliance
simply keeps its phantoms — so nothing else in the file noticed. Two things
had to change: a second appliance, and a retained replay that is
ASYNCHRONOUS. `subRecorder` replays its retained tree inline inside the
SUBSCRIBE, which is what every other pin here needs (a window that opened
after the replay sees nothing) and which also delivers everything before the
first handler call returns — so "the window waited for the second message"
was a property with no second message to wait for. It is now the third time
in this programme that a pin was inert because of its FIXTURE rather than
its assertion.

**M3 is a masking pair with M2**, kept as two: `IsOwnConfig` rejects a
platform-only entry anyway (no `unique_id`), so deleting the platform filter
alone changed no verdict. The table row that isolates it now names a
component that is ours by identity AND by every topic it carries, so the
missing platform is the only thing that can refuse it.

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
| 4 | **DONE (see [Step 4 outcome](#step-4-outcome--the-byte-equality-experiment)).** **Render through go-hamqtt, publishing nothing.** Add `internal/hass/hamqtt.go` building a `discovery.Bundle` from the same entities, behind no config flag, and a test that compares its per-component output against `testdata/discovery_*.json` — **against the files, not against the builder it replaces**. Neither pin regenerated. | This is where a `Layout`, `Context` or `Slot` mismatch surfaces, at zero risk. |
| **4.5** | **DONE (see [Step 5 outcome](#step-5-outcome--f13-fixed-687-of-687-accepted)).** **Fix F13** — `deviceClassAllowed` reads go-ha-catalog's per-platform tables instead of trusting the catalogue on `sensor`/`number`, and enrichment refuses (and logs) an override the platform does not declare. Three goldens and three digests move, 11 rows each, no identity string. | F13 must be fixed before step 6: a bundle is validated as one document and a blocking one publishes nothing, so eleven silently-dropped entities become 687 lost ones. |
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
- **Do not move one of the thirteen to `binary_sensor` to "match the
  integration".** It strands the entity (`unique_id` and `identifiers`
  have no migration path) *and* it is wrong: these land on `sensor`
  because the appliance does not model them as booleans, and a
  `binary_sensor` needs a two-valued mapping this bridge cannot derive.
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
