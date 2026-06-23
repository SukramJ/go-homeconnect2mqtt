# Home Connect — Resilienz: Fehlerklassen, Issues & Gegenmaßnahmen

> Die lokale Home-Connect-Anbindung ist **keine offiziell dokumentierte API**. Reale
> Praxisprobleme stammen aus den GitHub-Issues von `homeconnect_local_hass` (86 offen,
> davon 41 Bug-Label) und `homeconnect_websocket` (8 Issues). Diese Datei kondensiert die
> relevanten Issues zu **Fehlerklassen mit konkreten Go-Gegenmaßnahmen** — Resilienz ist
> die Kernanforderung von `go-homeconnect2mqtt`.

Stand: 2026-06-23. Wartungsstatus: Integration nach ~10 Monaten Stille seit Juni 2026
wieder aktiv (Beta `1.0.5b11`), aber großer Rückstau; viele Fixes existieren nur als
unmerged Community-PRs. Kern-Library gepflegter (`1.5.3`, März 2026).

---

## 1. Die 8 Fehlerklassen (Priorität für Go)

### FK-1 — Verbindungsstabilität / Reconnect ★★★ (häufigste Klasse)

**Symptome:** Gerät läuft wochenlang stabil, fällt dann auf `unavailable`; Reconnect-Schleife
mit `WSServerHandshakeError: 404 ... ws://…/homeconnect` erholt sich nicht; schlafende Geräte
verursachen Endlosfehler.

**Issues:** local_hass #403 (404-Loop, offen), #293 (Off-Gerät → „Failed setup, will retry"-Loop),
#339 (Off-Gerät blockiert HA-Start ~5 min), #287/#44/#42 (Flapping/Disconnect bei Schlaf);
websocket #41 (dauer-offline → 1000e Fehler, **kein Backoff** im Code).

**Go-Gegenmaßnahmen:**
- Reconnect mit **exponentiellem Backoff + Jitter** (z. B. 1 s → 30 s, ±500 ms). Python hat keins.
- **Offline = Normalzustand**, kein Fehler: Gerät schläft/aus → MQTT-`availability=offline` + LWT,
  weiter im Backoff pollen, **nie** das Tool/den Worker crashen.
- **Connect-Timeout** beim Start (Off-Gerät darf den Tool-Start nicht blockieren — vgl. #339).
- **Log-Rate-Limiting** für wiederkehrende Connect-Fehler.
- 404-Handshake gezielt behandeln: voller Reconnect (frischer Socket/State), nicht Retry auf totem Socket.
- Pro Gerät ein eigener, isolierter Worker (ein abstürzendes Gerät darf andere nicht beeinflussen).

### FK-2 — Krypto / HMAC-Desync ★★★

**Symptome:** Flut von `HMAC Failure` (real: 4124 Stück), Empfangsschleife reißt ab;
`Message not of Type binary`; Handshake-`500`/`404`.

**Issues:** websocket #62 (HMAC, offen); local_hass #255/#128/#177 (`500 /ro/allMandatoryValues`),
#16/#158 (not binary / Timeout), #297 (TLS-Auth fehlgeschlagen).

**Go-Gegenmaßnahmen (Details in `01-protokoll.md` §3.4):**
- HMAC constant-time vergleichen.
- Bei **erster** HMAC-/Padding-/Decode-Failure → **sofort voll reconnecten**, nicht weiterlesen.
- TX/RX-Krypto-State je mit Mutex serialisieren; CBC als echten Stream führen.
- `500` im Handshake tolerieren + retrybar machen (kein harter Abbruch).

### FK-3 — Gerätespezifische Parser-/Feature-Bugs ★★★ (größter Block)

**Symptome:** Setup-Crash für einzelne Geräte; greedy Regex; `None.enum`; `JSONDecodeError`
beim `initValue`-Parsen; falsches Group-ID-Format.

**Issues:** local_hass #395/#385/#368/#277/#194/#145 (Oven-Cavity Regex `int('001.Remaining...')`),
#407 (`None.enum` Oven WaterTank), #210/#292/#95/#38 (Hob `JSONDecodeError` beim Setup).

**Go-Gegenmaßnahmen:**
- **Per-Entity-Isolation:** ein kaputtes Feature → skip + log, **niemals** das ganze Gerät verwerfen.
- Group-IDs strikt mit `\d+`/`[^.]+` matchen; nicht-numerische ignorieren.
- Überall None-/Existenz-Guards (Enum, optionale Felder).
- Toleranter XML-Parser (siehe `02-datenmodell.md` §2).

### FK-4 — Programmstart ist gerätespezifisch ★★★

**Symptome:** Blindes `POST /ro/activeProgram` scheitert mit `400`/`501`/`541`.

**Issues:** local_hass #322 (Hood/Dishwasher/Washer Start `400`), #371 (Coffee `400`),
#400 (Dryer: kein Start-Entity), #201 (Washer: kein Start seit b10), #386 (Hood-Fan-Off → `500`),
#385-Kommentar (Hob: direkter `selectedProgram`-Write nötig).

**Go-Gegenmaßnahmen — drei Startpfade unterstützen:**
1. **Standard:** `POST /ro/activeProgram {program, options}`.
2. **Kochfeld (Hob):** direkter `POST /ro/selectedProgram` (weil `selectedProgram.validate=false`;
   Standard-Start crasht mit `NoneType.start`, wenn nichts vorgewählt ist).
3. **Command-basiert:** `BSH.Common.Command.StartProgram` (sofern vorhanden).
4. Hauben-Fan-Off: **DELETE** `/ro/activeProgram` statt Wert 0.

### FK-5 — Dynamischer `access` (Schreibfenster) ★★

**Symptome:** Schreiben → `541 ProcessStateNotCompliant` / `400`, weil das Feld gerade read-only ist.

**Issue:** local_hass #384 (Dryer): `BSH.Common.Root.ActiveProgram` (uid 256) wechselt
**~alle 30 s** zwischen READWRITE und READ. Schreiben nur im READWRITE-Fenster akzeptiert.

**Go-Gegenmaßnahmen:**
- Auf `NOTIFY /ro/descriptionChange` reagieren (access-Updates verarbeiten).
- Writes **gaten**: nur senden, wenn `access ∈ {readwrite, writeonly}`; sonst auf das Fenster
  warten / mit Backoff retrien.
- Korrekte Delayed-Start-Message (Beispiel #384): `POST /ro/values [{551:delay},{256:programUID}]`.

### FK-6 — Datentyp-/Enum-Mismatch ★★

**Issues:** websocket #68 (Float-Setting will Integer), #70 (`SELECTANDSTART` uppercase),
#56 (Enum-Wert außerhalb der Enumeration), #66 (unbekannte Programm-UID).

**Go-Gegenmaßnahmen (siehe `02-datenmodell.md` §7):**
- Float mit ganzzahligem Wert (stepSize==1) als **Integer** schreiben.
- Alle String-Enums **case-insensitiv**.
- Enum-Miss → Rohwert; unbekannte UID → None/ignorieren.

### FK-7 — Config-Flow / IPv6 / Onboarding ★

**Issues:** local_hass #410 (Library-Exceptions nicht gefangen → „Unknown error"),
#409 (IPv6 Doppel-Klammern + Zone-ID), #268 (all already setup), #297 (kein IP-Fallback bei TLS).

**Go-Gegenmaßnahmen (siehe `03-profil-format.md`):**
- Eigene, kategorisierte Fehlertypen + **immer** manuelle-IP-Eskalation.
- IPv6: vorhandene Klammern strippen, dann wrappen; Zone-IDs gesondert.

### FK-8 — Lokalisierung / Optionsabdeckung (niedrige Severity)

Programmnamen fallen auf Roh-IDs zurück (#298/#283/#15); viele Optionen fehlen in der
HA-Integration (EcoDry, CrystalDry, HalfLoad, iDos, Spin/Temp …). Für ein **generisches**
MQTT-Tool weniger kritisch, da idealerweise **alle** Features exponiert werden (siehe
`04-geraete-mapping.md`).

---

## 2. Gerätespezifische Issues — deine drei Geräte

### Geschirrspüler (Dishwasher)

| Issue | Problem | Konsequenz fürs Go-Tool |
|---|---|---|
| #322 | Siemens: Programmstart `400 /ro/activeProgram` (intermittierend) | Start-Pfade FK-4; retrybar |
| #373 | Bosch SMV4ECX30E: Delayed Start nur read-only Sensor | `StartInRelative` als steuerbare Number anbieten |
| #322-Kmt | `select…start_in` → `TypeError: NoneType not subscriptable` | `value["start"]` None-safe |
| #297 | Bosch SMV6ZCX01G (TLS): „Authentication failed", kein IP-Fallback | FK-7; TLS-PSK + IP-Fallback |
| #351/#263/#205/#49 | EcoDry/ExtraDry/CrystalDry/HalfLoad-Optionen fehlen | generisch alle Options exponieren |
| #44/#42 | Flapping wenn aus / Start-Button hängt nach Reconnect | FK-1 |
| — | Delayed Start: Dishwasher nutzt `BSH.Common.Option.StartInRelative` | (im Gegensatz zu Washer, s. u.) |

### Induktionskochfeld (Hob/Cooktop) — fragilster Setup-Typ

| Issue | Problem | Konsequenz |
|---|---|---|
| #292/#204/#210/#95/#38 | Setup-Crash `JSONDecodeError` beim `initValue`-Parsen | FK-3: per-Entity-Isolation, toleranter Parser |
| #128 | Bosch PIF695HC1E: `500 /ro/allMandatoryValues` | FK-2: Handshake-500 tolerieren |
| #385-Kmt (Bosch PXX645HC1M) | Zonen-Status fehlt; `residuelheat` (Tippfehler) als State; `Access.NONE`-Commands; **Start = direkter `selectedProgram`-POST** (`validate=false`) | FK-4 Pfad 2; Rohwerte tolerieren |
| #111/#261 | Programmstart braucht On-Device-Bestätigung | als erwartetes Verhalten behandeln |

> Kochfeld-Zonen werden dynamisch erkannt: `Cooking.Hob.Status.Zone.<n>.{State,OperationState,PowerLevel,FryingSensorLevel,CurrentTemperature,HeatupProgress,Duration,...}` — siehe `04-geraete-mapping.md`.

### Waschmaschine / Waschtrockner (Washer/WasherDryer/Dryer)

| Issue | Problem | Konsequenz |
|---|---|---|
| #201 | Bosch Serie 8 WD: kein Programmstart seit b10 (Start unavailable, Resume „not writeable") | FK-4 / FK-5 |
| #179 | Bosch WDU28513: Spin Speed/Temperature nicht setzbar, Temp-Number fehlt | `LaundryCare.Washer.Option.{SpinSpeed,Temperature}` schreibbar machen |
| #196 | Serie 8 WD: Delayed Start „'Start in' not available" — **Washer nutzt `BSH.Common.Option.FinishInRelative` (uid 551)**, nicht `StartInRelative` | korrektes Feld je Gerätetyp |
| #384 | Siemens Dryer: `FinishInRelative` → `541`; **uid 256 dynamischer Access (~30 s)** | FK-5: Schreibfenster-Gating |
| #400 | Bosch Dryer WRB247D0FG: Start-Button fehlt (kein ActiveProgram-Entity) | Start-Pfade robust ableiten |
| #255 | Siemens Washer: `500 /ro/allMandatoryValues` beim Re-Setup | FK-2 |
| #293/#403 | Off-State / 404-Loop | FK-1 |
| #224 | Bosch Dryer: Duration-Sensor min/max falsch | Werte tolerant behandeln |

**Delayed-Start-Merksatz:** Geschirrspüler → `StartInRelative`; Waschmaschine/Trockner →
`FinishInRelative` (uid 551, oft read-only/`access=none` bis scharf). Beide unterstützen, None-safe.

---

## 3. Resilienz-Designprinzipien (Zusammenfassung)

1. **Isolation pro Gerät** — eigener Worker/Goroutine + Context; ein Gerät darf andere nie reißen.
2. **Isolation pro Entity** — defensives Parsen, skip+log statt Crash.
3. **Offline ist normal** — Backoff + Jitter, LWT/availability, kein Spam, kein Crash.
4. **Krypto-Desync = Voll-Reconnect** — niemals auf desyncter Kette weiterlesen.
5. **Schreiben ist bedingt** — access/available/Schreibfenster prüfen, gerätespezifische Startpfade.
6. **Tolerant lesen, strikt schreiben** — Rohwerte/unbekannte UIDs annehmen; beim Schreiben Typen/Enums normalisieren.
7. **Beobachtbarkeit** — strukturierte Logs (mit Redaction), pro-Gerät-Health (last-update-Alter),
   Connection-State über MQTT exponieren.
8. **Niemals Secrets** in Logs/Topics (psk, iv, serialNumber, mac, shipSki, deviceID).
