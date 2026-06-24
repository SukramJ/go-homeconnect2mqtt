# Home Connect — Data Model (DeviceDescription, FeatureMapping, Entities)

> Standalone description of how the object model is built from the two profile XML
> files and which value/type semantics apply. Reconstructed from
> `description_parser.py`, `entities.py`, `appliance.py`, `const.py`, `helpers.py`
> and the test fixtures `DeviceDescription_short.xml` / `FeatureMapping_short.xml`.

---

## 1. Overview

Two XML files describe each appliance statically (they come from the profile archive):

- **`<serial>_DeviceDescription.xml`** — structure, data types, access, min/max, enum IDs.
- **`<serial>_FeatureMapping.xml`** — clear names (e.g. `BSH.Common.Setting.PowerState`),
  error texts and enum value names.

Both are linked via **UID** (hex). Parsing produces a `DeviceDescription`
object, from which the `HomeAppliance` creates typed **Entities**. Live values arrive
at runtime via `NOTIFY /ro/values` (see `01-protocol.md`).

---

## 2. DeviceDescription.xml — Structure

Root element `<device>` with:
- `<description>` — appliance metadata (`type`, `brand`, `model`, `version`, `revision`, `pairableDeviceTypes`).
- Nested `*List` containers that are resolved recursively:
  - `<statusList>` → `<status>` (read-only states)
  - `<settingList>` → `<setting>` (readable/writable settings)
  - `<eventList>` → `<event>` (events/messages)
  - `<commandList>` → `<command>` (actions)
  - `<optionList>` → `<option>` (program options)
  - `<programGroup>` → `<program>` (programs, with embedded `<option refUID=..>`)
- Single elements: `<activeProgram>`, `<selectedProgram>`, `<protectionPort>`.
- `<enumerationTypeList>` → `<enumerationType enid=..>` → `<enumeration value=..>` (+ `subsetOf`).

### Example (abridged, from fixture)

```xml
<device>
  <description><type>HomeAppliance</type><brand>Fake_Brand</brand><model>Fake_Model</model>
               <version>2</version><revision>0</revision></description>
  <statusList access="read" available="true" uid="0001">
    <status access="read" available="true" refCID="01" refDID="00" uid="1001" />
    <statusList access="read" available="true" uid="0002">
      <status access="read" available="true" enumerationType="3002" refCID="03" refDID="00" uid="1002" />
    </statusList>
  </statusList>
  <settingList access="readWrite" available="true" uid="0003">
    <setting access="readWrite" available="true" refCID="01" uid="1005"
             max="10" min="0" stepSize="1" initValue="1" default="0" refDID="00"
             passwordProtected="false" notifyOnChange="false" />
  </settingList>
  <commandList access="writeOnly" available="true" uid="0007">
    <command access="writeOnly" available="true" refCID="01" refDID="00" uid="100D" />
  </commandList>
  <programGroup available="true" uid="000B">
    <program available="true" execution="selectOnly" uid="1015">
      <option access="readWrite" available="true" liveUpdate="false" default="true" refUID="1011" />
    </program>
  </programGroup>
  <activeProgram access="readWrite" validate="true" uid="1019" />
  <selectedProgram access="readWrite" fullOptionSet="false" uid="101A" />
  <enumerationTypeList>
    <enumerationType enid="3001"><enumeration value="0"/><enumeration value="1"/><enumeration value="2"/></enumerationType>
    <enumerationType enid="3003" subsetOf="3001"><enumeration value="1"/></enumerationType>
  </enumerationTypeList>
</device>
```

### Attribute semantics per element

| Attribute | Processing |
|---|---|
| `uid` | **hex → int**. Primary key; links to FeatureMapping (→ `name`). |
| `refCID` | **hex → int** → determines `contentType` + `protocolType` (tables §4). Data-type source! |
| `refDID` | hex → int (device/domain ID, rarely relevant). |
| `enumerationType` | hex → int (= `enid`); resolves the enum value map from FeatureMapping. |
| `access` | **lowercase** → `read`/`readwrite`/`writeonly`/`readstatic`/`none`. |
| `execution` | **lowercase** → `selectonly`/`startonly`/`selectandstart`/`none`. |
| `available`, `notifyOnChange`, `passwordProtected`, `liveUpdate`, `fullOptionSet`, `validate` | bool (`"true"`/`"false"`/number). |
| `min`, `max`, `stepSize` | → float. |
| `initValue`, `default` | raw value (string) → later cast via protocolType. |
| `option` (in `program`) | list: `{access(lower), available, liveUpdate, refUID(hex), default}`. |

**Parser robustness (mandatory in Go):**
- `force_list` for `option/status/setting/event/command/program/...` — a single element
  must be treated like a 1-element list (XML has no fixed 1-vs-n form).
- Per section try/except → on error skip the section, do **not** discard the whole appliance.
- Skip the unknown instead of crashing (models vary widely).

---

## 3. FeatureMapping.xml — Structure

```xml
<featureMappingFile>
  <featureDescription>
    <feature refUID="1001">Status.1</feature>            <!-- real: BSH.Common.Status.... -->
    <feature refUID="1005">Setting.1</feature>
    <feature refUID="1019">ActiveProgram</feature>
  </featureDescription>
  <errorDescription>
    <error refEID="2001">Error.1</error>
  </errorDescription>
  <enumDescriptionList>
    <enumDescription refENID="3001" enumKey="EventState">
      <enumMember refValue="0">Off</enumMember>
      <enumMember refValue="1">Present</enumMember>
      <enumMember refValue="2">Confirmed</enumMember>
    </enumDescription>
  </enumDescriptionList>
</featureMappingFile>
```

Yields three maps (keys hex-decoded, except `refValue` = decimal):
- `feature`: `refUID(hex) → name`
- `error`: `refEID(hex) → name`
- `enumeration`: `refENID(hex) → { refValue(dec) → name }`

`force_list` for `feature/error/enumDescription/enumMember`.

---

## 4. Type tables (`refCID` → type)

`refCID` (hex→int) determines two things: the fine-grained **contentType** (display/unit) and
the coarse **protocolType** (wire cast). Excerpt of the most important codes; full tables
below.

### 4.1 protocolType (wire cast — controls value conversion)

5 values: **Boolean, Integer, Float, String, Object**.

```
1→Boolean  2→Integer  3→Integer  4→Float  5→String  6→String  7→Float  8→Float
10→String  16→Integer 17→Float   18→Integer 19→Integer 20→Integer 21→Integer
22→String  23→String  24→Integer 25..27→Object 30→String 31→Integer 32→Integer
33→Float 34→Float 35→Float 36→Float 37→Integer 38→Integer 39→Float 40→Object
41→Float 42→Object 43..46→Float 47→Integer 48→String 49→Object 50→String
51..57→Float 58→Integer 59→Integer 61→String 62..63→Object 64→Float 65→Float
129..194 → Object   (all *List/complex types)
```

### 4.2 contentType (fine-grained type — for unit/device_class)

```
1 boolean        2 integer       3 enumeration   4 float          5 string
6 dateTime       7 temperatureCelsius  8 temperatureFahrenheit   10 hexBinary
16 timeSpan      17 percent      18 dbm          19 weight        20 liquidVolume
21 uidValue      22 date         23 time         24 waterHardness 25 point2D
26 pose2D        27 line2D       30 rgb          31 rpm           32 flowRate
33 length        34 area         35 power        36 energy        37 bigInteger
38 identifier    39 speed        40 programInstruction  41 weightPound  42 localeString
43 teaspoon ... 46 piece         47 byteLength   48 uuid          49 timezone
50 csv  51 leaf 52 bunch ... 59 portion  61 utcDateTime  62 programRunSummary
63 programSessionSummary  64 liquidVolumeThroughput  65 weightOunces
129..194 = respective *List variants (e.g. 131 enumerationList, 156 path, 157 polygon)
```

> **Note:** `contentType == 3` (enumeration) ⇒ protocolType Integer, but the element carries
> an `enumerationType` → raw integer is resolved to a name via the enum map.

---

## 5. Parsing pipeline (pseudocode for Go)

```
1. featureMap = parse FeatureMapping.xml  (feature, error, enumeration)
2. addEnumSubsets: for each <enumerationType subsetOf=X>:
     subset[value] = featureMap.enumeration[X][value]  for the listed <enumeration value=..>
     featureMap.enumeration[enid] = subset
3. parse DeviceDescription.xml recursively:
     for each status/setting/event/command/option/program/activeProgram/selectedProgram:
       element.uid          = hex(@uid)
       element.name         = featureMap.feature[uid]          # link!
       element.refCID       = hex(@refCID)
       element.contentType  = CONTENT_TYPES[refCID]
       element.protocolType = PROTOCOL_TYPES[refCID]
       if @enumerationType: element.enumeration = featureMap.enumeration[hex(@enumerationType)]
       element.access/execution = lower(@..)
       element.{min,max,stepSize} = float(@..)
       carry over remaining attributes raw
4. HomeAppliance.createEntities(description) → typed entities (see §6)
```

---

## 6. Entity model & value semantics (`entities.py`)

The parsed descriptions produce entities, indexed by `uid` **and** by `name`.

### 6.1 Entity types

| Type | Source | Mixins | Particularity |
|---|---|---|---|
| `Status` | status | Access, Available, MinMax | read |
| `Setting` | setting | Access, Available, MinMax | readable/writable |
| `Event` | event | — | `acknowledge()`/`reject()` via Commands |
| `Command` | command | Access, Available, MinMax | `execute(value)` |
| `Option` | option | Access, Available, MinMax | program options |
| `Program` | program | Available | `select()` / `start()`, holds Options |
| `ActiveProgram` | activeProgram | Access, Available (=True) | `value` = UID of the running program |
| `SelectedProgram` | selectedProgram | Access, Available (=True) | `value` = UID of the selected program |
| `ProtectionPort` | protectionPort | Access, Available (=False) | |

### 6.2 Value fields

- `value_raw` — internal raw value (cast via protocolType).
- `value` — if enum: `enumeration.get(value_raw)` (name); otherwise `value_raw`.
- `value_shadow` — set optimistically after a successful write; basis for
  `active_program`/`selected_program` (0/None = no program).
- Enum maps: `enumeration` = `{int → name}`, `rev_enumeration` = `{name → int}`.

### 6.3 update() (incoming NOTIFY/RESPONSE)

```
if "value" in item:    value_raw = cast(protocolType, item.value); value_shadow = value_raw
if "access" in item:   access    = lower(item.access)        # AccessMixin
if "available" in item: available = bool(item.available)     # AvailableMixin
if "min"/"max"/"stepSize" in item: float(...)                # MinMaxMixin
if "execution" in item: execution = lower(item.execution)    # Program  ← Bug #70: lower() mandatory!
-> fire callbacks
```

### 6.4 set_value / set_value_raw (outgoing)

```
set_value(x):
  if enum: x must be in rev_enumeration → set_value_raw(rev_enumeration[x])
  otherwise: set_value_raw(x)
set_value_raw(v):
  AccessMixin:   access ∈ {readwrite, writeonly}  otherwise AccessError
  AvailableMixin: available == true               otherwise AccessError
  POST /ro/values [{uid, value: cast(protocolType, v)}]
  on RESPONSE without code: value_shadow = cast(v)
```

### 6.5 Type cast functions (`helpers.TYPE_MAPPING`)

```
Boolean → convert_bool   ("true"/"false" case-insensitive; numbers → bool)
Integer → int
Float   → float
String  → str
Object  → json.loads  (with workaround: on error replace ']"' → ']', parse again)
None    → identity
```

---

## 7. Known data-model pitfalls (handle defensively)

| # | Pitfall | Go rule |
|---|---|---|
| #70 | `execution` arrives live in uppercase (`SELECTANDSTART`) | parse **all** string enums (access, execution, handling, level) case-insensitively — in the parser **and** in the live update |
| #68 | Float setting (stepSize=1) rejects `4.0`, wants `4` | for protocolType Float + integer value (esp. stepSize==1, temperature*/percent) write **Integer** on the wire |
| #56 | Appliance sends raw value outside the enum | `value`: `enumeration.get(raw)`, on miss return the **raw value** (no crash) |
| #66 | active/selectedProgram reports a UID not present in the description | defensive UID lookup → None instead of KeyError |
| — | Update for unknown UID | ignore + debug log |
| — | sID/msgID/version as string | cast with int |
| — | Object field with malformed JSON | `]"`→`]` workaround |
| #53 | Namespaces vary (`Dishcare.Dishwasher.*`, not only `BSH.Common.*`) | support arbitrary feature names, no hard-coding to `BSH.Common` |

---

## 8. Namespace conventions of feature names

Feature names follow dot notation and roughly split into:
- `BSH.Common.{Status|Setting|Option|Event|Command|Root}.*` — cross-appliance
- `Dishcare.Dishwasher.{Status|Setting|Option|Event}.*` — dishwasher
- `Cooking.{Oven|Hob|Hood|Common}.{Status|Setting|Option|Event|Command}.*` — hob/oven/hood
- `LaundryCare.{Common|Washer|Dryer}.{Status|Setting|Option|Event}.*` — laundry care
- `Refrigeration.*`, `ConsumerProducts.*` (coffee) etc.

These names map naturally onto MQTT topics — see `04-device-mapping.md`.
