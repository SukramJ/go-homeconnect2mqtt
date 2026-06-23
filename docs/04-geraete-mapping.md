# Home Connect — Feature-Kataloge & MQTT-/HA-Mapping

> Vollständiger Feature-Katalog (verbatim aus `homeconnect_local_hass`
> `entity_descriptions/`) für **BSH.Common** plus die drei Zielgeräte
> **Geschirrspüler, Induktionskochfeld, Waschmaschine**, sowie die empfohlene
> Abbildung auf MQTT-Topics und Home-Assistant-Discovery.
>
> Die HA-Integration nutzt eine **kuratierte Allowlist** (feste Feature-Namen → Plattform).
> `go-homeconnect2mqtt` sollte demgegenüber **generisch alle Features** des Geräts auf MQTT
> abbilden (resilienter, vollständiger) und diese Kataloge nur für **Anreicherung**
> (device_class, Einheit, sinnvolle Defaults, gerätespezifische Startpfade) verwenden.

---

## 1. Mapping-Framework (HA-Plattform-Heuristik)

Die HA-Integration bindet jede „EntityDescription" an einen festen Feature-Namen (`entity`)
oder mehrere (`entities`). Eine Entity entsteht nur, wenn **alle** referenzierten Features im
Gerät vorhanden sind. Plus **dynamische Generatoren** (Callables, die zur Laufzeit aus dem
Gerät ableiten).

**Plattform-Zuordnung (Heuristik, übertragbar auf MQTT/HA-Discovery):**

| Plattform | Quelle | Kriterium |
|---|---|---|
| `switch` | Setting/Option | boolesch; optional `value_mapping=(on,off)` für Enum-Schalter (z. B. PowerState On/Off) |
| `select` | Setting/Option | Enum, `access ∈ {readwrite, writeonly}` |
| `sensor` | Status/Option | read; `device_class=ENUM` bei Enum, sonst DURATION/TEMPERATURE/PERCENTAGE/SIGNAL_STRENGTH |
| `binary_sensor` | Event/Status | `value_on`/`value_off`-Sets (Events: on=`{Present,Confirmed}`, off=`{Off}`; Tür: open/closed) |
| `number` | Setting/Option | numerisch schreibbar; min/max/step **dynamisch vom Entity** (MinMax), Beschreibung nur Fallback |
| `button` | Command | z. B. `…Command.AbortProgram`; `start_button` nur wenn Programm mit `Execution.SELECT_AND_START` existiert |
| `event_sensor` | mehrere Events | kombiniert (z. B. Salz: `SaltLack`+`SaltNearlyEmpty` → `empty/nearly_empty/full`) |
| `light`/`fan` | dynamisch | Hauben-/Ambient-Licht bzw. Hauben-Lüfterstufen |

**Werte-Konventionen:** `unique_id = "<deviceID>-<key>"`; Enum-Werte über FeatureMapping
aufgelöst; `has_state_translation` ⇒ lowercased Werte; Programmname = `program.lower().replace(".","_")`.

---

## 2. BSH.Common — geräteübergreifende Features

Gilt (teilweise) für **alle** Geräte. (`common.py`)

### Sensoren (Status/Option, read)
| key | Feature | device_class / Einheit |
|---|---|---|
| operation_state | `BSH.Common.Status.OperationState` | ENUM |
| power_state (Sensor) | `BSH.Common.Setting.PowerState` | ENUM |
| door_state | `BSH.Common.Status.DoorState` | ENUM (Sensor, wenn >2 Enum-Werte) |
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

### Binary-Sensoren (Status/Event)
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

### Schalter / Auswahl / Zahlen
| key | Feature | Plattform |
|---|---|---|
| child_lock | `BSH.Common.Setting.ChildLock` | switch |
| remote_control_level | `BSH.Common.Setting.RemoteControlLevel` | select (config) |
| temperature_unit | `BSH.Common.Setting.TemperatureUnit` | select (wenn >2 Werte) |
| duration | `BSH.Common.Option.Duration` | number DURATION/s |
| start_in | `BSH.Common.Option.StartInRelative` | number DURATION/s |
| finish_in | `BSH.Common.Option.FinishInRelative` | number DURATION/s |
| alarm_clock | `BSH.Common.Setting.AlarmClock` | number DURATION/s |

### Buttons (Commands)
`BSH.Common.Command.AbortProgram` · `PauseProgram` · `ResumeProgram` · `MainsPowerOff` ·
`AcknowledgeEvent` · `RejectEvent`

### Dynamische Generatoren (wichtig fürs Verständnis)
- **Power-Switch/Select** (`generate_power_switch`): aus `BSH.Common.Setting.PowerState`-Enum
  die schaltbaren States (gefiltert über min/max) bilden. Genau 2 States, die zu einer der
  Paarungen passen → **Switch** mit `value_mapping`:
  ```
  POWER_SWITCH_VALUE_MAPINGS = ("On","MainsOff") | ("Standby","MainsOff") | ("On","Off")
                               | ("On","Standby") | ("Standby","Off")
  ```
  sonst → **Select** (alle States, lowercased).
- **Programm** (`generate_program`): Select `BSH.Common.Root.SelectedProgram` + Sensor
  `BSH.Common.Root.ActiveProgram`; Programmnamen sortiert; Favoriten
  (`BSH.Common.Program.Favorite.<x>`) über `BSH.Common.Setting.Favorite.<x>.Name` auflösen.
- **Start-Button** (`generate_start_button`): nur wenn ein Programm `Execution.SELECT_AND_START`
  hat → Button auf `BSH.Common.Root.ActiveProgram` (uid 256 — siehe FK-5 in `05-resilienz.md`).
- **WiFi-Sensor**, **Temperature-Unit-Select**: Fallbacks.

---

## 3. Geschirrspüler — `Dishcare.Dishwasher.*` (`dishcare.py`)

### Binary-Sensoren (Events, alle PROBLEM/diagnostic, on=`{Present,Confirmed}` off=`{Off}`)
`Status.EcoDryActive` · `Event.MachineCareReminder` · `Event.LowVoltage` ·
`Event.MachineCareAndFilterCleaningReminder` · `Event.WaterheaterCalcified` ·
`Event.SmartFilterCleaningReminder` · `Event.CheckFilterSystem` · `Event.DrainingNotPossible` ·
`Event.DrainPumpBlocked` · `Event.FlexSpray.Error.{Blocked,General,SprayArmNotMounted}`
*(FlexSpray = Bosch „PowerControl")*

### Event-Sensoren (kombiniert → `empty`/`nearly_empty`/`full`)
- Spülklarspüler: `Event.RinseAidLack` + `Event.RinseAidNearlyEmpty`
- Salz: `Event.SaltLack` + `Event.SaltNearlyEmpty`

### Select (Settings/Options, config)
`Setting.DryingAssistantAllPrograms` · `Setting.HotWater` · `Setting.RinseAid` ·
`Setting.SoundLevelSignal` · `Setting.SoundLevelKey` · `Setting.WaterHardness` ·
`Setting.SensitivityTurbidity` · `Setting.EcoAsDefault` ·
`Option.FlexSpray.{Type,FrontLeft,BackLeft,BackRight,FrontRight}` ·
`Setting.FlexSpray.Custom.{FrontLeft,BackLeft,BackRight,FrontRight}`

### Sensor
`Status.ProgramPhase` (ENUM)

### Switch (Options = pro-Programm; Settings = config)
`Option.ExtraDry` · `Option.HygienePlus` · `Option.IntensivZone` · `Option.VarioSpeedPlus` ·
`Option.SilenceOnDemand` · `Option.BrillianceDry` · `Option.ZeoliteDry` (CrystalDry) ·
`Option.HalfLoad` · `Option.ExtraRinse` · `Setting.ExtraDry` · `Setting.SpeedOnDemand` ·
`Setting.InfoLight`

**Start/Delayed-Start:** Standard-Pfad (`/ro/activeProgram`), Delayed via
`BSH.Common.Option.StartInRelative`. Bei `400` retrybar (#322).

---

## 4. Induktionskochfeld — `Cooking.Hob.*` / `Cooking.Common.*` (`cooking.py`)

> Fragilster Setup-Typ (FK-3). Zonen werden **dynamisch per Regex** erkannt.

### Dynamische Zonen-Sensoren — `generate_hob_zones`
Regex: `^Cooking\.Hob\.Status\.Zone\.(\d+)\..*$` → pro Zone `<n>` (1-basiert) je ein Sensor,
sofern das Feature existiert:

| Suffix | device_class / Einheit | extra |
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

⚠️ **#395/#368-Bugklasse:** Group-ID strikt `(\d+)` matchen, nicht-numerisch ignorieren
(`int("001.RemainingProgramTime")` crasht). Per-Zone-Erzeugung mit Existenz-Guard.

### Statische Cooking-Sensoren/Settings
- Hood (Haube): `Setting.IntervalTimeOff/On`, `Setting.DelayedShutOffTime`,
  `Status.GreaseFilterSaturation` (%), `Status.CarbonFilterSaturation` (%)
- Oven (Backofen, falls vorhanden): `Option.HeatupProgress` (%),
  `Status.CurrentCavityTemperature`/`CurrentMeatprobeTemperature` (°C),
  WaterTank (`Status.WaterTankUnplugged`+`WaterTankEmpty` → `unplugged/empty/ok`)

### Select / Switch (Hob/Hood relevant)
- `Cooking.Hob.Setting.Ventilation` (select, config)
- `Cooking.Hood.Setting.IntervalStage` · `DelayedShutOffStage` · `CarbonFilterType` (select)
- `Cooking.Common.Option.Hood.Boost` (switch) · `Cooking.Hood.Setting.NoiseReduction` (switch)
- Oven-Settings: `Option.SetpointTemperature` (number °C), `Setting.DisplayBrightness`,
  Selects `Option.Level/UsedHeatingMode/PyrolysisLevel`, Switches `Option.FastPreHeat`,
  `Setting.{ButtonTones,OvenLightDuringOperation,SabbathMode}`

### Haube — dynamisch
- **Fan** (`generate_hood_fan`): aus `Cooking.Common.Option.Hood.VentingLevel` /
  `IntensiveLevel`. ⚠️ Fan-Off via **DELETE** `/ro/activeProgram` (#386).
- **Licht** (`generate_hood_light`): `Cooking.Common.Setting.Lighting` (+ `LightingBrightness`,
  `Cooking.Hood.Setting.ColorTemperaturePercent`).
- **Ambient-Licht** (`generate_hood_ambient_light`): `BSH.Common.Setting.AmbientLight*`.

### Filter-Reset-Buttons
`Cooking.Common.Command.Hood.{CarbonFilterReset,GreaseFilterReset,RegenerativeCarbonFilterReset,RegenerativeCarbonFilterLifeTimeReset}`

**Start (Hob):** ⚠️ direkter `POST /ro/selectedProgram` (`validate=false`); Standard-Start
crasht mit `NoneType.start`, wenn nichts vorgewählt (#385). Oft On-Device-Bestätigung nötig (#111/#261).

---

## 5. Waschmaschine — `LaundryCare.{Common,Washer,Dryer}.*` (`laundry_care.py`)

### Sensoren
`Common.Status.Laundry.Reload` (ENUM) · `Common.Option.ProcessPhase` (ENUM) ·
`Dryer.Option.ProcessPhase` (ENUM) · `Washer.Option.SpinSpeed` (ENUM) ·
`Common.Option.LoadRecommendation` (WEIGHT/g→kg) ·
`Washer.Status.IDos1FillLevel`/`IDos2FillLevel` (ENUM, diagnostic)

### Binary-Sensoren (PROBLEM/diagnostic; on=`{Present,Confirmed}` off=`{Off}`)
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
- Washer: `Option.IDos1Active`/`IDos2Active` (auch `.IDos1.Active`/`.IDos2.Active`-Variante) ·
  `Option.{IntensivePlus,LessIroning,SilentWash,SpeedPerfect,Soak,Prewash,RinseHold,RinsePlus1,RinsePlus3,WaterAndRinsePlus1,WaterPlus,Disinfectant,HygienicSteam}`
- Dryer: `Option.{Gentle,HalfLoad,Hygiene}`

### Licht
`Common.Setting.DoorLightRing.Active` · `Common.Setting.DrumLight.Active`

**Start/Delayed-Start:** ⚠️ Delayed via **`BSH.Common.Option.FinishInRelative` (uid 551)** —
**nicht** `StartInRelative` (#196). Schreiben oft nur im READWRITE-Fenster von
`BSH.Common.Root.ActiveProgram` (uid 256, ~30 s-Takt, #384). Korrekte Message:
`POST /ro/values [{551:delay},{256:programUID}]`.

---

## 6. Empfohlene MQTT-Abbildung für `go-homeconnect2mqtt`

### 6.1 Topic-Schema (Punktnotation → Slash)

```
Basis:    <mqtt_topic>/<device>           # device = haId oder konfigurierter Name
State:    <basis>/<feature-pfad>/state    # z. B. .../BSH/Common/Setting/PowerState/state
Command:  <basis>/<feature-pfad>/set      # nur für schreibbare Features
Avail.:   <basis>/availability            # "online"/"offline" (LWT)
Conn:     <basis>/connection_state        # connecting/handshake/connected/reconnecting/...
```

`<feature-pfad>` = Feature-Name mit `.` → `/` (z. B. `BSH.Common.Status.OperationState`
→ `BSH/Common/Status/OperationState`). Enum-Werte als aufgelöste Namen publizieren; Rohwert
optional als zusätzliches Attribut.

### 6.2 Generischer vs. kurierter Ansatz

- **Generisch (empfohlen, resilient):** Für **jedes** Entity aus der DeviceDescription ein
  State-Topic; für jedes schreibbare (`access ∈ {readwrite,writeonly}`) zusätzlich ein
  Set-Topic. So gehen keine Features verloren (löst FK-8 „fehlende Optionen" strukturell).
- **Angereichert:** Die Kataloge oben liefern device_class, Einheit, sinnvolle Defaults und
  die **gerätespezifischen Startpfade** (FK-4). Diese als optionale Mapping-Tabelle
  (analog `registers.yaml`) pflegen — operator-patchbar, ohne Rebuild.

### 6.3 Home-Assistant-Discovery (optional)

Pro Entity ein Discovery-Payload unter `<hass_base_topic>/<platform>/<unique_id>/config`:
```json
{
  "unique_id": "homeconnect_<haId>_<feature>",
  "name": "<lesbarer Name>",
  "state_topic": "<basis>/<feature-pfad>/state",
  "command_topic": "<basis>/<feature-pfad>/set",   // nur schreibbar
  "availability_topic": "<basis>/availability",
  "device_class": "<aus Katalog>",
  "unit_of_measurement": "<aus Katalog>",
  "options": ["..."],                                // select/enum
  "device": {"identifiers":["homeconnect_<haId>"], "manufacturer":"<brand>",
             "model":"<vib>", "name":"<gerätename>", "sw_version":"<swVersion>"}
}
```
Plattform-Wahl nach der Heuristik aus §1. Birth/LWT wie im Schwesterprojekt
(`<hass_base_topic>/status` → Re-Publish der Discovery bei „online").

### 6.4 Schreib-Semantik (Command-Topics)

1. Wert empfangen → Typ/Enum gemäß `02-datenmodell.md` normalisieren (Float→Int bei #68,
   Enum-Name→Rohwert, case-insensitiv).
2. `access`/`available`/Schreibfenster prüfen (FK-5).
3. `POST /ro/values [{uid, value}]`; Programme/Commands gemäß §3–§5 (gerätespezifische Pfade, FK-4).
4. Bei Fehler-Code (400/501/541) → loggen, optional retrien; State unverändert lassen.
