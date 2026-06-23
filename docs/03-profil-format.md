# Home Connect — Profil-Format & Onboarding

> Wie das Gerät onboardet wird: Schlüssel und Gerätebeschreibung kommen **einmalig** aus
> der Cloud über den „Home Connect Profile Downloader". Danach läuft alles lokal.
> Rekonstruiert aus `config_flow.py`, `tests/const.py` und den READMEs beider Repos.

---

## 1. Beschaffung des Profils

Tool: **[bruestel/homeconnect-profile-downloader](https://github.com/bruestel/homeconnect-profile-downloader)**.

1. Mit Home-Connect-Account anmelden (Geräte müssen dort registriert/verbunden sein).
2. **Zielformat „openHAB"** wählen.
3. Es wird ein **ZIP** heruntergeladen, das pro registriertem Gerät drei Dateien enthält.

> Der Schlüsselaustausch mit der Cloud passiert genau einmal beim Download. Im Normalbetrieb
> ist **kein** OAuth/Cloud-Zugriff nötig — `go-homeconnect2mqtt` liest nur das ZIP/JSON ein.

---

## 2. ZIP-Inhalt pro Gerät

| Datei | Inhalt |
|---|---|
| `<serial>.json` | **Index + Schlüssel**: Encryption Key (PSK), ggf. IV, `connectionType`, Geräte-Metadaten, Pfade zu den XML-Dateien |
| `<serial>_DeviceDescription.xml` | Struktur/Typen/Zugriff (siehe `02-datenmodell.md`) |
| `<serial>_FeatureMapping.xml` | Klarnamen/Enum-Namen (siehe `02-datenmodell.md`) |

⚠️ **Nicht** auf die Namenskonvention verlassen — die `.json` referenziert die XML-Dateien
explizit per Feld (`deviceDescriptionFileName` / `featureMappingFileName`). So lädt es auch
die HA-Integration: alle `*.json` im ZIP finden, daraus die XML-Pfade lesen.

---

## 3. `<serial>.json` — exakte Felder

Bestätigt aus `config_flow.py` (`process_zip_file`, `_set_encryption_keys`) und `tests/const.py`:

| JSON-Schlüssel | Bedeutung | Verwendung |
|---|---|---|
| `haId` | eindeutige Geräte-ID, z. B. `010203040506070809` | unique_id; Default-Host im AES-Modus |
| `deviceDescriptionFileName` | Pfad der DeviceDescription.xml im ZIP | XML laden |
| `featureMappingFileName` | Pfad der FeatureMapping.xml im ZIP | XML laden |
| `connectionType` | `"TLS"` oder `"AES"` | Modus-Auswahl |
| `key` | PSK (base64url) — **für beide Modi** das `psk64` | Krypto |
| `iv` | IV (base64url) — **nur AES** | Krypto (`iv64`) |
| `brand` | z. B. `BOSCH`, `SIEMENS` | Anzeige; Default-Host (TLS) |
| `type` | z. B. `Dishwasher`, `Hob`, `Washer` | Anzeige; Default-Host (TLS) |
| `vib` | Verkaufs-/Modellkürzel | Anzeige/DeviceInfo |
| `model` | Modellbezeichnung | Anzeige |

### Schlüssel-/Host-Zuweisung

```
psk64 = key
if connectionType == "AES":
    iv64        = iv
    default_host = haId
else:  # TLS
    iv64        = (nicht gesetzt)
    default_host = f"{brand}-{type}-{haId}"     # z. B. BOSCH-Dishwasher-0102...
```

Der Default-Host ist ein mDNS-auflösbarer Name; falls die Auflösung scheitert, muss eine
**manuelle IP** eingegeben werden können (siehe §5).

---

## 4. Discovery (mDNS / zeroconf)

Die HA-Integration nutzt **ausschließlich zeroconf**: Service `_homeconnect._tcp.local.`.
Kein DHCP, kein SSDP.

Genutzte TXT-Properties:
- `id` → `haId` (= unique_id)
- `vib`, `brand`, `type` → Anzeige
- Host = aufgelöste IP-Adresse aus dem Discovery-Record

**Go-Empfehlung:** mDNS-Discovery optional anbieten (Komfort), aber **immer** manuelle
Host/IP-Konfiguration zulassen. Bei bereits konfiguriertem Gerät die IP per mDNS aktualisieren —
**außer** ein „manueller Host" wurde gesetzt (dann fix lassen). Ein `manual_host`-Flag persistieren.

---

## 5. Onboarding-Ablauf (für `go-homeconnect2mqtt`)

1. ZIP (oder einzelne `.json`+XMLs) einlesen → pro Gerät PSK/IV/connectionType/Host/Description.
2. Optional: mDNS-Discovery zum Auflösen der IP.
3. Verbindungstest (echter Connect + Handshake, Timeout ~20 s).
4. Schlägt der Connect fehl → **manuelle IP** abfragen/konfigurieren (Pflicht-Eskalation, vgl. #410/#297).
5. Gerätekonfiguration persistieren (PSK/IV werden zur Laufzeit gebraucht).

> **Empfehlung für das MQTT-Tool:** Profil **einmal** parsen und die geparste Beschreibung
> als JSON cachen (wie die Library es vorschlägt). Konfiguration der Geräte (Host, PSK, IV,
> Pfad zur Description) in der `config.yaml` bzw. einer geräte-spezifischen Datei — analog zu
> `registers.yaml` im Schwesterprojekt. Siehe `06-architektur-konzept.md`.

---

## 6. Sensible Felder — Logging/Diagnostics

Die HA-Integration redigiert in Diagnostics folgende Felder — **diese niemals im Klartext
loggen oder über MQTT publizieren:**

```
psk / key, aes_iv / iv, deviceID, serialNumber, shipSki, mac
```

**Go-Regel:** strukturiertes Logging mit Redaction für diese Schlüssel; in MQTT-Topics keine
Secrets, allenfalls `haId`/`serialNumber` als (optional maskierte) Geräte-ID.

---

## 7. Onboarding-Fehlerklassen (aus en.json / config_flow)

| Schlüssel | Bedeutung |
|---|---|
| `cannot_connect` | Verbindung/Handshake fehlgeschlagen |
| `auth_failed` | PSK/TLS-Authentifizierung fehlgeschlagen |
| `invalid_profile_file` | ZIP/JSON nicht lesbar |
| `profile_file_parser_error` | XML-Parsing fehlgeschlagen |
| `appliance_not_in_profile_file` | gewähltes Gerät nicht im Profil |
| `all_setup` | alle Geräte bereits eingerichtet |

⚠️ **#410-Lehre:** Die Library wirft **eigene** Exceptions (`ConnectionFailedError`,
`HCHandshakeError`) — **keine** aiohttp-Fehler. Wer nur aiohttp-Fehler fängt, bekommt
„Unknown error" statt des Host-Fallbacks. **Go:** saubere, kategorisierte Fehlertypen +
immer die „manuelle IP"-Eskalation erreichbar machen.
