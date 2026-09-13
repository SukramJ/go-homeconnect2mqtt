// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
	"github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// commandDrainTimeout bounds the router's shutdown drain.
const commandDrainTimeout = 5 * time.Second

// subscribeCommands wires the inbound half onto publisher.CommandRouter:
// one route per device, covering that device's command sub-tree.
//
// # The filter, and why it is still the whole sub-tree
//
// The feature path is variable-depth — a Home Connect feature is a dotted
// name of any length — so no fixed-arity filter covers the command tree
// and "<root>/<device>/#" is forced. It therefore also matches all 689 of
// this daemon's own state, availability and connection-state publishes for
// the device (F4). Three things keep that from turning a state publish
// into a command this daemon issues to itself:
//
//   - MQTT 5.0 No Local, so the broker does not forward this daemon's own
//     publishes back to it at all. The shipped go-hamqtt transport adapter
//     implements publisher.NoLocalSubscriber and the router uses it, so
//     the option survives the move rather than having to be re-passed.
//   - publisher.CommandConfig.DeliverRetained, off. No Local does not
//     cover the retained replay the broker delivers on (re)subscribe —
//     that is not a forward — and this is now a stated policy rather than
//     a hand-written `if msg.Retain` a refactor could drop.
//   - [shouldDispatch]'s Relative check, which is the real disjointness
//     rule: a command topic ends in "/set" and a state topic in "/state".
//     That is a SUFFIX, which an MQTT filter cannot express, which is why
//     publisher.CommandRouter.CheckDisjoint and
//     publisher.StateConfig.CommandFilters — both of which decide by
//     matching a filter — cannot be used here and would refuse every one
//     of this daemon's own state publishes if they were. See
//     TestCommandFilterCannotBeStatedToTheStatePlane, which asserts that
//     rather than leaving it as a comment.
//
// # One route per device, and no overlap
//
// A broker sends one PUBLISH copy per matching subscription, and a client
// that re-matches each copy against its whole filter list runs every
// matching handler per copy — so two overlapping routes run a handler
// twice per published message, which openccu-loom measured against
// Mosquitto and needed a separate connection to fix. The routes here are
// "<root>/<name>/#" per configured device and LoadDevices refuses a
// duplicate name, so they differ in a literal level and cannot overlap;
// publisher.CommandRouter refuses the pair at registration if they ever
// do. The two transient discovery-tree subscriptions this daemon used to
// install are gone (the sweep opens one window under the discovery
// prefix), and the birth subscription is under the discovery prefix too,
// so neither can multiply a command.
func (b *Bridge) subscribeCommands(ctx context.Context) error {
	router := publisher.NewCommandRouter(gomqtt.Transport(b.mqtt), b.commandConfig(ctx))
	for _, d := range b.devices {
		dev := d
		filter := dev.topics.CommandFilter()
		if err := router.Handle(filter, func(hctx context.Context, cmd publisher.Command) {
			b.onCommand(hctx, dev, cmd)
		}); err != nil {
			// A rejected route is a composition mistake — a malformed
			// filter, a duplicate, an overlap — not a broker condition,
			// so it fails the boot rather than being retried.
			return fmt.Errorf("bridge: route %s: %w", filter, err)
		}
	}
	if err := router.Start(ctx); err != nil {
		return err
	}
	b.commands = router
	return b.subscribeBirth(ctx)
}

// commandConfig is the router's policy, in one function rather than a
// literal inside subscribeCommands.
//
// It is a function for the same reason mqttClientConfig is one: two of
// these fields are policy an assertion has to be able to reach.
// DeliverRetained in particular is masked downstream by shouldDispatch's
// own retained check — both are wanted, a retained command re-fires a
// stale write on every (re)subscribe — so a test that only watched the
// outcome would pass with either of them gone.
// TestTheRouterItselfDropsARetainedDelivery builds a router from THIS
// value, which is what makes the policy observable on its own.
func (b *Bridge) commandConfig(ctx context.Context) publisher.CommandConfig {
	return publisher.CommandConfig{
		// Stated, never defaulted: publisher.QoS's zero value is
		// QoSUnset, which resolves to QoS 1, so an operator's MQTT_QOS: 0
		// would be silently upgraded here (F9).
		QoS: haplane.QoS(b.cfg.MQTTQoS),
		// A retained command is somebody's `mosquitto_pub -r` left behind,
		// and the broker replays it on every (re)subscribe.
		DeliverRetained: false,
		// The handler context derives from this, not from Start's: a
		// handler whose write was cancelled because the call that started
		// the router returned is a defect with nothing in the log.
		Lifecycle: ctx,
		Logger:    b.logger,
	}
}

// onCommand is the routed-command handler. It runs on a router worker,
// never on the transport's read loop, which is what makes the blocking
// Home Connect cloud calls in handleSet safe: the old code had to spawn a
// goroutine per delivery for exactly that reason, and an unbounded one at
// that. Order is preserved per topic, which the goroutine gave up.
func (b *Bridge) onCommand(ctx context.Context, d *Device, cmd publisher.Command) {
	if !shouldDispatch(d, cmd.Topic, cmd.Retained) {
		return
	}
	b.handleSet(ctx, d, cmd.Topic, cmd.Payload)
}

// shouldDispatch decides, without side effects, whether an inbound message
// on the device sub-tree is a command this daemon should act on.
//
// It is a function rather than an inline pair of early returns because the
// alternative is untestable: a handler that returns early leaves no trace,
// so deleting either check below is invisible to every test that can be
// written against the handler itself.
//
// Both checks earn their place:
//
//   - Retained. publisher.CommandConfig.DeliverRetained is off, so the
//     router already drops these — but the router's policy and this
//     daemon's are two statements, and the one that costs an operator a
//     re-fired write on every reconnect is worth asserting twice. A
//     retained delivery at subscribe time is not a forward, so MQTT 5.0
//     No Local does not cover it either.
//   - A command topic at all. The subscription is the whole device
//     sub-tree (the feature path is variable-depth, so no fixed-arity
//     filter fits), which matches every state, availability and
//     connection-state topic this daemon publishes for the device. With
//     MQTT_RETAIN: false the retained check never fires, and each of those
//     used to spawn a goroutine whose only job was to return (F4).
func shouldDispatch(d *Device, topic string, retained bool) bool {
	if retained {
		return false
	}
	_, ok := d.topics.Relative(topic)
	return ok
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
func (b *Bridge) handleSet(parent context.Context, d *Device, msgTopic string, payload []byte) {
	rel, ok := d.topics.Relative(msgTopic)
	if !ok {
		return // a state/availability publish echoed back, ignore
	}
	value := strings.TrimSpace(string(payload))

	if b.handleProgramControl(parent, d, rel) {
		return // a synthetic start/stop control, not a feature write
	}

	entity, ok := b.resolveEntity(d, rel)
	if !ok {
		b.logger.Warn("bridge.command_unknown_feature", slog.String("device", d.name), slog.String("topic", msgTopic))
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
	name, uid, byUID := layout.FeatureName(rel)
	if byUID {
		return d.app.Entity(uid)
	}
	if name == "" {
		return nil, false
	}
	return d.app.EntityByName(name)
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

// handleProgramControl runs a synthetic start/stop control, reporting whether
// rel was one (so the caller skips the feature-write path).
func (b *Bridge) handleProgramControl(parent context.Context, d *Device, rel string) bool {
	// The relative paths the discovery layer advertises as the two
	// synthetic buttons' command_topic. Both sides read layout.ControlPath,
	// so a press can never land on a path nothing handles (F3).
	start, stop := layout.ControlPath(layout.ControlStartProgram), layout.ControlPath(layout.ControlStopProgram)
	if rel != start && rel != stop {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, b.cfg.SendTimeoutDuration()+b.cmdRetryDelay*time.Duration(b.cmdRetries+1))
	defer cancel()
	switch rel {
	case start:
		b.startSelectedProgram(ctx, d)
	case stop:
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
