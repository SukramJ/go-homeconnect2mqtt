// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package haplane

import (
	"strconv"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// QoS translates the operator's MQTT_QOS into the publisher vocabulary.
//
// It exists because the two vocabularies disagree about zero, and the
// disagreement is silent. `mqtt.QoS(0)` is QoS 0; publisher.QoS(0) is
// [publisher.QoSUnset], which every runtime type in that package resolves
// to QoS 1. An operator who set `MQTT_QOS: 0` is asking for at-most-once,
// and a struct literal that simply omitted the field would have upgraded
// the whole installed base's delivery guarantee inside a migration step
// whose purpose was something else — with a broker capture as the only
// evidence. That is finding F9 of notes/adr0070-phase7-measurement.md.
//
// [publisher.QoSAtMostOnce] is 0x80, deliberately outside the wire's 0-2
// range, precisely so "unset" and "deliberately at most once" cannot be
// written the same way. This function is the one place the two
// vocabularies meet, so there is one place to read and one place to
// change.
//
// MQTT_QOS is validated to 0..1 (internal/config/validate.go), so 2 is
// unreachable from a config file; it is mapped anyway rather than folded
// into the panic, because a value this function silently rounded would be
// the same class of defect it exists to prevent. Anything else is a
// composition-root mistake and panics at wiring time rather than becoming
// a publish at a level nobody chose.
func QoS(mqttQoS int) publisher.QoS {
	switch mqttQoS {
	case 0:
		return publisher.QoSAtMostOnce
	case 1:
		return publisher.QoSAtLeastOnce
	case 2:
		return publisher.QoSExactlyOnce
	default:
		panic("haplane: MQTT_QOS = " + strconv.Itoa(mqttQoS) + " is not an MQTT quality-of-service level")
	}
}
