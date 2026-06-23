# Home Connect — Datenmodell (DeviceDescription, FeatureMapping, Entities)

> Eigenständige Beschreibung, wie aus den beiden Profil-XML-Dateien das Objektmodell
> entsteht und welche Wert-/Typ-Semantik gilt. Rekonstruiert aus
> `description_parser.py`, `entities.py`, `appliance.py`, `const.py`, `helpers.py`
> und den Test-Fixtures `DeviceDescription_short.xml` / `FeatureMapping_short.xml`.

---

## 1. Überblick

Zwei XML-Dateien beschreiben jedes Gerät statisch (kommen aus dem Profil-Archiv):

- **`<serial>_DeviceDescription.xml`** — Struktur, Datentypen, Zugriff, Min/Max, Enum-IDs.
- **`<serial>_FeatureMapping.xml`** — Klarnamen (z. B. `BSH.Common.Setting.PowerState`),
  Fehlertexte und Enum-Wert-Namen.

Verknüpft werden beide über **UID** (hex). Aus dem Parsen entsteht ein `DeviceDescription`-
Objekt, daraus erzeugt die `HomeAppliance` typisierte **Entities**. Live-Werte kommen
zur Laufzeit per `NOTIFY /ro/values` (siehe `01-protokoll.md`).

---

## 2. DeviceDescription.xml — Struktur

Wurzelelement `<device>` mit:
- `<description>` — Geräte-Metadaten (`type`, `brand`, `model`, `version`, `revision`, `pairableDeviceTypes`).
- Verschachtelte `*List`-Container, die rekursiv aufgelöst werden:
  - `<statusList>` → `<status>` (read-only Zustände)
  - `<settingList>` → `<setting>` (les-/schreibbare Einstellungen)
  - `<eventList>` → `<event>` (Ereignisse/Meldungen)
  - `<commandList>` → `<command>` (Aktionen)
  - `<optionList>` → `<option>` (Programm-Optionen)
  - `<programGroup>` → `<program>` (Programme, mit eingebetteten `<option refUID=..>`)
- Einzelelemente: `<activeProgram>`, `<selectedProgram>`, `<protectionPort>`.
- `<enumerationTypeList>` → `<enumerationType enid=..>` → `<enumeration value=..>` (+ `subsetOf`).

### Beispiel (gekürzt, aus Fixture)

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

### Attribut-Semantik je Element

| Attribut | Verarbeitung |
|---|---|
| `uid` | **hex → int**. Primärschlüssel; verbindet mit FeatureMapping (→ `name`). |
| `refCID` | **hex → int** → bestimmt `contentType` + `protocolType` (Tabellen §4). Datentyp-Quelle! |
| `refDID` | hex → int (Geräte-/Domain-ID, selten relevant). |
| `enumerationType` | hex → int (= `enid`); löst die Enum-Wert-Map aus FeatureMapping auf. |
| `access` | **lowercase** → `read`/`readwrite`/`writeonly`/`readstatic`/`none`. |
| `execution` | **lowercase** → `selectonly`/`startonly`/`selectandstart`/`none`. |
| `available`, `notifyOnChange`, `passwordProtected`, `liveUpdate`, `fullOptionSet`, `validate` | bool (`"true"`/`"false"`/Zahl). |
| `min`, `max`, `stepSize` | → float. |
| `initValue`, `default` | Rohwert (String) → später per protocolType gecastet. |
| `option` (in `program`) | Liste: `{access(lower), available, liveUpdate, refUID(hex), default}`. |

**Parser-Robustheit (Pflicht in Go):**
- `force_list` für `option/status/setting/event/command/program/...` — ein Einzelelement
  muss wie eine 1-elementige Liste behandelt werden (XML hat keine feste 1-vs-n-Form).
- Pro Sektion try/except → bei Fehler die Sektion überspringen, **nicht** das ganze Gerät verwerfen.
- Unbekanntes überspringen statt abstürzen (Modelle variieren stark).

---

## 3. FeatureMapping.xml — Struktur

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

Ergibt drei Maps (Keys hex-dekodiert, außer `refValue` = dezimal):
- `feature`: `refUID(hex) → name`
- `error`: `refEID(hex) → name`
- `enumeration`: `refENID(hex) → { refValue(dez) → name }`

`force_list` für `feature/error/enumDescription/enumMember`.

---

## 4. Typ-Tabellen (`refCID` → Typ)

`refCID` (hex→int) bestimmt zwei Dinge: den feinen **contentType** (Anzeige/Einheit) und
den groben **protocolType** (Wire-Cast). Auszug der wichtigsten Codes; vollständige Tabellen
unten.

### 4.1 protocolType (Wire-Cast — steuert die Wert-Konvertierung)

5 Werte: **Boolean, Integer, Float, String, Object**.

```
1→Boolean  2→Integer  3→Integer  4→Float  5→String  6→String  7→Float  8→Float
10→String  16→Integer 17→Float   18→Integer 19→Integer 20→Integer 21→Integer
22→String  23→String  24→Integer 25..27→Object 30→String 31→Integer 32→Integer
33→Float 34→Float 35→Float 36→Float 37→Integer 38→Integer 39→Float 40→Object
41→Float 42→Object 43..46→Float 47→Integer 48→String 49→Object 50→String
51..57→Float 58→Integer 59→Integer 61→String 62..63→Object 64→Float 65→Float
129..194 → Object   (alle *List-/komplexen Typen)
```

### 4.2 contentType (feiner Typ — für Einheit/device_class)

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
129..194 = jeweilige *List-Varianten (z. B. 131 enumerationList, 156 path, 157 polygon)
```

> **Hinweis:** `contentType == 3` (enumeration) ⇒ protocolType Integer, aber das Element trägt
> ein `enumerationType` → Roh-Integer wird über die Enum-Map zum Namen aufgelöst.

---

## 5. Parsing-Pipeline (Pseudocode für Go)

```
1. featureMap = parse FeatureMapping.xml  (feature, error, enumeration)
2. addEnumSubsets: für jeden <enumerationType subsetOf=X>:
     subset[value] = featureMap.enumeration[X][value]  für die gelisteten <enumeration value=..>
     featureMap.enumeration[enid] = subset
3. parse DeviceDescription.xml rekursiv:
     für jedes status/setting/event/command/option/program/activeProgram/selectedProgram:
       element.uid          = hex(@uid)
       element.name         = featureMap.feature[uid]          # Verknüpfung!
       element.refCID       = hex(@refCID)
       element.contentType  = CONTENT_TYPES[refCID]
       element.protocolType = PROTOCOL_TYPES[refCID]
       if @enumerationType: element.enumeration = featureMap.enumeration[hex(@enumerationType)]
       element.access/execution = lower(@..)
       element.{min,max,stepSize} = float(@..)
       restliche Attribute roh übernehmen
4. HomeAppliance.createEntities(description) → typisierte Entities (siehe §6)
```

---

## 6. Entity-Modell & Wert-Semantik (`entities.py`)

Aus den geparsten Beschreibungen entstehen Entities, indexiert per `uid` **und** per `name`.

### 6.1 Entity-Typen

| Typ | Quelle | Mixins | Besonderheit |
|---|---|---|---|
| `Status` | status | Access, Available, MinMax | read |
| `Setting` | setting | Access, Available, MinMax | les-/schreibbar |
| `Event` | event | — | `acknowledge()`/`reject()` via Commands |
| `Command` | command | Access, Available, MinMax | `execute(value)` |
| `Option` | option | Access, Available, MinMax | Programm-Optionen |
| `Program` | program | Available | `select()` / `start()`, hält Options |
| `ActiveProgram` | activeProgram | Access, Available (=True) | `value` = UID des laufenden Programms |
| `SelectedProgram` | selectedProgram | Access, Available (=True) | `value` = UID des gewählten Programms |
| `ProtectionPort` | protectionPort | Access, Available (=False) | |

### 6.2 Wert-Felder

- `value_raw` — interner Rohwert (per protocolType gecastet).
- `value` — wenn Enum: `enumeration.get(value_raw)` (Name); sonst `value_raw`.
- `value_shadow` — optimistisch nach erfolgreichem Schreiben gesetzt; Basis für
  `active_program`/`selected_program` (0/None = kein Programm).
- Enum-Maps: `enumeration` = `{int → name}`, `rev_enumeration` = `{name → int}`.

### 6.3 update() (eingehende NOTIFY/RESPONSE)

```
if "value" in item:    value_raw = cast(protocolType, item.value); value_shadow = value_raw
if "access" in item:   access    = lower(item.access)        # AccessMixin
if "available" in item: available = bool(item.available)     # AvailableMixin
if "min"/"max"/"stepSize" in item: float(...)                # MinMaxMixin
if "execution" in item: execution = lower(item.execution)    # Program  ← Bug #70: lower() Pflicht!
-> Callbacks feuern
```

### 6.4 set_value / set_value_raw (ausgehend)

```
set_value(x):
  wenn Enum: x muss in rev_enumeration sein → set_value_raw(rev_enumeration[x])
  sonst: set_value_raw(x)
set_value_raw(v):
  AccessMixin:   access ∈ {readwrite, writeonly}  sonst AccessError
  AvailableMixin: available == true               sonst AccessError
  POST /ro/values [{uid, value: cast(protocolType, v)}]
  bei RESPONSE ohne code: value_shadow = cast(v)
```

### 6.5 Typ-Cast-Funktionen (`helpers.TYPE_MAPPING`)

```
Boolean → convert_bool   ("true"/"false" case-insensitiv; Zahlen → bool)
Integer → int
Float   → float
String  → str
Object  → json.loads  (mit Workaround: bei Fehler ']"' → ']' ersetzen, erneut parsen)
None    → identity
```

---

## 7. Bekannte Datenmodell-Fallstricke (defensiv behandeln)

| # | Fallstrick | Go-Regel |
|---|---|---|
| #70 | `execution` kommt live großgeschrieben (`SELECTANDSTART`) | **alle** String-Enums (access, execution, handling, level) case-insensitiv parsen — im Parser **und** im Live-Update |
| #68 | Float-Setting (stepSize=1) lehnt `4.0` ab, will `4` | bei protocolType Float + ganzzahligem Wert (insb. stepSize==1, temperature*/percent) **Integer** auf den Draht schreiben |
| #56 | Gerät sendet Rohwert außerhalb des Enums | `value`: `enumeration.get(raw)`, bei Miss **Rohwert** zurückgeben (kein Crash) |
| #66 | active/selectedProgram meldet UID, die nicht in der Description ist | UID-Lookup defensiv → None statt KeyError |
| — | Update für unbekannte UID | ignorieren + Debug-Log |
| — | sID/msgID/version als String | mit int casten |
| — | Object-Feld mit fehlerhaftem JSON | `]"`→`]`-Workaround |
| #53 | Namespaces variieren (`Dishcare.Dishwasher.*`, nicht nur `BSH.Common.*`) | beliebige Feature-Namen unterstützen, keine Hartkodierung auf `BSH.Common` |

---

## 8. Namespace-Konventionen der Feature-Namen

Feature-Namen folgen Punktnotation und gliedern sich grob in:
- `BSH.Common.{Status|Setting|Option|Event|Command|Root}.*` — geräteübergreifend
- `Dishcare.Dishwasher.{Status|Setting|Option|Event}.*` — Geschirrspüler
- `Cooking.{Oven|Hob|Hood|Common}.{Status|Setting|Option|Event|Command}.*` — Kochfeld/Backofen/Haube
- `LaundryCare.{Common|Washer|Dryer}.{Status|Setting|Option|Event}.*` — Wäschepflege
- `Refrigeration.*`, `ConsumerProducts.*` (Kaffee) u. a.

Diese Namen bilden sich natürlich auf MQTT-Topics ab — siehe `04-geraete-mapping.md`.
