# go-homeconnect2mqtt — Wissensbasis & Konzept

Diese `docs/` enthalten die **vollständige, selbst-tragende** Grundlage für
`go-homeconnect2mqtt` (Home Connect ⇒ MQTT, lokal, ohne Cloud). Alle nötigen Informationen
wurden aus den Referenz-Repos extrahiert; **die Repos `../aaa_homeconnect_websocket` und
`../aaa_homeconnect_local_hass` werden nicht mehr benötigt.**

Das Projekt ist als **Schwesterprojekt von `../go-mtec2mqtt`** konzipiert: gleiche Struktur,
Konventionen und Resilienz-Philosophie.

## Dokumente

| Datei | Inhalt |
|---|---|
| [`homeconnect-recherche.md`](homeconnect-recherche.md) | Ausgangs-Recherche: Überblick, Zuverlässigkeit, Implikationen |
| [`01-protokoll.md`](01-protokoll.md) | **Wire-Protokoll**: Transport (AES/TLS-PSK), KDF, AES-CBC+HMAC-Kette, Handshake, Services, Error-Codes, Reconnect-State-Machine |
| [`02-datenmodell.md`](02-datenmodell.md) | **Datenmodell**: DeviceDescription/FeatureMapping-XML, Typ-Tabellen (refCID→Typ), Enum-Logik, Entity-/Wert-Semantik |
| [`03-profil-format.md`](03-profil-format.md) | **Onboarding**: Profil-Archiv (`.json`+2 XML), exakte JSON-Felder, Discovery, Fehlerklassen, Secrets |
| [`04-geraete-mapping.md`](04-geraete-mapping.md) | **Feature-Kataloge** (BSH.Common + Geschirrspüler/Kochfeld/Waschmaschine) + MQTT-/HA-Discovery-Mapping |
| [`05-resilienz.md`](05-resilienz.md) | **Resilienz**: 8 Fehlerklassen aus den GitHub-Issues + konkrete Go-Gegenmaßnahmen, gerätespezifisch |
| [`06-architektur-konzept.md`](06-architektur-konzept.md) | **Konzept**: Go-Paketlayout, Lifecycle, Config, Bridge, Build/Qualität, Teststrategie, Roadmap, optionale Web-UI (§10) |
| [`07-referenz-quellen.md`](07-referenz-quellen.md) | **Verbatim-Artefakte**: Krypto-Code-Referenz + Test-Fixtures (XML) |
| [`08-schwesterprojekt-vorlage.md`](08-schwesterprojekt-vorlage.md) | **Wiederverwendbare Dateien verbatim** aus `go-mtec2mqtt`: MQTT-Client, Config-Loader, Makefile, golangci, Dockerfile, version, githook (Unabhängigkeit vom Schwesterprojekt) |
| [`09-web-api.md`](09-web-api.md) | **HTTP-API- & SSE-Vertrag** der optionalen Web-UI: Endpunkt-Schemas, JSON-Payloads, Event-Format, Fehler-Taxonomie |
| [`10-implementation-plan.md`](10-implementation-plan.md) | **Trackable implementation plan** (English): 13 phases (P0–P12) with checklists, file mapping, test gates, dependencies, master tracker |

## Lesereihenfolge

- **Implementierungsstart:** 06 (Konzept) → 01 (Protokoll) → 02 (Datenmodell) → 07 (Fixtures/Tests).
- **Onboarding/Geräte:** 03 (Profil) → 04 (Mapping).
- **Querschnitt Resilienz:** 05 — in alle Bausteine eingewoben (Verweise FK-1…FK-8).

## Kernfakten in einem Absatz

Jedes Home-Connect-Gerät betreibt lokal einen WebSocket-Server unter `/homeconnect`.
Neue Geräte: **AES** auf `ws://host:80` mit App-Layer-Krypto (AES-256-CBC als fortlaufender
Stream + rollende HMAC-SHA256-Kette pro Richtung; Schlüssel aus `HMAC(psk,"ENC"/"MAC")`).
Ältere Geräte: **TLS-PSK** auf `wss://host:443`. Schlüssel/Beschreibung kommen einmalig aus
dem Profil-Downloader (`.json` mit `key`/`iv`/`connectionType` + zwei XML-Dateien). Nach dem
Handshake (`/ei/initialValues` → `/ci/services` → … → `/ro/allMandatoryValues`) kommen Werte
als `NOTIFY /ro/values`; Schreiben via `POST /ro/values`. **Resilienz ist die Kernanforderung**
(undokumentierte API): voller Reconnect bei HMAC-Desync, Backoff+Jitter, offline≠Fehler,
per-Entity-/per-Gerät-Isolation, gerätespezifische Programmstart-Pfade.
