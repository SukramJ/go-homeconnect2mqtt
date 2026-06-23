# Home Connect lokal – Recherche & Erkenntnisse

> Stand: 2026-06-23. Grundlage für `go-homeconnect2mqtt`. Quellen sind die
> beiden Repos von `chris-mc1`:
> [`homeconnect_local_hass`](https://github.com/chris-mc1/homeconnect_local_hass)
> (Home-Assistant-Integration) und
> [`homeconnect_websocket`](https://github.com/chris-mc1/homeconnect_websocket)
> (zugrunde liegende Python-Protokoll-Library).

Dieses Dokument fasst zusammen, **wie** der lokale Home-Connect-Zugriff
funktioniert und **wie zuverlässig** er in der Praxis ist – als Vorarbeit für
eine eigene Go-Implementierung (Home Connect ⇒ MQTT).

---

## 1. Überblick

Home-Connect-Geräte (Bosch / Siemens / Gaggenau / Neff) lassen sich **komplett
lokal** ohne Cloud steuern. Jedes Gerät betreibt im LAN einen
**WebSocket-Server**. Die Integration `homeconnect_local_hass` ist nur die
Home-Assistant-Anbindung; die eigentliche Protokolllogik steckt in der Library
`homeconnect_websocket`.

Wichtig: Die für Verschlüsselung und Gerätebeschreibung nötigen Daten kommen
**einmalig aus der Cloud** über das externe Tool **„Home Connect Profile
Downloader"** (Zielformat `openHAB` wählen). Danach läuft der Betrieb rein
lokal.

---

## 2. Architektur / Protokoll

### 2.1 Transport: lokaler WebSocket

Verbindung per IP/Hostname (Auto-Discovery, sonst manuelle IP-Eingabe). Es gibt
**zwei Sicherheitsmodi**, abhängig vom Gerät:

| Modus       | Transport          | Verschlüsselung                                                   |
|-------------|--------------------|------------------------------------------------------------------|
| **TLS-PSK** (ältere Geräte) | `wss://<ip>:443` | TLS mit Pre-Shared Key (`psk64`)                                 |
| **AES** (neuere Geräte)     | `ws://<ip>:80`   | Verschlüsselung auf **Anwendungsebene**: AES-CBC mit Key + IV (`iv64`), Integrität über **HMAC** |

Der WebSocket-Endpunktpfad ist `/homeconnect`. Im AES-Modus ist der Transport
selbst unverschlüsselt (`ws://`), aber jede Nachricht wird mit aus dem PSK
abgeleiteten AES-Schlüsseln ver-/entschlüsselt und per fortlaufendem HMAC
abgesichert.

### 2.2 Authentifizierung: PSK aus dem Geräteprofil

Die Schlüssel stammen **nicht vom Gerät**, sondern aus dem heruntergeladenen
Profil-Archiv. Pro Gerät enthält es:

- **`<seriennummer>.json`** – Metadaten + **Encryption Key (PSK)** und ggf. IV
- **`<seriennummer>_DeviceDescription.xml`** – Fähigkeiten des Geräts:
  verfügbare UIDs, Optionen, Enums, Min/Max-Werte
- **`<seriennummer>_FeatureMapping.xml`** – Zuordnung der numerischen UIDs zu
  Feature-Namen wie `BSH.Common.Setting.PowerState`

### 2.3 Datenmodell & Nachrichten

Aus den beiden XML-Dateien baut die Library ein `DeviceDescription`-Objekt. Die
Kommunikation läuft über ein **hierarchisches Ressourcen-/Feature-Modell** in
Punktnotation (z. B. `BSH.Common.Setting.PowerState`). Nach einem Handshake
(Initialwerte/Session austauschen) werden Zustände synchronisiert sowie Werte
gelesen/gesetzt. Die HA-Integration mappt diese Features auf Entities
(Sensoren, Switches, Selects …).

### 2.4 Parallele zu HomeMatic/godevccu

Strukturell vergleichbar mit HomeMatic/CCU: So wie `godevccu` Geräte über
eingebettete `*_DeviceDescription`/Paramset-JSONs beschreibt, beschreibt Home
Connect seine Geräte über `*_DeviceDescription.xml` + `*_FeatureMapping.xml`.

---

## 3. Zuverlässigkeit & bekannte Probleme

**Kurzfazit:** Die Grundkonstruktion gilt als „solide Basis", aber es gibt
**wiederkehrende Verbindungs-/Stabilitätsprobleme** und **viele
gerätespezifische Bugs**. Die tatsächliche Zuverlässigkeit hängt stark vom
konkreten Modell/Firmware ab.

Zahlen (Stand 2026-06-23):
- `homeconnect_local_hass`: **86 offene Issues**, davon **41 als Bug gelabelt**
  (14 Feature requests, Rest Übersetzungen/Fragen).
- `homeconnect_websocket`: nur **4 offene Issues** – deutlich ruhiger/gepflegter.

### 3.1 Verbindungsstabilität (wichtigstes Thema)

- **`local_hass` #403 – „Losing connection to devices; Loop Exceptions"**:
  Gerät läuft wochenlang stabil und fällt dann auf `unavailable`. Im Log eine
  Reconnect-Schleife mit `WSServerHandshakeError: 404 … url='ws://…/homeconnect'`.
  WLAN ist nachweislich ok → Problem liegt im WebSocket-Handshake/Reconnect,
  nicht im Netz. Offen, ohne Fix.
- **`local_hass` #339** – sehr langer HA-Start.
- **`local_hass` #410 / #409** – Config-Flow bricht mit „Unknown error" ab
  bzw. IPv6-Hosts werden falsch behandelt (Verbindungsaufbau scheitert).

### 3.2 AES/HMAC-Ebene (Kern-Library)

- **`homeconnect_websocket` #62 – „Receive loop Exception / HMAC Failure"**:
  **4124 Vorkommen** in kurzer Zeit. Die anwendungsseitige AES-Verschlüsselung
  scheitert an der HMAC-Prüfung und reißt die Empfangsschleife ab. Genau die
  HMAC-Integritätsprüfung aus Abschnitt 2.1 ist hier eine reale Fehlerquelle.
  Offen seit März 2026.
- **#70** – Parser stürzt bei großgeschriebenen Werten (`SELECTANDSTART`) ab
  (teilweise adressiert: Commit „Normalize execution value to lowercase").
- **#68** – Bosch FridgeFreezer erwartet Integer-Payload, obwohl als Float
  beschrieben → Datentyp-Mismatch.

### 3.3 Gerätespezifische Bugs (größter Block)

Viele Issues sind **modellabhängige Parser-/Feature-Fehler**, weil die
`DeviceDescription.xml` je Gerät stark variiert:

- Crashes beim Setup einzelner Geräte: #407 (Oven water tank), #395
  (Oven-Status-Regex zu gierig), #385 (RemainingProgramTime), #368
  (Group-ID-Format Siemens-Ofen).
- Kommandos schlagen fehl: #371 (CoffeeMaker Start → 400), #322 (Hood
  Programmwahl → Fehler), #386 (`fan.turn_off` → 500 bei Hauben), #400/#384
  (fehlender Start-Button / Delayed-Start beim Trockner).
- Trifft **nicht alle Nutzer**: gut unterstützte Geräte laufen oft stabil, bei
  „exotischeren" Modellen häufen sich die Probleme.

### 3.4 Wartungsstatus

- **~10-monatige stille Phase** (letzte Beta `1.0.5b10` im **Aug 2025**), die
  zur Meta-Frage **#390 „… is this abandoned?"** führte. O-Ton eines
  Contributors: *„The integration works well in general, but some appliances
  may have issues due to incomplete support … the foundation is solid."*
- **Aktivität im Juni 2026 zurückgekehrt**: neue Beta `1.0.5b11` (18.06.2026),
  PR-Merges am 15.06.2026. Es gibt aber einen Rückstau funktionierender PRs,
  die Nutzer aktuell teils selbst cherry-picken (z. B. PR #332).
- Kern-Library `homeconnect_websocket` ist gepflegter (zuletzt `1.5.3`, März
  2026, nur 4 offene Issues).

### 3.5 Bottom line

Für gängige Geräte und stabiles WLAN **brauchbar und lokal ohne Cloud**, aber
**kein „set-and-forget"-Niveau**: mit gelegentlichen Verbindungsabbrüchen/
Reconnect-Schleifen (#403, #62) und modellspezifischen Macken rechnen.

---

## 4. Implikationen für `go-homeconnect2mqtt`

Konkrete Konsequenzen aus der Recherche für eine eigene Go-Implementierung:

1. **Profil-Import statt Cloud-Live-Zugriff.** Schlüssel und Gerätebeschreibung
   aus dem „Profile Downloader"-Archiv (`.json` + zwei XML-Dateien) einlesen.
   Kein OAuth/Cloud-Flow im Normalbetrieb nötig.
2. **Beide Sicherheitsmodi abbilden:**
   - TLS-PSK (`wss://<ip>:443`) – in Go via `crypto/tls` mit PSK-Cipher-Suites
     (ggf. Custom-Handshake, da PSK in der Std-Lib eingeschränkt ist).
   - AES-CBC + HMAC auf Anwendungsebene über `ws://<ip>:80` – Key/IV aus dem
     PSK ableiten, HMAC-Kette pro Richtung führen.
3. **Robustes Reconnect-Verhalten als Kernanforderung.** Die häufigsten
   Praxis-Issues (#403, #62) sind Verbindungsabbrüche und HMAC-Desync. Daher:
   sauberer Reconnect mit Backoff, HMAC-/Session-State bei Neuverbindung
   vollständig zurücksetzen, 404-Handshake-Fälle gezielt behandeln.
4. **Toleranter XML-Parser.** Die `DeviceDescription.xml` variiert stark
   zwischen Modellen; viele Crashes entstehen durch zu strenge Annahmen
   (Regex, Group-ID-Format, Datentypen Float vs. Int, Groß-/Kleinschreibung von
   Enum-Werten). Defensiv parsen, Unbekanntes überspringen statt abstürzen.
5. **IPv6-/Host-Handling von Anfang an korrekt** (vgl. #409): URL-Bau mit
   Klammern + Zone-ID sauber kodieren.
6. **MQTT-Mapping:** Das Punktnotations-Feature-Modell
   (`BSH.Common.Setting.PowerState`) bildet sich natürlich auf MQTT-Topics ab
   (z. B. `homeconnect/<gerät>/BSH/Common/Setting/PowerState`). Read = State-
   Topic, Write = Command-Topic.

---

## 5. Quellen

- Integration: <https://github.com/chris-mc1/homeconnect_local_hass>
- Kern-Library: <https://github.com/chris-mc1/homeconnect_websocket>
- Referenzierte Issues: local_hass #403, #410, #409, #407, #400, #395, #390,
  #386, #385, #384, #371, #368, #339, #322; websocket #70, #68, #62
- Profil-Beschaffung: „Home Connect Profile Downloader" (Zielformat openHAB)
