# Test Fixtures & Provenance

> Clean-room note: this project does **not** reproduce or port any third-party
> source code. The protocol behaviour is specified language-neutrally in
> `01-protocol.md`; the data model in `02-data-model.md`. The fixtures below are
> our own synthetic test data — the element/attribute schema is the factual Home
> Connect XML structure, the brand/model, feature names and enum keys are ours.

---

## 1. Crypto & protocol behaviour

The AES app-layer crypto (KDF, streaming AES-CBC, the rolling per-direction
HMAC chain, the custom padding) and the message/handshake behaviour are
specified language-neutrally in **`01-protocol.md`** (§2 keys, §3 AES framing,
§4 TLS-PSK, §5 message format, §6 handshake). The Go implementation in
`internal/homeconnect` is derived from that specification; comments cite the
relevant spec section.

No third-party crypto source is included here (see §4 on licensing).

---

## 2. Fixture: DeviceDescription (synthetic)

Minimal example for parser tests: nesting of `*List`, all element kinds, enum +
subset. This is the fixture used verbatim by `internal/profile/parser_test.go`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<device>
    <description>
        <type>HomeAppliance</type>
        <brand>DemoBrand</brand>
        <model>DemoModel</model>
        <version>2</version>
        <revision>0</revision>
    </description>
    <statusList access="read" available="true" uid="0001">
        <status access="read" available="true" refCID="01" refDID="00" uid="1001" />
        <statusList access="read" available="true" uid="0002">
            <status access="read" available="true" enumerationType="3002" refCID="03" refDID="00" uid="1002" />
        </statusList>
    </statusList>
    <settingList access="readWrite" available="true" uid="0003">
        <setting access="readWrite" available="true" refCID="01" uid="1005" max="10" min="0" stepSize="1" initValue="1" default="0" refDID="00" passwordProtected="false" notifyOnChange="false" />
        <settingList access="readWrite" available="true" uid="0004">
            <setting access="readWrite" available="true" refCID="01" refDID="00" uid="1006" />
        </settingList>
    </settingList>
    <eventList uid="0005">
        <event enumerationType="3001" handling="acknowledge" level="hint" refCID="03" refDID="80" uid="1009" />
        <eventList uid="0006">
            <event enumerationType="3001" handling="acknowledge" level="hint" refCID="03" refDID="80" uid="100A" />
            <event enumerationType="3003" handling="acknowledge" level="hint" refCID="03" refDID="80" uid="100B" />
        </eventList>
    </eventList>
    <commandList access="writeOnly" available="true" uid="0007">
        <command access="writeOnly" available="true" refCID="01" refDID="00" uid="100D" />
    </commandList>
    <optionList access="readWrite" available="true" uid="0009">
        <option access="read" available="true" refCID="11" refDID="A0" uid="1011" liveUpdate="true" />
    </optionList>
    <programGroup available="true" uid="000B">
        <program available="true" execution="selectOnly" uid="1015">
            <option access="readWrite" available="true" liveUpdate="false" default="true" refUID="1011" />
        </program>
    </programGroup>
    <activeProgram access="readWrite" validate="true" uid="1019" />
    <selectedProgram access="readWrite" fullOptionSet="false" uid="101A" />
    <protectionPort access="readWrite" available="true" uid="101B" />
    <enumerationTypeList>
        <enumerationType enid="3001">
            <enumeration value="0" />
            <enumeration value="1" />
            <enumeration value="2" />
        </enumerationType>
        <enumerationType enid="3003" subsetOf="3001">
            <enumeration value="1" />
        </enumerationType>
    </enumerationTypeList>
</device>
```

---

## 3. Fixture: FeatureMapping (synthetic)

```xml
<?xml version="1.0" encoding="utf-8"?>
<featureMappingFile>
  <featureDescription>
    <feature refUID="1001">Demo.Status.Alpha</feature>
    <feature refUID="1002">Demo.Status.Door</feature>
    <feature refUID="1005">Demo.Setting.Power</feature>
    <feature refUID="1006">Demo.Setting.Beta</feature>
    <feature refUID="1009">Demo.Event.One</feature>
    <feature refUID="100A">Demo.Event.Two</feature>
    <feature refUID="100B">Demo.Event.Three</feature>
    <feature refUID="100D">Demo.Command.One</feature>
    <feature refUID="1011">Demo.Option.One</feature>
    <feature refUID="1015">Demo.Program.Alpha</feature>
    <feature refUID="1019">Demo.Root.ActiveProgram</feature>
    <feature refUID="101A">Demo.Root.SelectedProgram</feature>
    <feature refUID="101B">Demo.Root.ProtectionPort</feature>
  </featureDescription>
  <errorDescription>
    <error refEID="2001">Demo.Error.One</error>
  </errorDescription>
  <enumDescriptionList>
    <enumDescription refENID="3001" enumKey="EventState">
      <enumMember refValue="0">Off</enumMember>
      <enumMember refValue="1">Present</enumMember>
      <enumMember refValue="2">Confirmed</enumMember>
    </enumDescription>
    <enumDescription refENID="3002" enumKey="DoorState">
      <enumMember refValue="0">Open</enumMember>
      <enumMember refValue="1">Closed</enumMember>
    </enumDescription>
  </enumDescriptionList>
</featureMappingFile>
```

**Expected parse result (excerpt, for test verification):**
- `uid 0x1002` → name `Demo.Status.Door`, contentType `enumeration`, protocolType `Integer`,
  enumeration = `{0:"Open", 1:"Closed"}` (refCID `03`, enumerationType `3002`).
- `uid 0x1005` → name `Demo.Setting.Power`, contentType `boolean`, protocolType `Boolean`,
  access `readwrite`, min 0, max 10, step 1.
- `enid 0x3003` (subsetOf 3001) → `{1:"Present"}`.
- `uid 0x1015` → Program `Demo.Program.Alpha`, execution `selectonly`, option refUID `0x1011`.

Real device profiles come from the profile downloader at runtime and are far larger;
these synthetic fixtures only exercise the parser.

---

## 4. Provenance & licensing

The behaviour implemented here was reverse-engineered/specified from the public
reference projects below and captured in our own words in `00`–`05`:

- `chris-mc1/homeconnect_websocket` — Python protocol library (analysed at v1.5.3).
  **Has no license file (all rights reserved).** Therefore **no code or constants
  are copied or ported verbatim** from it; the Go implementation is a clean-room
  reimplementation from the protocol specification in `01-protocol.md`.
- `chris-mc1/homeconnect_local_hass` — Home Assistant integration (MIT). Only
  concepts (device mapping, onboarding, the resilience issue catalogue) were
  reused, reimplemented in Go.
- `bruestel/homeconnect-profile-downloader` — external profile acquisition tool
  (MIT). Used as-is to produce the profile archive; only its output format is
  consumed (it is **not** reimplemented here).

See `NOTICE.md` for the full attribution and third-party license overview.
