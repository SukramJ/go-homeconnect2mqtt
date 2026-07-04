// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// subscribeCommands subscribes each device to its command sub-tree. The
// MQTT adapter replays subscriptions across reconnects, so one call at
// startup is enough.
func (b *Bridge) subscribeCommands(ctx context.Context) error {
	for _, d := range b.devices {
		dev := d
		filter := dev.topics.base + "/#"
		if _, err := b.mqtt.Subscribe(ctx, filter, b.qos, func(msg *mqtt.Message) {
			if msg.Retain {
				// Drop the broker's replay of the last retained command on
				// (re)subscribe: without this, a stale command topic (or our
				// own retained state loopback) re-fires the write on every
				// reconnect. See [mqtt.MessageHandler] for the retained bit.
				return
			}
			// handleSet makes blocking Home Connect cloud HTTP calls with
			// retry/backoff loops; the adapter calls this handler
			// synchronously inline in its read loop, so a blocking call here
			// would stall PUBACK/PINGRESP processing and could trip a
			// spurious ping_timeout. See [mqtt.MessageHandler].
			go b.handleSet(ctx, dev, msg.Topic, msg.Payload)
		}); err != nil {
			return err
		}
	}
	return b.subscribeBirth(ctx)
}

// subscribeBirth watches the Home Assistant status topic and re-publishes
// discovery for every device when HA comes back online (docs/04 §6.3).
func (b *Bridge) subscribeBirth(ctx context.Context) error {
	if b.hass == nil {
		return nil
	}
	_, err := b.mqtt.Subscribe(ctx, b.hass.BirthTopic(), b.qos, func(msg *mqtt.Message) {
		// HA publishes homeassistant/status retained, so a retained replay
		// on (re)subscribe must still trigger a discovery re-publish here
		// (unlike subscribeCommands, this handler does not drop retained).
		if strings.EqualFold(strings.TrimSpace(string(msg.Payload)), "online") {
			// publishDiscovery does per-device MQTT publishes; run the loop
			// off the read-loop goroutine so it can't stall PUBACK/PINGRESP
			// processing (the adapter calls this handler synchronously
			// inline). See [mqtt.MessageHandler].
			go func() {
				for _, d := range b.devices {
					b.publishDiscovery(ctx, d)
				}
			}()
		}
	})
	return err
}

// handleSet resolves an incoming "/set" command to a feature and applies
// it, choosing the device-specific program-start path where applicable
// (FK-4) and gating writes on the dynamic access window (FK-5).
func (b *Bridge) handleSet(parent context.Context, d *Device, topic string, payload []byte) {
	if !strings.HasSuffix(topic, "/set") {
		return // a state/availability publish echoed back, ignore
	}
	rel := strings.TrimPrefix(topic, d.topics.base+"/")
	rel = strings.TrimSuffix(rel, "/set")
	value := strings.TrimSpace(string(payload))

	if b.handleProgramControl(parent, d, rel) {
		return // a synthetic start/stop control, not a feature write
	}

	entity, ok := b.resolveEntity(d, rel)
	if !ok {
		b.logger.Warn("bridge.command_unknown_feature", slog.String("device", d.name), slog.String("topic", topic))
		return
	}

	ctx, cancel := context.WithTimeout(parent, b.cfg.SendTimeoutDuration()+b.cmdRetryDelay*time.Duration(b.cmdRetries+1))
	defer cancel()

	switch entity.Desc.Kind {
	case profile.KindProgram:
		b.startProgram(ctx, d, entity.UID(), value)
	case profile.KindSelectedProgram:
		b.selectNamedProgram(ctx, d, value)
	case profile.KindActiveProgram:
		if isStopValue(value) {
			b.runProgramCall(ctx, d, "stop", func() error { _, err := d.app.StopActiveProgram(ctx); return err })
			return
		}
		b.startNamedProgram(ctx, d, value)
	default:
		b.writeWithWindow(ctx, d, entity, value)
	}
}

// resolveEntity maps a relative topic path back to an entity, handling both
// the dotted feature name and the _uid/<n> fallback path.
func (b *Bridge) resolveEntity(d *Device, rel string) (*homeconnect.Entity, bool) {
	if uidStr, ok := strings.CutPrefix(rel, "_uid/"); ok {
		if uid, err := strconv.Atoi(uidStr); err == nil {
			return d.app.Entity(uid)
		}
		return nil, false
	}
	return d.app.EntityByName(strings.ReplaceAll(rel, "/", "."))
}

// writeWithWindow writes a scalar value, retrying within the dynamic access
// window: a not-yet-writable feature or a 541 ProcessStateNotCompliant is
// retried a bounded number of times (FK-5, #384).
func (b *Bridge) writeWithWindow(ctx context.Context, d *Device, e *homeconnect.Entity, value string) {
	if e.Desc.IsEnum() {
		value = i18n.EnumValue(value, b.cfg.Language) // accept localized dropdown labels
	}
	for attempt := 0; ; attempt++ {
		if e.Writable() {
			err := d.app.WriteValue(ctx, e.UID(), value)
			if err == nil {
				return
			}
			if !isWriteWindowError(err) || attempt >= b.cmdRetries {
				b.logger.Warn("bridge.write_failed", slog.String("device", d.name),
					slog.Int("uid", e.UID()), slog.String("err", err.Error()))
				return
			}
		} else if attempt >= b.cmdRetries {
			b.logger.Warn("bridge.write_not_writable", slog.String("device", d.name),
				slog.Int("uid", e.UID()), slog.String("access", e.Access()))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(b.cmdRetryDelay):
		}
	}
}

// Synthetic program-control topics (published by the discovery layer; they back
// no device feature). Start posts the selected program to /ro/activeProgram.
const (
	controlStartProgram = "_control/start_program"
	controlStopProgram  = "_control/stop_program"
)

// handleProgramControl runs a synthetic start/stop control, reporting whether
// rel was one (so the caller skips the feature-write path).
func (b *Bridge) handleProgramControl(parent context.Context, d *Device, rel string) bool {
	if rel != controlStartProgram && rel != controlStopProgram {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, b.cfg.SendTimeoutDuration()+b.cmdRetryDelay*time.Duration(b.cmdRetries+1))
	defer cancel()
	switch rel {
	case controlStartProgram:
		b.startSelectedProgram(ctx, d)
	case controlStopProgram:
		b.runProgramCall(ctx, d, "stop", func() error { _, err := d.app.StopActiveProgram(ctx); return err })
	}
	return true
}

// startSelectedProgram starts the program currently chosen in the
// selected-program select (the appliances expose no start command; we post the
// selected program to /ro/activeProgram via the device's start strategy).
func (b *Bridge) startSelectedProgram(ctx context.Context, d *Device) {
	sp, ok := d.app.EntityByName("BSH.Common.Root.SelectedProgram")
	if !ok {
		b.logger.Warn("bridge.no_selected_program", slog.String("device", d.name))
		return
	}
	sel, ok := sp.Value().(string)
	if !ok || sel == "" {
		b.logger.Warn("bridge.no_program_selected", slog.String("device", d.name))
		return
	}
	uid, ok := b.resolveProgramUID(d, sel)
	if !ok {
		b.logger.Warn("bridge.unknown_program", slog.String("device", d.name), slog.String("program", sel))
		return
	}
	b.startProgram(ctx, d, uid, sel)
}

func (b *Bridge) startProgram(ctx context.Context, d *Device, programUID int, value string) {
	// A program feature may be toggled with an explicit stop.
	if isStopValue(value) {
		b.runProgramCall(ctx, d, "stop", func() error { _, err := d.app.StopActiveProgram(ctx); return err })
		return
	}
	strategy := b.startStrategy(d)
	b.runProgramCall(ctx, d, "start", func() error {
		_, err := d.app.StartProgram(ctx, programUID, nil, strategy)
		return err
	})
}

func (b *Bridge) startNamedProgram(ctx context.Context, d *Device, name string) {
	uid, ok := b.resolveProgramUID(d, name)
	if !ok {
		b.logger.Warn("bridge.unknown_program", slog.String("device", d.name), slog.String("program", name))
		return
	}
	b.startProgram(ctx, d, uid, name)
}

func (b *Bridge) selectNamedProgram(ctx context.Context, d *Device, name string) {
	uid, ok := b.resolveProgramUID(d, name)
	if !ok {
		b.logger.Warn("bridge.unknown_program", slog.String("device", d.name), slog.String("program", name))
		return
	}
	b.runProgramCall(ctx, d, "select", func() error { _, err := d.app.SelectProgram(ctx, uid, nil); return err })
}

// resolveProgramUID maps a program reference to a uid: the full feature name, a
// numeric uid string, or a (possibly localized) select label such as
// "Eco 50 °C" — the label is de-localized and matched against each program's
// short name, so a Home Assistant select option resolves back to its program.
func (b *Bridge) resolveProgramUID(d *Device, name string) (int, bool) {
	if e, ok := d.app.EntityByName(name); ok {
		return e.UID(), true
	}
	if uid, err := strconv.Atoi(name); err == nil {
		return uid, true
	}
	if key := progNorm(i18n.EnumValue(name, b.cfg.Language)); key != "" {
		for _, e := range d.app.Entities() {
			if e.Desc.Kind == profile.KindProgram && progNorm(progLeaf(e.Name())) == key {
				return e.UID(), true
			}
		}
	}
	return 0, false
}

// progLeaf is the last dotted segment of a program feature name.
func progLeaf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// progNorm lower-cases and strips non-alphanumerics, matching the i18n key form
// so a localized label, the English leaf and the raw value all compare equal.
func progNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// startStrategy picks the start path for a device: hobs need the direct
// selectedProgram post; everything else uses the standard activeProgram.
func (b *Bridge) startStrategy(d *Device) homeconnect.ProgramStartStrategy {
	if strings.EqualFold(d.app.Info().Type, "Hob") || strings.EqualFold(d.app.Info().Type, "Cooktop") {
		return homeconnect.StartHob
	}
	if _, ok := d.app.EntityByName("BSH.Common.Command.StartProgram"); ok {
		return homeconnect.StartCommand
	}
	return homeconnect.StartStandard
}

// runProgramCall runs a program control call and logs a device error.
func (b *Bridge) runProgramCall(_ context.Context, d *Device, action string, fn func() error) {
	if err := fn(); err != nil {
		b.logger.Warn("bridge.program_call_failed", slog.String("device", d.name),
			slog.String("action", action), slog.String("err", err.Error()))
	}
}

func isStopValue(v string) bool {
	switch strings.ToLower(v) {
	case "off", "stop", "0", "", "false":
		return true
	}
	return false
}

// isWriteWindowError reports whether err is a 541 ProcessStateNotCompliant,
// i.e. the dynamic write window is currently closed (FK-5).
func isWriteWindowError(err error) bool {
	var ce *homeconnect.CodeResponseError
	return errors.As(err, &ce) && ce.Code == 541
}
