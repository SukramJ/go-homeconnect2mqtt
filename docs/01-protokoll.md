# Home Connect — Wire-Protokoll-Spezifikation

> Vollständige, eigenständige Spezifikation des lokalen Home-Connect-WebSocket-Protokolls,
> rekonstruiert aus `chris-mc1/homeconnect_websocket` (Python, v1.5.3) — Dateien
> `hc_socket.py`, `session.py`, `message.py`, `const.py`, `helpers.py` und
> `doc/Home_Connect_Protocol.md`.
>
> Diese Datei reicht aus, um den Protokoll-Kern in Go neu zu implementieren. Die
> Referenz-Repos werden nicht mehr benötigt.

---

## 1. Transport & Modus-Auswahl

Jedes Gerät betreibt im LAN einen WebSocket-Server unter dem Pfad **`/homeconnect`**.
Es gibt zwei Sicherheitsmodi; die Auswahl erfolgt allein über das Vorhandensein eines IV:

| Bedingung (aus Profil-`.json`) | Modus | URL |
|---|---|---|
| `connectionType == "AES"`, `iv` gesetzt | **AES** (App-Layer-Krypto) | `ws://<host>:80/homeconnect` |
| `connectionType == "TLS"`, kein `iv` | **TLS-PSK** | `wss://<host>:443/homeconnect` |

Entscheidungslogik (Python `HCSessionBase.__init__`): `iv64` vorhanden → AES-Socket;
sonst `psk64` vorhanden → TLS-Socket. (Ein unverschlüsselter Socket existiert nur für Tests.)

**WebSocket-Details:**
- Endpunktpfad **immer** `/homeconnect`.
- **Keine** HTTP-Header (kein Subprotocol, kein Origin, keine Auth-Header). Authentifizierung
  läuft ausschließlich über TLS-PSK bzw. AES/HMAC.
- **Heartbeat:** Ping alle **20 s** (aiohttp `heartbeat=20`); Pong-Ausbleiben ⇒ Verbindung tot.
  In Go selbst nachbilden (Ping-Ticker + Pong-Deadline).

**IPv6:** Host wird bei Vorkommen von `:` in eckige Klammern gesetzt: `[2a0a:...]`.
⚠️ **Bug-Quelle (#409):** Python wrappt blind — bereits geklammerte Hosts werden zu `[[...]]`.
**Go-Regel:** vorhandene Klammern erst strippen, dann wrappen; Link-local-Zone-IDs (`%eth0`)
gesondert behandeln (yarl/getaddrinfo überleben sie nicht).

---

## 2. Schlüsselableitung

`psk64` und `iv64` sind **URL-safe Base64 ohne Padding**.

```
psk = base64url_decode(psk64)      # 32 Bytes
iv  = base64url_decode(iv64)       # 16 Bytes (nur AES)
```

> Python dekodiert mit angehängtem `"==="` (überschüssiges Padding wird ignoriert).
> In Go: `base64.RawURLEncoding.DecodeString(...)`.

**AES-Modus — KDF (keine HKDF! nur je ein HMAC-SHA256-Aufruf):**

```
enckey = HMAC_SHA256(key=psk, msg="ENC")    # 32 Bytes → AES-Key
mackey = HMAC_SHA256(key=psk, msg="MAC")    # 32 Bytes → HMAC-Key
```

- Labels exakt als ASCII-Bytes: `"ENC"` = `0x45 0x4E 0x43`, `"MAC"` = `0x4D 0x41 0x43`.
- Kein Salt, kein Info-String, keine Expand-Runde.
- `iv` wird **nicht** abgeleitet, sondern direkt verwendet: (a) als initialer CBC-IV,
  (b) als konstanter Präfix in **jeder** HMAC-Berechnung (siehe §3).

**TLS-Modus:** `psk` wird direkt als PSK in den TLS-Handshake gegeben (kein enckey/mackey/iv).

---

## 3. AES-Modus: Nachrichten-Krypto (Kernstück — Quelle von Bug #62)

### 3.1 Konstanten & Verbindungs-State

```
ENCRYPT_DIRECTION = 0x45  # 'E'  — vom Client GESENDETE Nachrichten
DECRYPT_DIRECTION = 0x43  # 'C'  — vom Client EMPFANGENE Nachrichten
MINIMUM_MESSAGE_LENGTH = 32
```

Pro Verbindung gehaltener, fortlaufender State (bei **jedem** `connect()` zurückgesetzt):

```
last_tx_hmac = 16 Null-Bytes        # rollender HMAC der Senderichtung
last_rx_hmac = 16 Null-Bytes        # rollender HMAC der Empfangsrichtung
aes_encrypt  = AES-CBC(enckey, iv)  # EIN persistentes Cipher-Objekt für ALLE TX-Nachrichten
aes_decrypt  = AES-CBC(enckey, iv)  # EIN persistentes Cipher-Objekt für ALLE RX-Nachrichten
```

> **Zwei unabhängige, stateful Krypto-Ketten** (TX und RX). Sowohl die **CBC-Verkettung**
> (letzter Ciphertext-Block = nächster IV) als auch die **HMAC-Kette** laufen über die
> gesamte Verbindung. Das ist die Wurzel von #62 (siehe §3.4).

### 3.2 Senden (Encrypt-then-MAC)

```
1. clear = utf8(message)
2. Padding (NICHT PKCS#7):
     pad_len = 16 - (len(clear) % 16)
     if pad_len == 1: pad_len += 16          # min. 2 Pad-Bytes ⇒ bei Rest 15 wird pad_len = 17
     padded = clear ‖ 0x00 ‖ random(pad_len-2) ‖ byte(pad_len)
   ⇒ erstes Pad-Byte = 0x00, letztes Byte = pad_len; Ergebnis immer Vielfaches von 16.
3. ct = aes_encrypt.encrypt(padded)          # fortlaufende CBC-Kette
4. mac_input = iv ‖ 0x45 ‖ last_tx_hmac ‖ ct
   last_tx_hmac = HMAC_SHA256(mackey, mac_input)[0:16]
5. Wire: ct ‖ last_tx_hmac                    # send_bytes
```

Padding-Beispiele (verifiziert): `""`→16, `"a"`→15, 15 Zeichen→17, 16 Zeichen→16.

### 3.3 Empfangen (MAC-then-Decrypt)

```
1. Frame muss BINARY sein, len >= 32, len % 16 == 0   (sonst verwerfen — siehe Warnung §3.4)
2. ct = buf[:-16]; recv_hmac = buf[-16:]
3. mac_input = iv ‖ 0x43 ‖ last_rx_hmac ‖ ct
   calc = HMAC_SHA256(mackey, mac_input)[0:16]
   if not constant_time_eq(recv_hmac, calc): -> "HMAC Failure" (AuthenticationError)
   last_rx_hmac = recv_hmac                  # Kette NUR bei Erfolg fortschreiben
4. plain = aes_decrypt.decrypt(ct)           # fortlaufende CBC-Kette
5. pad_len = plain[-1]; if len(plain) < pad_len: "Padding Error"
   message = utf8_decode(plain[:-pad_len])
```

### 3.4 Bug #62 „HMAC Failure" — Ursache & Go-Pflichten

Symptom (real beobachtet): **4124** `HMAC Failure` in kurzer Zeit; die Empfangsschleife
reißt ab. Offen seit März 2026, kein Fix im Repo.

**Ursache:** `last_rx_hmac` und der CBC-`aes_decrypt`-State müssen lückenlos mit dem
Server-Stream synchron laufen. Sobald **ein** eingehender Frame nicht den normalen
RX-Pfad durchläuft (Verlust, Nicht-BINARY-Frame, Längen-/Padding-Fehler, unerwartete
Server-Nachricht), ist die Kette **dauerhaft** desynchronisiert → **jeder** Folge-Frame
schlägt fehl. Es gibt keinen Resync im laufenden Stream. Die Python-Lib liest fälschlich
auf demselben Socket weiter → Endlos-Flut.

**Pflichten für die Go-Implementierung:**
1. HMAC immer **constant time** vergleichen (`hmac.Equal`).
2. Bei **erster** HMAC-Failure (oder Padding-/Decode-Fehler): Socket **sofort schließen
   und voll reconnecten** — niemals auf desyncter Kette weiterlesen.
3. TX- und RX-Krypto-State je mit eigenem Mutex **strikt serialisieren** (zwei parallele
   `send()` würden `last_tx_hmac`/CBC korrumpieren).
4. CBC als echten **Stream** führen (letzten Ciphertext-Block als nächsten IV mitnehmen),
   nicht jede Nachricht mit dem statischen `iv` neu verschlüsseln.
5. Richtungs-Bytes aus Sicht des **Senders**: TX immer `'E'` (0x45), RX immer `'C'` (0x43).
   (Der Server signiert seine Nachrichten mit `'C'`, daher verifiziert der Client mit `'C'`.)

---

## 4. TLS-PSK-Modus

```
psk = base64url_decode(psk64)
TLS-Client-Context:
  max_version   = TLS 1.2          # auf 1.2 begrenzt
  ciphers       = "PSK"            # OpenSSL: alle PSK-Suiten (TLS_PSK_WITH_AES_*)
  check_hostname = false
  verify_mode    = CERT_NONE       # keine Server-Zertifikatsprüfung
  psk_client_callback -> (identity=None/leer, psk=psk)
```

- Nachrichten werden als **reiner UTF-8-Text** über den TLS-Tunnel ausgetauscht
  (keine zusätzliche App-Layer-Krypto — TLS schützt alles).
- Auth-Fehler erkennbar als TLS-Handshake-Fehler.

⚠️ **Go-Portierungshürde:** `crypto/tls` unterstützt **kein** TLS-PSK nativ. Optionen:
- Eine PSK-fähige TLS-1.2-Bibliothek (z. B. cgo-OpenSSL-Binding) verwenden, **oder**
- den AES-Modus priorisieren (viele Geräte sprechen ohnehin AES auf Port 80; AES ist in
  Go mit Bordmitteln vollständig umsetzbar).

> **Empfehlung:** AES-Modus zuerst implementieren (deckt neuere Geräte ab), TLS-PSK als
> zweite Ausbaustufe. Welcher Modus pro Gerät gilt, steht in der Profil-`.json`
> (`connectionType`).

---

## 5. Nachrichten-Format

JSON über WebSocket, kompakt serialisiert (Separatoren `,` und `:`):

```json
{"sID":<int>,"msgID":<int>,"resource":"<str>","version":<int>,"action":"GET|POST|RESPONSE|NOTIFY","data":[ ... ],"code":<int?>}
```

| Feld | Typ | Bedeutung |
|---|---|---|
| `sID` | int | Session-ID (vom Gerät vorgegeben, aus `/ei/initialValues`) |
| `msgID` | int | Message-ID, monoton steigend pro gesendeter Nachricht |
| `resource` | string | Endpunkt, z. B. `/ci/services`, `/ro/values` |
| `version` | int | Service-Version (aus `/ci/services`, Default 1) |
| `action` | enum | `GET` / `POST` / `RESPONSE` / `NOTIFY` |
| `data` | array | Nutzdaten — **beim Senden immer Liste** (Einzel-Dict wird zu `[dict]`) |
| `code` | int? | nur in Responses; **≠ null ⇒ Fehler** (→ `CodeResponsError`) |

**Defensives Parsen (Pflicht):**
- `sID`, `msgID`, `version` beim Lesen mit `int(...)` casten — Geräte senden Zahlen teils als String.
- Object-Felder können fehlerhaftes JSON enthalten; Workaround der Lib: `]"` → `]` ersetzen,
  dann erneut parsen.

---

## 6. Handshake — exakte Sequenz

### 6.1 Pre-Handshake

Client wartet auf die **erste vom Gerät gesendete** Nachricht. Sie muss
`resource == "/ei/initialValues"` sein. Daraus übernehmen:
- `sID = msg.sID`
- `last_msg_id = msg.data[0].edMsgID` (Start-msgID für eigene Nachrichten)

Beispiel-Frame:
```json
{"sID":<sid>,"msgID":<server_mid>,"resource":"/ei/initialValues","version":2,"action":"POST","data":[{"edMsgID":<client_start_mid>}]}
```

### 6.2 Handshake-Schritte

1. **RESPONSE auf initialValues** (gleiche resource/msgID/sID):
   ```json
   data: [{"deviceType": <2 wenn version==1, sonst "Application">,
           "deviceName": "<app_name>", "deviceID": "<app_id>"}]
   ```
   (`app_name`/`app_id` frei wählbar, z. B. `"go-homeconnect2mqtt"` / zufällige Hex-ID.)
2. **`GET /ci/services` (version 1)** → Response: `[{"service":"ci","version":x}, ...]`
   → in `service_versions` ablegen.
3. **Falls `ci`-Version < 3:**
   - **`GET /ci/authentication`** mit `data:[{"nonce":"<token>"}]`,
     `token = base64url(random 32 Bytes)` ohne `=`-Padding. Gerät antwortet mit eigener Nonce.
   - **`GET /ci/info`** (Fehler-Code tolerieren/ignorieren) → HW-Info.
4. **Falls `iz` in services:** `GET /iz/info`.
5. **Falls `ei`-Version == 2:** `NOTIFY /ei/deviceReady` (fire-and-forget, keine Response).
6. **Falls `ni` in services:** `GET /ni/info`.
7. State → **CONNECTED**.

### 6.3 Post-Connect-Init (sofort nach CONNECTED)

1. **`GET /ro/allDescriptionChanges`** → Entities updaten.
2. **`GET /ro/allMandatoryValues`** → Entities updaten (alle Pflichtwerte).

⚠️ Beide liefern bei manchen Geräten/Firmwares `500 InternalServerError`
(local_hass #255/#128/#177). **Go:** tolerant behandeln + retrybar, kein harter Abbruch.

### 6.4 msgID/sID/version-Defaults (vor dem Senden)

- `version`: falls nicht gesetzt → `service_versions[resource[1:3]]` (die 2 Zeichen nach `/`,
  z. B. `/ci/...` → `"ci"`), Default `1`.
- `sID`: falls nicht gesetzt → aktuelle Session-ID.
- `msgID`: falls nicht gesetzt → `last_msg_id`, danach `last_msg_id += 1`.

### 6.5 Request/Response-Korrelation

`send_sync`: pro `msgID` eine Warteschlange (Kapazität 1) registrieren; eingehende
`RESPONSE`-Nachrichten werden über `msgID` zugeordnet. **Timeout 20 s.** `code != null`
in der Response ⇒ Fehler (`CodeResponsError`). Nicht-Response-Nachrichten (NOTIFY) gehen
an den allgemeinen Message-Handler.

---

## 7. Werte lesen / setzen / Programme (`/ro`-Service)

> Der `/ro`-Abschnitt ist in `doc/Home_Connect_Protocol.md` leer — die folgende Semantik ist
> aus `appliance.py` / `entities.py` rekonstruiert.

**Lesen:** Kein Einzel-Read im Normalbetrieb. Werte kommen über:
- Bulk beim Connect: `GET /ro/allDescriptionChanges` + `GET /ro/allMandatoryValues` (RESPONSE).
- Push: **`NOTIFY /ro/values`** und **`NOTIFY /ro/descriptionChange`**.

Jedes Update-Item: `{"uid":<int>, "value":..., "access":..., "available":..., "min":..., "max":..., "stepSize":..., "execution":...}` (alle außer `uid` optional). Unbekannte `uid` → ignorieren (nur Debug-Log).

**Wert setzen:**
```json
{"resource":"/ro/values","action":"POST","data":[{"uid":<uid>,"value":<typed value>}]}
```
Bei `RESPONSE` ohne `code` lokalen Schattenwert (`value_shadow`) optimistisch setzen;
der echte Wert folgt per `NOTIFY /ro/values`.

**Command ausführen:** identisch `POST /ro/values` mit `{"uid":..,"value":<int>}`.

**Programm wählen / starten:**
```json
POST /ro/selectedProgram   data: {"program":<uid>, "options":[{"uid":..,"value":..}, ...]}
POST /ro/activeProgram     data: {"program":<uid>, "options":[ ... ]}
```
Fehlende readwrite-Optionen werden mit ihrem Schattenwert aufgefüllt.

**Events quittieren:** Commands `BSH.Common.Command.AcknowledgeEvent` /
`BSH.Common.Command.RejectEvent` mit der Event-UID.

> ⚠️ **Programmstart ist gerätespezifisch** (siehe `05-resilienz.md`): blindes
> `POST /ro/activeProgram` scheitert je nach Gerät mit 400/501/541. Kochfelder verlangen
> direkten `POST /ro/selectedProgram` (`validate=false`); Trockner/Waschmaschinen brauchen
> ein READWRITE-Zeitfenster auf `BSH.Common.Root.ActiveProgram` (uid 256).

---

## 8. Service-Katalog (aus `doc/Home_Connect_Protocol.md`)

Services werden über `/ci/services` mit Versionen gemeldet. Bekannte Endpunkte:

### `ei` (External Interface)
- **`POST /ei/initialValues`** — erste Gerätenachricht (enthält `edMsgID`). Antwort:
  `[{"deviceType":2|"Application","deviceName":..,"deviceID":..}]` (v1: `2`, v2: `"Application"`).
- **`NOTIFY /ei/deviceReady`** — Client meldet Bereitschaft (nur ei v2).

### `ci` (Command Interface)
- **`GET /ci/services`** — Liste `{service, version}`. v1 immer möglich.
- **`GET /ci/authentication`** — 32-Byte-Nonce (hex/base64), Gerät antwortet mit eigener Nonce.
- **`GET /ci/info`** — HW-Info:
  ```json
  {"deviceID":"SIEMENS-SN8S3647TE-68E05997A408","eNumber":"SN8S3647TE/33","brand":"SIEMENS",
   "vib":"SN8S3647TE","mac":"68-99-A4-0E-05-78","haVersion":"1.0","swVersion":"1.4.9",
   "hwVersion":"5056177560","deviceType":32,"deviceInfo":"DISHWASHER","customerIndex":33,
   "serialNumber":"017376983004000136","fdString":"8949","shipSki":"55DE...B953B1"}
  ```
- `GET /ci/tzinfo`, `GET /ci/networkdetails`, `GET /ci/wifiSetting`, `GET /ci/wifiNetworks`
- ci v3: `GET /ci/registeredDevices`, `GET /ci/pairableDevices`

### `iz` (Identification)
- **`GET /iz/info`** — HW-Info (ähnlich `/ci/info`, teils mit `deviceType:"Dishwasher"`).

### `ni` (Network Interface)
- **`GET /ni/info`** — Interface-Info (type, ssid, rssi, status, euiAddress, ipV4/ipV6).
- **`GET /ni/config`** — Interface-Config (automaticIPv4/6, manualIPv4/6).

### `ro` (Remote Operation) — Werte & Programme, siehe §7
- `GET /ro/allMandatoryValues`, `GET /ro/allDescriptionChanges`
- `POST /ro/values`, `NOTIFY /ro/values`, `NOTIFY /ro/descriptionChange`
- `POST /ro/selectedProgram`, `POST /ro/activeProgram`

---

## 9. Response-/Error-Codes (`const.py`, vollständig)

`code` in einer RESPONSE ≠ null ⇒ Fehler. Vollständige Tabelle:

| Code | Bedeutung | Code | Bedeutung |
|---|---|---|---|
| 200 | OK | 524 | NotAvailable |
| 202 | Accepted | 525 | WriteRequest NotAvailable |
| 400 | BadRequest | 526 | ReadRequest NotAvailable |
| 403 | Forbidden | 527 | NotAvailableByList |
| 404 | NotFound | 528 | WriteRequest NotAvailableByList |
| 405 | MethodNotAllowed | 529 | ReadRequest NotAvailableByList |
| 413 | RequestEntityTooLong | 530 | NoExecution |
| 414 | RequestUriTooLong | 531 | ValueOutOfRange |
| 429 | TooManyRequests | 532 | InvalidUIDValue |
| 500 | InternalServerError | 533 | Incomplete |
| 501 | NotImplemented | 534 | Inconsistent |
| 502 | BadGateway | 535 | CmdViolation |
| 503 | ServiceUnavailable | 536 | InvalidFormat |
| 504 | GatewayTimeout | 537 | RemoteControlNotActive |
| 507 | InsufficientMemory | 538 | RemoteStartNotActive |
| 512 | UnknownUID | 539 | LockedByLocalControl |
| 513 | WriteRequest UnknownUID | 540 | DeviceStateNotCompliant |
| 514 | ReadRequest UnknownUID | 541 | ProcessStateNotCompliant |
| 515 | Busy | 542 | BackendNotConnected |
| 516 | WriteRequest Busy | 543 | EnergyManagementNotConnected |
| 517 | ReadRequest Busy | 544 | NotInLocalWiFi |
| 518 | NoAccess | 519 | WriteRequest NoAccess |
| 520 | ReadRequest NoAccess | 521 | NoAccessByList |
| 522 | WriteRequest NoAccessByList | 523 | ReadRequest NoAccessByList |

Praktisch relevant: **400** (falscher Programmstart/Float-statt-Int), **500**
(`/ro/allMandatoryValues`-Handshake), **404** (Handshake-Reconnect-Schleife, #403),
**541** (Schreiben außerhalb des READWRITE-Fensters, #384).

---

## 10. Verbindungs-State-Machine & Reconnect

```
CONNECTING → HANDSHAKE → CONNECTED
   (Verlust) → RECONNECTING → (erneut HANDSHAKE) → CONNECTED
   (Fehler)  → ABNORMAL_CLOSURE
   (close)   → CLOSING → CLOSED
```

**Python-Reconnect (`HCSessionReconnect`):** Bei geschlossenem Socket im `finally` der
Empfangsschleife → falls `reconnect` aktiv → State RECONNECTING + `_reconnect_loop`.
Die Schleife versucht `socket.connect()` + Pre-Handshake + Recv-Loop + Handshake;
bei `ConnectionFailedError` → `continue` **ohne Backoff** (⚠️ Bug #41: 1000e Fehler bei
dauer-offline Geräten); bei `HCHandshakeError` → CLOSING.

**State-Reset bei jedem Reconnect (zwingend korrekt nachzubilden):**
1. **Krypto:** neuer Socket ⇒ `last_rx_hmac`/`last_tx_hmac` = 16 Null-Bytes, **neue** CBC-Objekte (Kette startet beim `iv`).
2. **Session:** `sID` + `last_msg_id` neu aus frischem `/ei/initialValues`.
3. **Services:** `service_versions` neu aus `/ci/services`.
4. **Entities:** nach CONNECTED erneut `/ro/allMandatoryValues` → alle Werte resyncen.

**Go-Verbesserungen gegenüber Python (Resilienz-Kernanforderung):**
- **Exponentielles Backoff mit Jitter** (z. B. 1 s → 30 s, ±500 ms) statt Tight-Loop.
- **Log-Rate-Limiting** für wiederkehrende Fehler (offline-Gerät = Normalzustand, kein Spam).
- **Offline ist kein Fehler:** Gerät schläft/aus ⇒ „unavailable" + LWT, kein Crash, keine
  Endlosschleife.
- Bei HMAC-Failure (#62): Reconnect erzwingen statt weiterlesen.
- Konstanten sind in Python vorgesehen, aber ungenutzt: `MAX_CONNECT_TIMEOUT=60`,
  `TIMEOUT_INCREASE_FACTOR=1.2`, `DEFAULT_HANDSHAKE_TIMEOUT=60`, `DEFAULT_SEND_TIMEOUT=20`.

---

## 11. Go-Reimplementierung — Kompakt-Checkliste

1. Modus: `iv64` → AES (ws:80), sonst TLS-PSK (wss:443); Pfad `/homeconnect`; Heartbeat 20 s.
2. KDF: `enckey=HMAC-SHA256(psk,"ENC")`, `mackey=HMAC-SHA256(psk,"MAC")`; `iv` direkt; Base64 RawURL.
3. AES-TX: Custom-Padding → CBC-Stream-Encrypt → `HMAC(mackey, iv‖'E'‖last_tx_hmac‖ct)[:16]`; Wire `ct‖mac`.
4. AES-RX: split `ct|mac` → HMAC `iv‖'C'‖last_rx_hmac‖ct` constant-time → CBC-Stream-Decrypt → Unpad. Fehler ⇒ Reconnect.
5. TX/RX-State je mit Mutex serialisieren.
6. Handshake: `/ei/initialValues` → RESPONSE → `/ci/services` → (ci<3: `/ci/authentication`+`/ci/info`) → (iz: `/iz/info`) → (ei2: NOTIFY `/ei/deviceReady`) → (ni: `/ni/info`).
7. Post-Connect: `/ro/allDescriptionChanges` + `/ro/allMandatoryValues` (500 tolerieren).
8. msgID monoton; Response über msgID korrelieren; Timeout 20 s; `code!=null` = Fehler.
9. Live-Updates: NOTIFY `/ro/values` + `/ro/descriptionChange`.
10. Schreiben: `POST /ro/values [{uid,value}]`; Programme: `/ro/selectedProgram` / `/ro/activeProgram`.
11. Reconnect mit Backoff + Jitter + Log-Throttling; voller State-Reset; offline ≠ Fehler.
```
