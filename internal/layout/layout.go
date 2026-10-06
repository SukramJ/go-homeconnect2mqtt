// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package layout is the single source of this daemon's MQTT topic layout.
//
// It exists because the layout used to be composed in six places across
// three packages, from two separate featurePath implementations and two
// separate spellings of the synthetic program-control path:
//
//	internal/hass/discovery.go    featurePath(e) + topicsFor()   -> state_topic, command_topic, availability_topic
//	internal/hass/discovery.go    publishProgramControls()       -> "_control/<key>/set", availability, inline
//	internal/bridge/publish.go    featurePath(name, uid) + deviceTopics
//	internal/bridge/command.go    the "/#" subscribe filter
//	internal/bridge/command.go    handleSet()/resolveEntity(), the INVERSE of featurePath
//	internal/bridge/command.go    controlStartProgram/controlStopProgram, the inverse of the second spelling
//	cmd/homeconnect2mqtt/main.go  the bridge status topic the Last Will writes
//
// The first two of those feed the retained discovery config — what Home
// Assistant is told to read and write — and the rest are where the daemon
// actually reads and writes. A divergence between any advertising site and
// its acting counterpart is silent in every direction: the entity points at
// a topic nobody writes (permanently `unknown`) or a button writes to a
// topic nobody handles (a press that does nothing), with nothing in the log
// and nothing in Home Assistant's registry to notice. See
// notes/adr0070-phase7-measurement.md, findings F1, F3 and F6.
//
// # The grammar (0.15.0, openccu-loom ADR 0083)
//
// The tree follows mqtt-smarthome 2.0, `<name>/<function>/<item...>`, built
// on go-hamqtt's topic.SmartHome:
//
//	<name>/connected                           0/1/2, the Last Will
//	<name>/status/<haId>/<Feature/Path>        a feature's value
//	<name>/set/<haId>/<Feature/Path>           a feature write, same item path
//	<name>/status/<haId>/online                the appliance's reachability
//	<name>/status/<haId>/connection_state      the appliance link state
//	<name>/set/<haId>/_control/<key>           a synthetic program control
//
// The device segment is the appliance's haId, a stable hardware identifier,
// rather than the operator's device name: the name used to go into the topic
// unsanitised, spaces, `/` and umlauts included, and moved every topic of an
// appliance when it was renamed. The name stays where Home Assistant shows
// it, and in the discovery identities, which must not move.
//
// Feature paths stay verbatim (Home Connect's own identifiers,
// `BSH/Common/Setting/PowerState`), every segment made topic-safe by
// topic.Safe. The discovery *config* topic, which slugifies the device name,
// is not part of this layout — it is identity-bearing and belongs to
// internal/hass (see F2).
//
// The layout the daemon published before 0.15.0 lives on in legacy.go, for
// the one job left to it: finding what the old release left retained.
package layout

import (
	"strconv"
	"strings"

	hatopic "github.com/SukramJ/go-hamqtt/topic"
)

// Synthetic program-control keys. They back no appliance feature: the
// discovery layer publishes a button whose command_topic is
// Device.ControlCommand(key), and the command handler recognises the
// matching Device.Relative() result. Spelling them once is the point.
const (
	ControlStartProgram = "start_program"
	ControlStopProgram  = "stop_program"
)

// controlPrefix namespaces the synthetic controls away from the feature
// tree, which is composed from dotted feature names and so can never
// produce a segment starting with an underscore.
const controlPrefix = "_control"

// unnamedPrefix is the path segment for a feature the appliance
// description does not name; the uid follows (FK-8).
const unnamedPrefix = "_uid"

// The two per-appliance status items that are not features.
const (
	itemOnline          = "online"
	itemConnectionState = "connection_state"
)

// Instance is the topic layout of one bridge instance, `<name>/…`.
type Instance struct{ sh hatopic.SmartHome }

// New builds the layout for the instance name (MQTT_TOPIC). It refuses a
// name mqtt-smarthome 2.0 §3 forbids: empty, or containing `/`, `+` or `#`.
func New(name string) (Instance, error) {
	sh, err := hatopic.NewSmartHome(name)
	if err != nil {
		return Instance{}, err
	}
	return Instance{sh: sh}, nil
}

// Name is the instance name.
func (i Instance) Name() string { return i.sh.Name() }

// SmartHome is the go-hamqtt layout this one is built on, for the
// instance-level topics (`info`, `maintenance/…`) this package does not
// spell itself.
func (i Instance) SmartHome() hatopic.SmartHome { return i.sh }

// Connected is `<name>/connected`: the one topic the Last Will writes, the
// one the daemon moves between 1 and 2 with its appliances, and what every
// discovery payload declares as its bridge-level availability source.
//
// It is bridge-level, not device-level: this daemon mirrors several
// appliances over one broker connection, so there is no device to put it
// under.
func (i Instance) Connected() string { return i.sh.Connected() }

// SetFilter is the subscription that covers every command topic of this
// instance, `<name>/set/#`. It is disjoint from every status topic by its
// second level, which is what the state plane's collision guard is told.
func (i Instance) SetFilter() string { return i.sh.Name() + "/" + hatopic.FunctionSet + "/#" }

// Device is the layout of one appliance.
func (i Instance) Device(haID string) Device { return Device{sh: i.sh, haID: haID} }

// Device is the topic layout of one appliance under the instance.
type Device struct {
	sh   hatopic.SmartHome
	haID string
}

// HaID is the appliance's device segment.
func (d Device) HaID() string { return d.haID }

// Status is `<name>/status/<haId>/<item...>`.
func (d Device) Status(item ...string) string {
	return d.sh.Status(append([]string{d.haID}, item...)...)
}

// Set is `<name>/set/<haId>/<item...>`.
func (d Device) Set(item ...string) string {
	return d.sh.Set(append([]string{d.haID}, item...)...)
}

// Online is where the device worker writes the appliance's reachability,
// and what every discovery payload declares as its device-level
// availability.
func (d Device) Online() string { return d.Status(itemOnline) }

// ConnectionState is the appliance link state. It is published retained
// and referenced by no discovery payload (F7, deliberately left).
func (d Device) ConnectionState() string { return d.Status(itemConnectionState) }

// State is where a feature's value is published and what the discovery
// payload advertises as state_topic.
func (d Device) State(feature string, uid int) string {
	return d.Status(FeatureItem(feature, uid)...)
}

// Command is where a feature is written and what the discovery payload
// advertises as command_topic.
func (d Device) Command(feature string, uid int) string {
	return d.Set(FeatureItem(feature, uid)...)
}

// ControlCommand is the command_topic of a synthetic program control.
func (d Device) ControlCommand(key string) string { return d.Set(controlPrefix, key) }

// CommandFilter is the subscription that covers every command topic of
// this device, `<name>/set/<haId>/#`. The feature path is variable-depth,
// so no fixed-arity filter fits — but unlike the old `<root>/<device>/#`
// it no longer matches a single state topic of the daemon's own (F4).
func (d Device) CommandFilter() string { return d.setBase() + "#" }

func (d Device) setBase() string {
	return d.sh.Name() + "/" + hatopic.FunctionSet + "/" + hatopic.Safe(d.haID) + "/"
}

// StatusPrefix is `<name>/status/<haId>/`, the prefix every status item of
// this appliance starts with — and, with `#` appended, the filter that
// reads them back.
func (d Device) StatusPrefix() string {
	return d.sh.Name() + "/" + hatopic.FunctionStatus + "/" + hatopic.Safe(d.haID) + "/"
}

// Relative strips the device's set prefix from an inbound command topic,
// yielding the value ControlPath or FeaturePath produced. ok is false when
// topic is not a command topic of this device.
func (d Device) Relative(topic string) (rel string, ok bool) {
	rest, ok := strings.CutPrefix(topic, d.setBase())
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// ControlPath is the relative path of a synthetic control.
func ControlPath(key string) string { return controlPrefix + "/" + key }

// FeatureItem maps a dotted feature name to its item segments. An unnamed
// feature falls back to a uid-based path so nothing is lost. The segments
// are made topic-safe where they are rendered, by topic.SmartHome.
func FeatureItem(name string, uid int) []string {
	if name == "" {
		return []string{unnamedPrefix, strconv.Itoa(uid)}
	}
	return strings.Split(name, ".")
}

// FeaturePath maps a dotted feature name to its slash-separated, topic-safe
// relative path — the path below `<name>/status/<haId>/`.
func FeaturePath(name string, uid int) string { return hatopic.Join(FeatureItem(name, uid)...) }

// FeatureName is FeaturePath's inverse: it maps a relative path back to
// either a dotted feature name or a uid. Exactly one of the two results is
// meaningful, as reported by byUID.
func FeatureName(rel string) (name string, uid int, byUID bool) {
	if s, ok := strings.CutPrefix(rel, unnamedPrefix+"/"); ok {
		n, err := strconv.Atoi(s)
		if err != nil {
			return "", 0, false
		}
		return "", n, true
	}
	return strings.ReplaceAll(rel, "/", "."), 0, false
}
