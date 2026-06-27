// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package i18n localizes Home Connect enum member values for Home Assistant
// dropdowns. Because an MQTT-discovery entity cannot use HA's native enum
// translations (those ship with a native integration), the localized label is
// baked into the discovery `options`, the published state, and the reverse
// command mapping — the same approach as the sister projects.
//
// The catalogue covers the common cross-appliance member names (states, common
// settings); an unknown value falls back to the original (English) name, so a
// partially-translated enum stays consistent between options and state.
package i18n

import "strings"

// enumDE maps a Home Connect enum member name (lowercased) to its German label.
var enumDE = map[string]string{
	// PowerState / generic on-off
	"off": "Aus", "on": "Ein", "standby": "Standby",
	// DoorState
	"open": "Offen", "closed": "Geschlossen", "locked": "Verriegelt",
	// OperationState
	"inactive": "Inaktiv", "ready": "Bereit", "delayedstart": "Startvorwahl",
	"run": "Läuft", "pause": "Pausiert", "actionrequired": "Aktion erforderlich",
	"finished": "Beendet", "error": "Fehler", "aborting": "Wird abgebrochen",
	// Event values (when surfaced as enums)
	"present": "Vorhanden", "confirmed": "Bestätigt",
	// RemoteControlLevel
	"manualremotestart": "Manueller Fernstart", "permanentremotestart": "Dauerhafter Fernstart",
	"monitoring": "Überwachung",
	// Common dishwasher / washer settings
	"program": "Programm", "allprograms": "Alle Programme",
	"ecoasdefault": "Eco als Standard", "lastprogram": "Letztes Programm",
	"coldwater": "Kaltwasser", "hotwater": "Warmwasser",
	"sensitive": "Empfindlich", "standard": "Standard", "verysensitive": "Sehr empfindlich",
	"auto": "Automatisch", "manual": "Manuell", "low": "Niedrig", "medium": "Mittel", "high": "Hoch",
	// FavoriteHandling
	"asbuttons": "Als Tasten", "aslist": "Als Liste",
}

// enumEN reverses enumDE (lowercased German label -> lowercased English name).
var enumEN = func() map[string]string {
	m := make(map[string]string, len(enumDE))
	for en, de := range enumDE {
		m[strings.ToLower(de)] = en
	}
	return m
}()

// EnumLabel returns the localized label for an enum member name, or the name
// unchanged when the language is not localized or the value is uncatalogued.
func EnumLabel(name, lang string) string {
	if lang == "de" {
		if de, ok := enumDE[strings.ToLower(name)]; ok {
			return de
		}
	}
	return name
}

// EnumValue maps a (possibly localized) label back to the English enum member
// name for the write path, or returns the label unchanged when it is already
// English / uncatalogued. The result is matched case-insensitively against the
// device's enumeration downstream, so the returned casing is not significant.
func EnumValue(label, lang string) string {
	if lang == "de" {
		if en, ok := enumEN[strings.ToLower(label)]; ok {
			return en
		}
	}
	return label
}
