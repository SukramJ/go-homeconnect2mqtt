// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"encoding/xml"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// ---- FeatureMapping XML ----

type xmlFeatureMapping struct {
	Features  []xmlNamedRef `xml:"featureDescription>feature"`
	Errors    []xmlNamedRef `xml:"errorDescription>error"`
	EnumDescs []xmlEnumDesc `xml:"enumDescriptionList>enumDescription"`
}

type xmlNamedRef struct {
	RefUID string `xml:"refUID,attr"`
	RefEID string `xml:"refEID,attr"`
	Name   string `xml:",chardata"`
}

type xmlEnumDesc struct {
	RefENID string          `xml:"refENID,attr"`
	Members []xmlEnumMember `xml:"enumMember"`
}

type xmlEnumMember struct {
	RefValue string `xml:"refValue,attr"`
	Name     string `xml:",chardata"`
}

// featureMapping is the parsed FeatureMapping: clear names and enum value
// maps keyed by their (hex-decoded) ids.
type featureMapping struct {
	feature map[int]string
	errors  map[int]string
	enum    map[int]map[int]string
}

// parseFeatureMapping parses FeatureMapping.xml into the three maps
// (docs/02-datenmodell.md §3). Entries with an unparseable id are skipped.
func parseFeatureMapping(data []byte, logger *slog.Logger) (*featureMapping, error) {
	var doc xmlFeatureMapping
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("profile: parse FeatureMapping: %w", err)
	}
	fm := &featureMapping{
		feature: map[int]string{},
		errors:  map[int]string{},
		enum:    map[int]map[int]string{},
	}
	for _, f := range doc.Features {
		uid, err := parseHex(f.RefUID)
		if err != nil {
			logger.Debug("profile.feature_skip", slog.String("refUID", f.RefUID))
			continue
		}
		fm.feature[uid] = strings.TrimSpace(f.Name)
	}
	for _, e := range doc.Errors {
		eid, err := parseHex(e.RefEID)
		if err != nil {
			continue
		}
		fm.errors[eid] = strings.TrimSpace(e.Name)
	}
	for _, ed := range doc.EnumDescs {
		enid, err := parseHex(ed.RefENID)
		if err != nil {
			continue
		}
		values := map[int]string{}
		for _, m := range ed.Members {
			v, err := strconv.Atoi(strings.TrimSpace(m.RefValue))
			if err != nil {
				continue
			}
			values[v] = strings.TrimSpace(m.Name)
		}
		fm.enum[enid] = values
	}
	return fm, nil
}

// ---- DeviceDescription XML ----

type xmlElement struct {
	UID             string             `xml:"uid,attr"`
	RefCID          string             `xml:"refCID,attr"`
	RefDID          string             `xml:"refDID,attr"`
	EnumerationType string             `xml:"enumerationType,attr"`
	Access          string             `xml:"access,attr"`
	Available       string             `xml:"available,attr"`
	Execution       string             `xml:"execution,attr"`
	Handling        string             `xml:"handling,attr"`
	Level           string             `xml:"level,attr"`
	Min             string             `xml:"min,attr"`
	Max             string             `xml:"max,attr"`
	StepSize        string             `xml:"stepSize,attr"`
	InitValue       string             `xml:"initValue,attr"`
	Default         string             `xml:"default,attr"`
	Options         []xmlProgramOption `xml:"option"`
}

type xmlProgramOption struct {
	RefUID     string `xml:"refUID,attr"`
	Access     string `xml:"access,attr"`
	Available  string `xml:"available,attr"`
	LiveUpdate string `xml:"liveUpdate,attr"`
	Default    string `xml:"default,attr"`
}

// Recursive list containers (a *List may nest another *List).
type xmlStatusList struct {
	Items []xmlElement    `xml:"status"`
	Sub   []xmlStatusList `xml:"statusList"`
}
type xmlSettingList struct {
	Items []xmlElement     `xml:"setting"`
	Sub   []xmlSettingList `xml:"settingList"`
}
type xmlEventList struct {
	Items []xmlElement   `xml:"event"`
	Sub   []xmlEventList `xml:"eventList"`
}
type xmlCommandList struct {
	Items []xmlElement     `xml:"command"`
	Sub   []xmlCommandList `xml:"commandList"`
}
type xmlOptionList struct {
	Items []xmlElement    `xml:"option"`
	Sub   []xmlOptionList `xml:"optionList"`
}
type xmlProgramGroup struct {
	Programs []xmlProgram      `xml:"program"`
	Sub      []xmlProgramGroup `xml:"programGroup"`
}
type xmlProgram struct {
	UID       string             `xml:"uid,attr"`
	Available string             `xml:"available,attr"`
	Execution string             `xml:"execution,attr"`
	Access    string             `xml:"access,attr"`
	Options   []xmlProgramOption `xml:"option"`
}

type xmlEnumType struct {
	ENID        string             `xml:"enid,attr"`
	SubsetOf    string             `xml:"subsetOf,attr"`
	Enumeration []xmlEnumTypeValue `xml:"enumeration"`
}
type xmlEnumTypeValue struct {
	Value string `xml:"value,attr"`
}

type xmlDevice struct {
	XMLName     xml.Name          `xml:"device"`
	Description xmlDescription    `xml:"description"`
	StatusList  []xmlStatusList   `xml:"statusList"`
	SettingList []xmlSettingList  `xml:"settingList"`
	EventList   []xmlEventList    `xml:"eventList"`
	CommandList []xmlCommandList  `xml:"commandList"`
	OptionList  []xmlOptionList   `xml:"optionList"`
	Programs    []xmlProgramGroup `xml:"programGroup"`
	Active      xmlElement        `xml:"activeProgram"`
	Selected    xmlElement        `xml:"selectedProgram"`
	Protection  xmlElement        `xml:"protectionPort"`
	EnumTypes   []xmlEnumType     `xml:"enumerationTypeList>enumerationType"`
}

type xmlDescription struct {
	Type     string `xml:"type"`
	Brand    string `xml:"brand"`
	Model    string `xml:"model"`
	Version  string `xml:"version"`
	Revision string `xml:"revision"`
}

// flatten helpers walk the recursive list containers.
func flattenStatus(lists []xmlStatusList) []xmlElement {
	var out []xmlElement
	for _, l := range lists {
		out = append(out, l.Items...)
		out = append(out, flattenStatus(l.Sub)...)
	}
	return out
}

func flattenSetting(lists []xmlSettingList) []xmlElement {
	var out []xmlElement
	for _, l := range lists {
		out = append(out, l.Items...)
		out = append(out, flattenSetting(l.Sub)...)
	}
	return out
}

func flattenEvent(lists []xmlEventList) []xmlElement {
	var out []xmlElement
	for _, l := range lists {
		out = append(out, l.Items...)
		out = append(out, flattenEvent(l.Sub)...)
	}
	return out
}

func flattenCommand(lists []xmlCommandList) []xmlElement {
	var out []xmlElement
	for _, l := range lists {
		out = append(out, l.Items...)
		out = append(out, flattenCommand(l.Sub)...)
	}
	return out
}

func flattenOption(lists []xmlOptionList) []xmlElement {
	var out []xmlElement
	for _, l := range lists {
		out = append(out, l.Items...)
		out = append(out, flattenOption(l.Sub)...)
	}
	return out
}

func flattenProgram(groups []xmlProgramGroup) []xmlProgram {
	var out []xmlProgram
	for _, g := range groups {
		out = append(out, g.Programs...)
		out = append(out, flattenProgram(g.Sub)...)
	}
	return out
}

// ParseDescription parses both XML files into a Description. Parsing is
// tolerant: an element with an unparseable uid is skipped with a log line
// rather than failing the whole device (FK-3, docs/05-resilienz.md).
func ParseDescription(descriptionXML, featureMappingXML []byte, logger *slog.Logger) (*Description, error) {
	if logger == nil {
		logger = slog.Default()
	}
	fm, err := parseFeatureMapping(featureMappingXML, logger)
	if err != nil {
		return nil, err
	}
	var dev xmlDevice
	if err := xml.Unmarshal(descriptionXML, &dev); err != nil {
		return nil, fmt.Errorf("profile: parse DeviceDescription: %w", err)
	}

	d := newDescription()
	d.Info = DeviceInfo{
		Type:     dev.Description.Type,
		Brand:    dev.Description.Brand,
		Model:    dev.Description.Model,
		Version:  atoiSafe(dev.Description.Version),
		Revision: atoiSafe(dev.Description.Revision),
	}

	// Resolve enum subsets declared in the DeviceDescription against the
	// FeatureMapping enums (docs/02 §5 step 2).
	resolveEnumSubsets(dev.EnumTypes, fm, logger)
	d.Enumerations = fm.enum

	addElems := func(elems []xmlElement, kind EntryKind) {
		for i := range elems {
			x := &elems[i]
			e, err := convertElement(x, kind, fm)
			if err != nil {
				logger.Debug("profile.element_skip", slog.String("kind", string(kind)),
					slog.String("uid", x.UID), slog.String("err", err.Error()))
				continue
			}
			d.add(e)
		}
	}
	addElems(flattenStatus(dev.StatusList), KindStatus)
	addElems(flattenSetting(dev.SettingList), KindSetting)
	addElems(flattenEvent(dev.EventList), KindEvent)
	addElems(flattenCommand(dev.CommandList), KindCommand)
	addElems(flattenOption(dev.OptionList), KindOption)

	programs := flattenProgram(dev.Programs)
	for i := range programs {
		p := &programs[i]
		e, err := convertProgram(p, fm)
		if err != nil {
			logger.Debug("profile.program_skip", slog.String("uid", p.UID), slog.String("err", err.Error()))
			continue
		}
		d.add(e)
	}

	singles := []struct {
		x    xmlElement
		kind EntryKind
	}{
		{dev.Active, KindActiveProgram},
		{dev.Selected, KindSelectedProgram},
		{dev.Protection, KindProtectionPort},
	}
	for i := range singles {
		single := &singles[i]
		if single.x.UID == "" {
			continue
		}
		if e, err := convertElement(&single.x, single.kind, fm); err == nil {
			d.add(e)
		} else {
			logger.Debug("profile.single_skip", slog.String("kind", string(single.kind)), slog.String("err", err.Error()))
		}
	}
	return d, nil
}

func resolveEnumSubsets(types []xmlEnumType, fm *featureMapping, logger *slog.Logger) {
	for _, et := range types {
		if et.SubsetOf == "" {
			continue
		}
		enid, err := parseHex(et.ENID)
		if err != nil {
			continue
		}
		parent, err := parseHex(et.SubsetOf)
		if err != nil {
			continue
		}
		parentMap, ok := fm.enum[parent]
		if !ok {
			logger.Debug("profile.enum_subset_missing_parent", slog.String("enid", et.ENID))
			continue
		}
		subset := map[int]string{}
		for _, v := range et.Enumeration {
			iv, err := strconv.Atoi(strings.TrimSpace(v.Value))
			if err != nil {
				continue
			}
			if name, ok := parentMap[iv]; ok {
				subset[iv] = name
			}
		}
		fm.enum[enid] = subset
	}
}

// convertElement turns an XML element into an *Entry, joining the
// FeatureMapping name and the refCID type tables.
func convertElement(x *xmlElement, kind EntryKind, fm *featureMapping) (*Entry, error) {
	uid, err := parseHex(x.UID)
	if err != nil {
		return nil, fmt.Errorf("bad uid %q: %w", x.UID, err)
	}
	e := &Entry{
		UID:       uid,
		Name:      fm.feature[uid],
		Kind:      kind,
		Access:    strings.ToLower(x.Access),
		Execution: strings.ToLower(x.Execution),
		Handling:  strings.ToLower(x.Handling),
		Level:     strings.ToLower(x.Level),
		Available: parseBoolDefault(x.Available, true),
		InitValue: x.InitValue,
		Default:   x.Default,
	}
	if refCID, err := parseHex(x.RefCID); err == nil {
		e.RefCID = refCID
		e.ContentType = ContentTypeFor(refCID)
		e.ProtocolType = ProtocolTypeFor(refCID)
	} else {
		e.ProtocolType = ProtocolString
	}
	if refDID, err := parseHex(x.RefDID); err == nil {
		e.RefDID = refDID
	}
	if x.EnumerationType != "" {
		if enid, err := parseHex(x.EnumerationType); err == nil {
			if em, ok := fm.enum[enid]; ok {
				e.Enumeration = em
			}
		}
	}
	if v, ok := parseFloat(x.Min); ok {
		e.HasMin, e.Min = true, v
	}
	if v, ok := parseFloat(x.Max); ok {
		e.HasMax, e.Max = true, v
	}
	if v, ok := parseFloat(x.StepSize); ok {
		e.HasStep, e.StepSize = true, v
	}
	return e, nil
}

func convertProgram(p *xmlProgram, fm *featureMapping) (*Entry, error) {
	uid, err := parseHex(p.UID)
	if err != nil {
		return nil, fmt.Errorf("bad program uid %q: %w", p.UID, err)
	}
	e := &Entry{
		UID:       uid,
		Name:      fm.feature[uid],
		Kind:      KindProgram,
		Access:    strings.ToLower(p.Access),
		Execution: strings.ToLower(p.Execution),
		Available: parseBoolDefault(p.Available, true),
	}
	for _, o := range p.Options {
		refUID, err := parseHex(o.RefUID)
		if err != nil {
			continue
		}
		e.Options = append(e.Options, ProgramOption{
			RefUID:     refUID,
			Access:     strings.ToLower(o.Access),
			Available:  parseBoolDefault(o.Available, true),
			LiveUpdate: parseBoolDefault(o.LiveUpdate, false),
			Default:    parseBoolDefault(o.Default, false),
		})
	}
	return e, nil
}

// ---- small parse helpers ----

func parseHex(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty hex")
	}
	v, err := strconv.ParseInt(s, 16, 64)
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseBoolDefault parses "true"/"false" (case-insensitive) or a number,
// falling back to def when the attribute is absent/unparseable.
func parseBoolDefault(s string, def bool) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return def
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n != 0
	}
	return def
}

func atoiSafe(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}
