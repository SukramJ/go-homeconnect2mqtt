# Home Connect — Feature Catalogues & MQTT/HA Mapping

> Complete feature catalogue (verbatim from `homeconnect_local_hass`
> `entity_descriptions/`) for **BSH.Common** plus the three target appliances
> **Dishwasher, Induction Hob, Washer**, along with the recommended
> mapping onto MQTT topics and Home Assistant discovery.
>
> The HA integration uses a **curated allowlist** (fixed feature names → platform).
> `go-homeconnect2mqtt`, by contrast, should map **all features** of the appliance generically
> onto MQTT (more resilient, more complete) and use these catalogues only for **enrichment**
> (device_class, unit, sensible defaults, appliance-specific start paths).

---

## 1. Mapping Framework (HA Platform Heuristic)

The HA integration binds each "EntityDescription" to one fixed feature name (`entity`)
or several (`entities`). An entity is created only if **all** referenced features are present in the
appliance. Plus **dynamic generators** (callables that derive at runtime from the
appliance).

**Platform assignment (heuristic, transferable to MQTT/HA discovery):**

| Platform | Source | Criterion |
|---|---|---|
| `switch` | Setting/Option | boolean; optional `value_mapping=(on,off)` for enum switches (e.g. PowerState On/Off) |
| `select` | Setting/Option | enum, `access ∈ {readwrite, writeonly}` |
| `sensor` | Status/Option | read; `device_class=ENUM` for enum, otherwise DURATION/TEMPERATURE/PERCENTAGE/SIGNAL_STRENGTH |
| `binary_sensor` | Event/Status | `value_on`/`value_off` sets (events: on=`{Present,Confirmed}`, off=`{Off}`; door: open/closed) |
| `number` | Setting/Option | numeric, writable; min/max/step **dynamically from the entity** (MinMax), description only a fallback |
| `button` | Command | e.g. `…Command.AbortProgram`; `start_button` only if a program with `Execution.SELECT_AND_START` exists |
| `event_sensor` | multiple events | combined (e.g. salt: `SaltLack`+`SaltNearlyEmpty` → `empty/nearly_empty/full`) |
| `light`/`fan` | dynamic | hood/ambient light or hood fan stages |

**Value conventions:** `unique_id = "<deviceID>-<key>"`; enum values resolved via FeatureMapping;
`has_state_translation` ⇒ lowercased values; program name = `program.lower().replace(".","_")`.

---

## 1b. Enrichment & curation catalogue (`mapping.yaml`)

Every feature is still exposed; the catalogue only refines the Home Assistant
discovery payload. It is keyed by feature name; each entry is optional and an
unset field falls back to the heuristic:

| field | effect |
|---|---|
| `name` / `name_de` | localized friendly **name** (en / de). Entity **ids** stay English (seeded via `default_entity_id`, the replacement for the removed `object_id`). |
| `device_class`, `unit` | HA `device_class` / `unit_of_measurement`. |
| `state_class` | `measurement` / `total` / `total_increasing` for sensor statistics. |
| `entity_category` | `diagnostic` / `config`; empty = primary (shown on top). |
| `enabled_by_default` | tri-state; overrides the primary heuristic. |
| `exclude` | never expose this feature. |

**Localized values.** Enum/dropdown member values are localized per `LANGUAGE`
too: `select` options, enum-sensor options and the published state carry the
localized label (HA's native enum translations aren't available to MQTT
discovery), and the write path accepts the localized label. Entity ids stay
English. Common member names ship translated (`internal/i18n`); uncatalogued
values pass through unchanged, keeping options and state consistent.

**Decluttering policy.** A small primary set (power/operation/door state,
active/selected program, remaining time/progress, key events) is enabled and
uncategorized. The long tail is published **disabled-by-default** (one click to
enable in HA) and categorized diagnostic/config — so a device drops from ~195 to
~28 entities shown, without dropping the "expose everything" promise.
`HASS_DISCOVERY: curated` instead publishes only the primary set.

**Orphan cleanup.** When a device's feature set changes (exclusions, renames, or
switching to curated), the bridge clears its own now-orphaned retained discovery
configs — matched by `unique_id`/state-topic ownership so other integrations are
never touched — so Home Assistant doesn't keep them as unavailable entities.

---

## 2. BSH.Common — Cross-Appliance Features

Applies (partly) to **all** appliances. (`common.py`)

### Sensors (Status/Option, read)
| key | Feature | device_class / unit |
|---|---|---|
| operation_state | `BSH.Common.Status.OperationState` | ENUM |
| power_state (sensor) | `BSH.Common.Setting.PowerState` | ENUM |
| door_state | `BSH.Common.Status.DoorState` | ENUM (sensor, if >2 enum values) |
| remaining_program_time | `BSH.Common.Option.RemainingProgramTime` | DURATION/s (+ attr `RemainingProgramTimeIsEstimated`) |
| estimated_total_program_time | `BSH.Common.Option.EstimatedTotalProgramTime` | DURATION/s |
| elapsed_program_time | `BSH.Common.Option.ElapsedProgramTime` | DURATION/s |
| program_progress | `BSH.Common.Option.ProgramProgress` | % |
| start_in | `BSH.Common.Option.StartInRelative` | DURATION/s |
| finish_in | `BSH.Common.Option.FinishInRelative` | DURATION/s |
| water_forecast | `BSH.Common.Option.WaterForecast` | % |
| energy_forecast | `BSH.Common.Option.EnergyForecast` | % |
| flex_start | `BSH.Common.Status.FlexStart` | ENUM |
| end_trigger | `BSH.Common.Status.ProgramRunDetail.EndTrigger` | ENUM |
| count_started | `BSH.Common.Status.Program.All.Count.Started` | TOTAL_INCREASING (+ ProgramSessionSummary.Latest start/end) |
| count_completed | `BSH.Common.Status.Program.All.Count.Completed` | TOTAL_INCREASING |
| wifi_signal_strength | `BSH.Common.Status.WiFiSignalStrength` | SIGNAL_STRENGTH/dBm (diagnostic) |

### Binary Sensors (Status/Event)
| key | Feature | device_class |
|---|---|---|
| door_state | `BSH.Common.Status.DoorState` | DOOR (on=`{Open,Ajar}`, off=`{Closed,Locked}`) |
| aqua_stop | `BSH.Common.Event.AquaStopOccured` | PROBLEM |
| low_water_pressure | `BSH.Common.Event.LowWaterPressure` | PROBLEM |
| remote_start_allowed | `BSH.Common.Status.RemoteControlStartAllowed` | (diagnostic) |
| program_aborted | `BSH.Common.Event.ProgramAborted` | PROBLEM |
| program_finished | `BSH.Common.Event.ProgramFinished` | (on=`{Present,Confirmed}`, off=`{Off}`) |
| interior_illumination | `BSH.Common.Status.InteriorIlluminationActive` | (diagnostic) |
| alarm_clock_elapsed | `BSH.Common.Event.AlarmClockElapsed` | |

### Switches / Selects / Numbers
| key | Feature | Platform |
|---|---|---|
| child_lock | `BSH.Common.Setting.ChildLock` | switch |
| remote_control_level | `BSH.Common.Setting.RemoteControlLevel` | select (config) |
| temperature_unit | `BSH.Common.Setting.TemperatureUnit` | select (if >2 values) |
| duration | `BSH.Common.Option.Duration` | number DURATION/s |
| start_in | `BSH.Common.Option.StartInRelative` | number DURATION/s |
| finish_in | `BSH.Common.Option.FinishInRelative` | number DURATION/s |
| alarm_clock | `BSH.Common.Setting.AlarmClock` | number DURATION/s |

### Buttons (Commands)
`BSH.Common.Command.AbortProgram` · `PauseProgram` · `ResumeProgram` · `MainsPowerOff` ·
`AcknowledgeEvent` · `RejectEvent`

### Dynamic Generators (important for understanding)
- **Power switch/select** (`generate_power_switch`): from the `BSH.Common.Setting.PowerState` enum,
  form the switchable states (filtered via min/max). Exactly 2 states matching one of the
  pairings → **switch** with `value_mapping`:
  ```
  POWER_SWITCH_VALUE_MAPINGS = ("On","MainsOff") | ("Standby","MainsOff") | ("On","Off")
                               | ("On","Standby") | ("Standby","Off")
  ```
  otherwise → **select** (all states, lowercased).
- **Program** (`generate_program`): select `BSH.Common.Root.SelectedProgram` + sensor
  `BSH.Common.Root.ActiveProgram`; program names sorted; favourites
  (`BSH.Common.Program.Favorite.<x>`) resolved via `BSH.Common.Setting.Favorite.<x>.Name`.
- **Start button** (`generate_start_button`): only if a program has `Execution.SELECT_AND_START`
  → button on `BSH.Common.Root.ActiveProgram` (uid 256 — see FK-5 in `05-resilience.md`).
- **WiFi sensor**, **temperature-unit select**: fallbacks.

---

## 3. Dishwasher — `Dishcare.Dishwasher.*` (`dishcare.py`)

### Binary Sensors (events, all PROBLEM/diagnostic, on=`{Present,Confirmed}` off=`{Off}`)
`Status.EcoDryActive` · `Event.MachineCareReminder` · `Event.LowVoltage` ·
`Event.MachineCareAndFilterCleaningReminder` · `Event.WaterheaterCalcified` ·
`Event.SmartFilterCleaningReminder` · `Event.CheckFilterSystem` · `Event.DrainingNotPossible` ·
`Event.DrainPumpBlocked` · `Event.FlexSpray.Error.{Blocked,General,SprayArmNotMounted}`
*(FlexSpray = Bosch "PowerControl")*

### Event Sensors (combined → `empty`/`nearly_empty`/`full`)
- Rinse aid: `Event.RinseAidLack` + `Event.RinseAidNearlyEmpty`
- Salt: `Event.SaltLack` + `Event.SaltNearlyEmpty`

### Select (Settings/Options, config)
`Setting.DryingAssistantAllPrograms` · `Setting.HotWater` · `Setting.RinseAid` ·
`Setting.SoundLevelSignal` · `Setting.SoundLevelKey` · `Setting.WaterHardness` ·
`Setting.SensitivityTurbidity` · `Setting.EcoAsDefault` ·
`Option.FlexSpray.{Type,FrontLeft,BackLeft,BackRight,FrontRight}` ·
`Setting.FlexSpray.Custom.{FrontLeft,BackLeft,BackRight,FrontRight}`

### Sensor
`Status.ProgramPhase` (ENUM)

### Switch (Options = per program; Settings = config)
`Option.ExtraDry` · `Option.HygienePlus` · `Option.IntensivZone` · `Option.VarioSpeedPlus` ·
`Option.SilenceOnDemand` · `Option.BrillianceDry` · `Option.ZeoliteDry` (CrystalDry) ·
`Option.HalfLoad` · `Option.ExtraRinse` · `Setting.ExtraDry` · `Setting.SpeedOnDemand` ·
`Setting.InfoLight`

**Start/Delayed start:** standard path (`/ro/activeProgram`), delayed via
`BSH.Common.Option.StartInRelative`. On `400`, retryable (#322).

---

## 4. Induction Hob — `Cooking.Hob.*` / `Cooking.Common.*` (`cooking.py`)

> The most fragile setup type (FK-3). Zones are detected **dynamically via regex**.

### Dynamic Zone Sensors — `generate_hob_zones`
Regex: `^Cooking\.Hob\.Status\.Zone\.(\d+)\..*$` → one sensor per zone `<n>` (1-based),
provided the feature exists:

| Suffix | device_class / unit | extra |
|---|---|---|
| `.State` | ENUM | + attr `Cooking.Hob.Status.Zone.<n>.Type` |
| `.OperationState` | ENUM | |
| `.PowerLevel` | ENUM | |
| `.FryingSensorLevel` | ENUM | |
| `.CurrentTemperature` | TEMPERATURE/°C | |
| `.HeatupProgress` | % | |
| `.Duration` | DURATION/s | |
| `.ElapsedProgramTime` | DURATION/s | + attr `.AutoCounting` |
| `.RemainingProgramTime` | DURATION/s | + attr `.AutoCounting` |
| `.ProgramProgress` | % | |

⚠️ **#395/#368 bug class:** match the group ID strictly as `(\d+)`, ignore non-numeric matches
(`int("001.RemainingProgramTime")` crashes). Per-zone creation with an existence guard.

### Static Cooking Sensors/Settings
- Hood: `Setting.IntervalTimeOff/On`, `Setting.DelayedShutOffTime`,
  `Status.GreaseFilterSaturation` (%), `Status.CarbonFilterSaturation` (%)
- Oven (if present): `Option.HeatupProgress` (%),
  `Status.CurrentCavityTemperature`/`CurrentMeatprobeTemperature` (°C),
  WaterTank (`Status.WaterTankUnplugged`+`WaterTankEmpty` → `unplugged/empty/ok`)

### Select / Switch (Hob/Hood relevant)
- `Cooking.Hob.Setting.Ventilation` (select, config)
- `Cooking.Hood.Setting.IntervalStage` · `DelayedShutOffStage` · `CarbonFilterType` (select)
- `Cooking.Common.Option.Hood.Boost` (switch) · `Cooking.Hood.Setting.NoiseReduction` (switch)
- Oven settings: `Option.SetpointTemperature` (number °C), `Setting.DisplayBrightness`,
  selects `Option.Level/UsedHeatingMode/PyrolysisLevel`, switches `Option.FastPreHeat`,
  `Setting.{ButtonTones,OvenLightDuringOperation,SabbathMode}`

### Hood — dynamic
- **Fan** (`generate_hood_fan`): from `Cooking.Common.Option.Hood.VentingLevel` /
  `IntensiveLevel`. ⚠️ Fan off via **DELETE** `/ro/activeProgram` (#386).
- **Light** (`generate_hood_light`): `Cooking.Common.Setting.Lighting` (+ `LightingBrightness`,
  `Cooking.Hood.Setting.ColorTemperaturePercent`).
- **Ambient light** (`generate_hood_ambient_light`): `BSH.Common.Setting.AmbientLight*`.

### Filter Reset Buttons
`Cooking.Common.Command.Hood.{CarbonFilterReset,GreaseFilterReset,RegenerativeCarbonFilterReset,RegenerativeCarbonFilterLifeTimeReset}`

**Start (Hob):** ⚠️ direct `POST /ro/selectedProgram` (`validate=false`); the standard start
crashes with `NoneType.start` if nothing is preselected (#385). On-device confirmation is often required (#111/#261).

---

## 5. Washer — `LaundryCare.{Common,Washer,Dryer}.*` (`laundry_care.py`)

### Sensors
`Common.Status.Laundry.Reload` (ENUM) · `Common.Option.ProcessPhase` (ENUM) ·
`Dryer.Option.ProcessPhase` (ENUM) · `Washer.Option.SpinSpeed` (ENUM) ·
`Common.Option.LoadRecommendation` (WEIGHT/g→kg) ·
`Washer.Status.IDos1FillLevel`/`IDos2FillLevel` (ENUM, diagnostic)

### Binary Sensors (PROBLEM/diagnostic; on=`{Present,Confirmed}` off=`{Off}`)
`Dryer.Status.RefresherFillLevel` (on=`Poor`/off=`Filled`) · `Dryer.Event.CondensateContainerFull` ·
`Dryer.Event.LintFilterFull` · `Dryer.Event.Maintenance.Remind` · `Common.Event.FoamDetection` ·
`Common.Event.SupplyPower.SupplyVoltageTooLow` · `Common.Event.DoorLock.WaterLevelTooHigh` ·
`Common.Event.DoorNotLockable`/`DoorNotUnlockable` · `Common.Event.FatalErrorOccured` ·
`Washer.Event.DrumCleanReminder` · `Washer.Event.IDos1FillLevelPoor`/`IDos2FillLevelPoor` ·
`Washer.Event.IDosUnitDefect` · `Washer.Event.PumpError` · `Washer.Event.Spin.SpinAbort` ·
`Washer.Event.ReleaseRinseHoldPending` · `Washer.Event.IDos.IDosOpenTray`

### Select (Settings/Options)
- Common (config): `Setting.AutoPowerOff` · `Setting.BrightnessLevel` ·
  `Setting.DoorLightRing.{ActiveMode,BrightnessLevel}` · `Setting.EndSignalVolume` ·
  `Setting.KeySignalVolume` · `Setting.Sound.Volume` · `Setting.SupplyPower.PowerRating`
- Washer: `Option.SpinSpeed` ⭐ · `Option.Temperature` ⭐ · `Option.Stains` · `Option.MultipleSoak` ·
  `Option.RinsePlus` · `Option.WaterAndRinsePlus` · `Setting.IDos2Content` ·
  `Option.IDos1DosingLevel`/`IDos2DosingLevel`
- Common Options: `Option.VarioPerfect` · `Option.HygienicSteamIntensity`
- Dryer: `Option.{WrinkleGuard,DryingTarget,Refresher}` · `Setting.{CupboardDryFineAdjust,CupboardDryPlusFineAdjust,IronDryFineAdjust,SpinSpeedBeforeDrying}`

### Number
`Common.Setting.Brightness` (%) · `Common.Setting.DoorLightRing.Brightness` (%) ·
`Dryer.Option.SpinClass` (rpm) · `Washer.Setting.IDos1BaseLevel`/`IDos2BaseLevel` (ml)

### Switch
- Common: `Setting.DoorLightRing.Active` · `Setting.DrumLight.Active` · `Setting.EndSignal` ·
  `Setting.Sound.Mute` · `Setting.TimeLight.Active` · `Setting.WrinkleGuard` ·
  `Option.{SilentMode,SpeedPerfect,LowTemperatureHygiene}`
- Washer: `Option.IDos1Active`/`IDos2Active` (also the `.IDos1.Active`/`.IDos2.Active` variant) ·
  `Option.{IntensivePlus,LessIroning,SilentWash,SpeedPerfect,Soak,Prewash,RinseHold,RinsePlus1,RinsePlus3,WaterAndRinsePlus1,WaterPlus,Disinfectant,HygienicSteam}`
- Dryer: `Option.{Gentle,HalfLoad,Hygiene}`

### Light
`Common.Setting.DoorLightRing.Active` · `Common.Setting.DrumLight.Active`

**Start/Delayed start:** ⚠️ delayed via **`BSH.Common.Option.FinishInRelative` (uid 551)** —
**not** `StartInRelative` (#196). Writes often only succeed within the READWRITE window of
`BSH.Common.Root.ActiveProgram` (uid 256, ~30 s cycle, #384). Correct message:
`POST /ro/values [{551:delay},{256:programUID}]`.

---

## 6. Recommended MQTT Mapping for `go-homeconnect2mqtt`

### 6.1 Topic Schema (dot notation → slash)

```
Base:     <mqtt_topic>/<device>           # device = haId or configured name
State:    <base>/<feature-path>/state     # e.g. .../BSH/Common/Setting/PowerState/state
Command:  <base>/<feature-path>/set       # only for writable features
Avail.:   <base>/availability             # "online"/"offline" (LWT)
Conn:     <base>/connection_state         # connecting/handshake/connected/reconnecting/...
```

`<feature-path>` = feature name with `.` → `/` (e.g. `BSH.Common.Status.OperationState`
→ `BSH/Common/Status/OperationState`). Publish enum values as resolved names; raw value
optionally as an additional attribute.

### 6.2 Generic vs. Curated Approach

- **Generic (recommended, resilient):** for **every** entity from the DeviceDescription, a
  state topic; for every writable one (`access ∈ {readwrite,writeonly}`), additionally a
  set topic. This way no features are lost (structurally solving FK-8 "missing options").
- **Enriched:** the catalogues above provide device_class, unit, sensible defaults and
  the **appliance-specific start paths** (FK-4). Maintain these as an optional mapping table
  (analogous to `registers.yaml`) — operator-patchable, without a rebuild.

### 6.3 Home Assistant Discovery (optional)

One discovery payload per entity under `<hass_base_topic>/<platform>/<unique_id>/config`:
```json
{
  "unique_id": "homeconnect_<haId>_<feature>",
  "name": "<readable name>",
  "state_topic": "<base>/<feature-path>/state",
  "command_topic": "<base>/<feature-path>/set",   // writable only
  "availability_topic": "<base>/availability",
  "device_class": "<from catalogue>",
  "unit_of_measurement": "<from catalogue>",
  "options": ["..."],                                // select/enum
  "device": {"identifiers":["homeconnect_<haId>"], "manufacturer":"<brand>",
             "model":"<vib>", "name":"<device name>", "sw_version":"<swVersion>"}
}
```
Platform choice follows the heuristic from §1. Birth/LWT handling
(`<hass_base_topic>/status` → re-publish the discovery on "online").

### 6.4 Write Semantics (Command Topics)

1. Receive value → normalize type/enum per `02-data-model.md` (float→int for #68,
   enum name→raw value, case-insensitive).
2. Check `access`/`available`/write window (FK-5).
3. `POST /ro/values [{uid, value}]`; programs/commands per §3–§5 (appliance-specific paths, FK-4).
4. On error code (400/501/541) → log, optionally retry; leave state unchanged.
