// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package profile imports a Home Connect profile (the DeviceDescription
// and FeatureMapping XML plus the per-device JSON index produced by the
// Home Connect Profile Downloader) into a tolerant device-description
// object model. mirrors description_parser.py + helpers.py.
package profile

// ProtocolType is the coarse wire type that drives value casting
// (docs/02-datenmodell.md §4.1).
type ProtocolType string

// ProtocolType values.
const (
	ProtocolBoolean ProtocolType = "Boolean"
	ProtocolInteger ProtocolType = "Integer"
	ProtocolFloat   ProtocolType = "Float"
	ProtocolString  ProtocolType = "String"
	ProtocolObject  ProtocolType = "Object"
)

// protocolTypes maps refCID -> ProtocolType (docs/02 §4.1). Unknown codes
// default to String via ProtocolTypeFor.
var protocolTypes = buildProtocolTypes()

// contentTypes maps refCID -> fine content type used for device_class and
// unit hints (docs/02 §4.2). Only the named codes are populated; gaps
// resolve to "".
var contentTypes = buildContentTypes()

// ProtocolTypeFor returns the wire type for a refCID, defaulting to String.
func ProtocolTypeFor(refCID int) ProtocolType {
	if pt, ok := protocolTypes[refCID]; ok {
		return pt
	}
	return ProtocolString
}

// ContentTypeFor returns the fine content type for a refCID, or "".
func ContentTypeFor(refCID int) string {
	return contentTypes[refCID]
}

func buildProtocolTypes() map[int]ProtocolType {
	m := map[int]ProtocolType{
		1: ProtocolBoolean, 2: ProtocolInteger, 3: ProtocolInteger, 4: ProtocolFloat,
		5: ProtocolString, 6: ProtocolString, 7: ProtocolFloat, 8: ProtocolFloat,
		10: ProtocolString, 16: ProtocolInteger, 17: ProtocolFloat, 18: ProtocolInteger,
		19: ProtocolInteger, 20: ProtocolInteger, 21: ProtocolInteger, 22: ProtocolString,
		23: ProtocolString, 24: ProtocolInteger, 30: ProtocolString, 31: ProtocolInteger,
		32: ProtocolInteger, 33: ProtocolFloat, 34: ProtocolFloat, 35: ProtocolFloat,
		36: ProtocolFloat, 37: ProtocolInteger, 38: ProtocolInteger, 39: ProtocolFloat,
		40: ProtocolObject, 41: ProtocolFloat, 42: ProtocolObject, 47: ProtocolInteger,
		48: ProtocolString, 49: ProtocolObject, 50: ProtocolString, 58: ProtocolInteger,
		59: ProtocolInteger, 61: ProtocolString, 64: ProtocolFloat, 65: ProtocolFloat,
	}
	for c := 25; c <= 27; c++ {
		m[c] = ProtocolObject
	}
	for c := 43; c <= 46; c++ {
		m[c] = ProtocolFloat
	}
	for c := 51; c <= 57; c++ {
		m[c] = ProtocolFloat
	}
	for c := 62; c <= 63; c++ {
		m[c] = ProtocolObject
	}
	// All *List / complex types are objects.
	for c := 129; c <= 194; c++ {
		m[c] = ProtocolObject
	}
	return m
}

func buildContentTypes() map[int]string {
	m := map[int]string{
		1: "boolean", 2: "integer", 3: "enumeration", 4: "float", 5: "string",
		6: "dateTime", 7: "temperatureCelsius", 8: "temperatureFahrenheit", 10: "hexBinary",
		16: "timeSpan", 17: "percent", 18: "dbm", 19: "weight", 20: "liquidVolume",
		21: "uidValue", 22: "date", 23: "time", 24: "waterHardness", 25: "point2D",
		26: "pose2D", 27: "line2D", 30: "rgb", 31: "rpm", 32: "flowRate", 33: "length",
		34: "area", 35: "power", 36: "energy", 37: "bigInteger", 38: "identifier",
		39: "speed", 40: "programInstruction", 41: "weightPound", 42: "localeString",
		43: "teaspoon", 46: "piece", 47: "byteLength", 48: "uuid", 49: "timezone",
		50: "csv", 51: "leaf", 52: "bunch", 59: "portion", 61: "utcDateTime",
		62: "programRunSummary", 63: "programSessionSummary", 64: "liquidVolumeThroughput",
		65: "weightOunces",
	}
	return m
}
