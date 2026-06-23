# Referenz-Quellen (verbatim) — für die Portierung & Tests

> Hier sind die **unverzichtbaren Original-Artefakte** verbatim gesichert, damit die
> Referenz-Repos (`../aaa_homeconnect_websocket`, `../aaa_homeconnect_local_hass`) gelöscht
> werden können: der Krypto-Kern als 1:1-Referenz und die Test-Fixtures für den Parser.

---

## 1. AES-Krypto — Original (`hc_socket.py`, Python) als Portierungs-Referenz

```python
# Konstanten
ENCRYPT_DIRECTION = b"\x45"  # 'E'
DECRYPT_DIRECTION = b"\x43"  # 'C'
MINIMUM_MESSAGE_LENGTH = 32

# Init (AesSocket.__init__)
psk = urlsafe_b64decode(psk64 + "===")
self._iv = urlsafe_b64decode(iv64 + "===")
self._enckey = hmac.digest(psk, b"ENC", digest="sha256")
self._mackey = hmac.digest(psk, b"MAC", digest="sha256")

# Connect (State-Reset pro Verbindung!)
self._last_rx_hmac = bytes(16)
self._last_tx_hmac = bytes(16)
self._aes_encrypt = AES.new(self._enckey, AES.MODE_CBC, self._iv)   # EIN Objekt, CBC streamt
self._aes_decrypt = AES.new(self._enckey, AES.MODE_CBC, self._iv)

# Senden
async def send(self, clear_msg):
    clear_msg = bytes(clear_msg, "utf-8")
    pad_len = 16 - (len(clear_msg) % 16)
    if pad_len == 1:
        pad_len += 16
    clear_msg = clear_msg + b"\x00" + get_random_bytes(pad_len - 2) + bytearray([pad_len])
    enc_msg = self._aes_encrypt.encrypt(clear_msg)
    hmac_msg = self._iv + ENCRYPT_DIRECTION + self._last_tx_hmac + enc_msg
    self._last_tx_hmac = hmac.digest(self._mackey, hmac_msg, digest="sha256")[0:16]
    await self._websocket.send_bytes(enc_msg + self._last_tx_hmac)

# Empfangen
async def _receive(self, message):
    # message.type muss BINARY sein
    buf = message.data
    if len(buf) < MINIMUM_MESSAGE_LENGTH: raise ValueError("too short")
    if len(buf) % 16 != 0:                raise ValueError("unaligned")
    enc_msg  = buf[0:-16]
    recv_hmac = buf[-16:]
    hmac_msg = self._iv + DECRYPT_DIRECTION + self._last_rx_hmac + enc_msg
    calculated_hmac = hmac.digest(self._mackey, hmac_msg, digest="sha256")[0:16]
    if not hmac.compare_digest(recv_hmac, calculated_hmac):
        raise AuthenticationError("HMAC Failure")     # → in Go: Reconnect erzwingen!
    self._last_rx_hmac = recv_hmac
    msg = self._aes_decrypt.decrypt(enc_msg)
    pad_len = msg[-1]
    if len(msg) < pad_len: raise ValueError("Padding Error")
    return msg[0:-pad_len].decode("utf-8")
```

**TLS-PSK (`TlsSocket`):**
```python
psk = urlsafe_b64decode(psk64 + "===")
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
ctx.maximum_version = ssl.TLSVersion.TLSv1_2
ctx.set_ciphers("PSK")
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE
ctx.set_psk_client_callback(lambda _: (None, psk))
# URL: wss://{host}:443/homeconnect ; AES-URL: ws://{host}:80/homeconnect
```

**Message (`message.py`):** JSON-Felder `sID,msgID,resource,version,action,data?,code?`;
`action ∈ {GET,POST,RESPONSE,NOTIFY}`; `data` beim Senden immer Liste; kompakte Separatoren.

---

## 2. Fixture: `DeviceDescription_short.xml` (verbatim)

> Minimal-Beispiel für Parser-Tests (Nesting von `*List`, alle Element-Arten, Enum + Subset).

```xml
<?xml version="1.0" encoding="UTF-8"?>
<device>
    <description>
        <type>HomeAppliance</type>
        <brand>Fake_Brand</brand>
        <model>Fake_Model</model>
        <version>2</version>
        <revision>0</revision>
        <pairableDeviceTypes>
            <deviceType>Application</deviceType>
        </pairableDeviceTypes>
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
        <commandList access="writeOnly" available="true" uid="0008">
            <command access="writeOnly" available="true" refCID="01" refDID="00" uid="100E" />
        </commandList>
    </commandList>
    <optionList access="readWrite" available="true" uid="0009">
        <option access="read" available="true" refCID="11" refDID="A0" uid="1011" liveUpdate="true" />
        <optionList access="readWrite" available="true" uid="000A">
            <option access="read" available="true" refCID="10" refDID="A0" uid="1012" />
        </optionList>
    </optionList>
    <programGroup available="true" uid="000B">
        <program available="true" execution="selectOnly" uid="1015">
            <option access="readWrite" available="true" liveUpdate="false" default="true" refUID="1011" />
        </program>
        <programGroup available="true" uid="000C">
            <program available="true" execution="selectOnly" uid="1016">
                <option access="readWrite" available="true" liveUpdate="false" default="true" refUID="1011" />
            </program>
        </programGroup>
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

## 3. Fixture: `FeatureMapping_short.xml` (verbatim)

```xml
<?xml version="1.0" encoding="utf-8"?>
<featureMappingFile>
  <featureDescription>
    <feature refUID="1001">Status.1</feature>
    <feature refUID="1002">Status.2</feature>
    <feature refUID="1005">Setting.1</feature>
    <feature refUID="1006">Setting.2</feature>
    <feature refUID="1009">Event.1</feature>
    <feature refUID="100A">Event.2</feature>
    <feature refUID="100B">Event.3</feature>
    <feature refUID="100D">Command.1</feature>
    <feature refUID="100E">Command.2</feature>
    <feature refUID="1011">Option.1</feature>
    <feature refUID="1012">Option.2</feature>
    <feature refUID="1015">Program.1</feature>
    <feature refUID="1016">Program.2</feature>
    <feature refUID="1019">ActiveProgram</feature>
    <feature refUID="101A">SelectedProgram</feature>
    <feature refUID="101B">ProtectionPort</feature>
  </featureDescription>
  <errorDescription>
    <error refEID="2001">Error.1</error>
    <error refEID="2002">Error.2</error>
  </errorDescription>
  <enumDescriptionList>
    <enumDescription refENID="3001" enumKey="EventState">
      <enumMember refValue="0">Off</enumMember>
      <enumMember refValue="1">Present</enumMember>
      <enumMember refValue="2">Confirmed</enumMember>
    </enumDescription>
    <enumDescription refENID="3002" enumKey="EnumType.1">
      <enumMember refValue="0">Open</enumMember>
      <enumMember refValue="1">Closed</enumMember>
    </enumDescription>
  </enumDescriptionList>
</featureMappingFile>
```

**Erwartetes Parse-Ergebnis (Auszug, zur Test-Verifikation):**
- `uid 0x1002` → name `Status.2`, contentType `enumeration`, protocolType `Integer`,
  enumeration = `{0:"Open", 1:"Closed"}` (refCID `03`, enumerationType `3002`).
- `uid 0x1005` → name `Setting.1`, contentType `boolean`, protocolType `Boolean`,
  access `readwrite`, min 0, max 10, step 1.
- `enid 0x3003` (subsetOf 3001) → `{1:"Present"}`.
- `uid 0x1015` → Program `Program.1`, execution `selectonly`, option refUID `0x1011`.

> Größere, reale Fixtures (`DeviceDescription.xml`/`FeatureMapping.xml` voller Umfang) lagen in
> `aaa_homeconnect_websocket/tests/`. Für die Erstimplementierung genügen die obigen
> Kurzfassungen; reale Geräteprofile kommen ohnehin aus dem Profile-Downloader.

---

## 4. Herkunft & Versionsstand

- `chris-mc1/homeconnect_websocket` — Python-Protokoll-Library, Stand v1.5.3 (März 2026).
  Quelle der Krypto-/Protokoll-/Parser-Logik.
- `chris-mc1/homeconnect_local_hass` — HA-Integration, Stand Beta 1.0.5b10/b11 (Juni 2026).
  Quelle der Onboarding-/Mapping-Logik und der meisten Praxis-Issues.
- `bruestel/homeconnect-profile-downloader` — Profil-Beschaffung (Zielformat „openHAB").

Lizenz-Hinweis: Beim Übernehmen von Logik/Konstanten die Lizenzen der Quell-Repos beachten
(die Library hatte zum Analysezeitpunkt ein offenes LICENSE-Issue, #69).
