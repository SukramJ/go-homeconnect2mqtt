// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

// EntryKind classifies a description entry by its source element.
type EntryKind string

// EntryKind values.
const (
	KindStatus          EntryKind = "status"
	KindSetting         EntryKind = "setting"
	KindEvent           EntryKind = "event"
	KindCommand         EntryKind = "command"
	KindOption          EntryKind = "option"
	KindProgram         EntryKind = "program"
	KindActiveProgram   EntryKind = "activeProgram"
	KindSelectedProgram EntryKind = "selectedProgram"
	KindProtectionPort  EntryKind = "protectionPort"
)

// ProgramOption is a program's embedded option reference.
type ProgramOption struct {
	RefUID     int
	Access     string
	Available  bool
	LiveUpdate bool
	Default    bool
}

// Entry is one parsed feature of an appliance, joining the
// DeviceDescription structure with the FeatureMapping clear name.
type Entry struct {
	UID          int
	Name         string // from FeatureMapping; "" if unmapped
	Kind         EntryKind
	RefCID       int
	RefDID       int
	ContentType  string
	ProtocolType ProtocolType
	Access       string // lowercase: read/readwrite/writeonly/readstatic/none
	Execution    string // lowercase, programs only
	Handling     string // events
	Level        string // events
	Available    bool
	HasMin       bool
	Min          float64
	HasMax       bool
	Max          float64
	HasStep      bool
	StepSize     float64
	InitValue    string
	Default      string
	Enumeration  map[int]string  // resolved enum value->name, nil if not an enum
	Options      []ProgramOption // program entries only
}

// IsEnum reports whether the entry carries an enumeration.
func (e *Entry) IsEnum() bool { return len(e.Enumeration) > 0 }

// Writable reports whether the entry's static access allows writing.
func (e *Entry) Writable() bool {
	return e.Access == "readwrite" || e.Access == "writeonly"
}

// DeviceInfo is the descriptive header of a DeviceDescription.
type DeviceInfo struct {
	Type     string
	Brand    string
	Model    string
	Version  int
	Revision int
}

// Description is the fully parsed appliance model: device metadata plus
// every entry, indexed by uid and by name for fast lookup.
type Description struct {
	Info         DeviceInfo
	Entries      []*Entry
	byUID        map[int]*Entry
	byName       map[string]*Entry
	Enumerations map[int]map[int]string // enid -> value -> name
}

// newDescription initialises the lookup maps.
func newDescription() *Description {
	return &Description{
		byUID:        map[int]*Entry{},
		byName:       map[string]*Entry{},
		Enumerations: map[int]map[int]string{},
	}
}

// add registers an entry in both indexes.
func (d *Description) add(e *Entry) {
	d.Entries = append(d.Entries, e)
	d.byUID[e.UID] = e
	if e.Name != "" {
		d.byName[e.Name] = e
	}
}

// rebuildIndex repopulates the uid/name lookup maps, used after loading a
// description from a JSON cache (where the unexported maps are nil).
func (d *Description) rebuildIndex() {
	d.byUID = make(map[int]*Entry, len(d.Entries))
	d.byName = make(map[string]*Entry, len(d.Entries))
	for _, e := range d.Entries {
		d.byUID[e.UID] = e
		if e.Name != "" {
			d.byName[e.Name] = e
		}
	}
}

// ByUID returns the entry for a uid.
func (d *Description) ByUID(uid int) (*Entry, bool) { e, ok := d.byUID[uid]; return e, ok }

// ByName returns the entry for a feature name.
func (d *Description) ByName(name string) (*Entry, bool) { e, ok := d.byName[name]; return e, ok }
