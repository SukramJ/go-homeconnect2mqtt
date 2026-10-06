// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
	"github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/i18n"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
)

// commandDrainTimeout bounds the router's shutdown drain.
const commandDrainTimeout = 5 * time.Second

// subscribeCommands wires the inbound half onto publisher.CommandRouter:
// one route per device, covering that device's command sub-tree, plus the
// instance's maintenance commands.
//
// # The filter
//
// The feature path is variable-depth — a Home Connect feature is a dotted
// name of any length — so no fixed-arity filter covers the command tree
// and "<name>/set/<haId>/#" is forced. Before 0.15.0 the equivalent filter
// was "<root>/<device>/#", which also matched all 689 of this daemon's own
// state, availability and connection-state publishes for the device (F4).
// The function now sits at the second level, so the command tree and the
// status tree are disjoint by an MQTT filter, and the state plane is told
// `<name>/set/#` as its collision guard (haplane.Config.SetFilter). What
// still stands guard on top of that:
//
//   - MQTT 5.0 No Local, so the broker does not forward this daemon's own
//     publishes back to it at all. The shipped go-hamqtt transport adapter
//     implements publisher.NoLocalSubscriber and the router uses it.
//   - publisher.CommandConfig.DeliverRetained, off. A retained `set` is
//     ignored (openccu-loom ADR 0083): it is somebody's `mosquitto_pub -r`
//     left behind, and the broker replays it on every (re)subscribe.
//   - [shouldDispatch], which states the same two rules once more where a
//     test can reach them.
//
// # The payload
//
// Every route is a publisher.CommandRouter.HandleSet route, so the payload
// arrives normalised per mqtt-smarthome 2.0 §5.3: a plain value and
// `{"val": …}` are the same thing, an empty payload never reaches a
// handler, and malformed JSON is logged at warn with its topic and payload
// by the router itself.
//
// # One route per device, and no overlap
//
// A broker sends one PUBLISH copy per matching subscription, and a client
// that re-matches each copy against its whole filter list runs every
// matching handler per copy — so two overlapping routes run a handler
// twice per published message, which openccu-loom measured against
// Mosquitto and needed a separate connection to fix. The routes here are
// "<name>/set/<haId>/#" per configured device and bridge.New refuses a
// duplicate haId, so they differ in a literal level and cannot overlap;
// the maintenance route is "<name>/maintenance/set/#", disjoint by its
// second level; publisher.CommandRouter refuses the pair at registration
// if they ever do overlap. The migration sweep's windows stay outside the
// command tree for the same reason (see migrate.go), and the birth
// subscription is under the discovery prefix, so neither can multiply a
// command.
func (b *Bridge) subscribeCommands(ctx context.Context) error {
	router := publisher.NewCommandRouter(gomqtt.Transport(b.mqtt), b.commandConfig(ctx))
	for _, d := range b.devices {
		dev := d
		filter := dev.topics.CommandFilter()
		if err := router.HandleSet(filter, func(hctx context.Context, cmd publisher.Command, v publisher.SetValue) {
			b.onCommand(hctx, dev, cmd, v)
		}); err != nil {
			// A rejected route is a composition mistake — a malformed
			// filter, a duplicate, an overlap — not a broker condition,
			// so it fails the boot rather than being retried.
			return fmt.Errorf("bridge: route %s: %w", filter, err)
		}
	}
	if b.instance != nil {
		if err := b.instance.Register(router); err != nil {
			return fmt.Errorf("bridge: maintenance route: %w", err)
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
		// QoS 1, stated rather than defaulted, and no longer MQTT_QOS:
		// openccu-loom ADR 0083 subscribes `set` at QoS 1 in all six
		// projects, because a lost write on a flaky link is the failure
		// operators report. A subscription QoS is an upper bound, so a
		// consumer that publishes at QoS 0 still gets QoS 0.
		QoS: publisher.QoSAtLeastOnce,
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
// Home Connect calls in handleSet safe: the old code had to spawn a
// goroutine per delivery for exactly that reason, and an unbounded one at
// that. Order is preserved per topic, which the goroutine gave up.
func (b *Bridge) onCommand(ctx context.Context, d *Device, cmd publisher.Command, v publisher.SetValue) {
	if !shouldDispatch(d, cmd.Topic, cmd.Retained) {
		return
	}
	b.handleSet(ctx, d, setRequest{topic: cmd.Topic, payload: string(cmd.Payload), value: v})
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
//   - A command topic of THIS device, with an item below it.
func shouldDispatch(d *Device, topic string, retained bool) bool {
	if retained {
		return false
	}
	_, ok := d.topics.Relative(topic)
	return ok
}

// subscribeBirth watches the Home Assistant status topic and re-publishes
// discovery for every device when HA comes back online (docs/04 §6.3).
//
// The re-publish goes through [Bridge.republishDiscovery] rather than
// looping over the devices here, and that is the fix for a race this
// handler drove rather than merely risked.
//
// Home Assistant publishes homeassistant/status RETAINED, and this handler
// deliberately keeps retained deliveries (an HA that came up before this
// daemon did announced itself once, and the replay is the only copy this
// daemon will ever see). So the broker replays `online` INLINE on the
// SUBSCRIBE below — which Run performs in subscribeCommands, BEFORE
// refreshDiscoveryOnce and before `started` closes. A handler that
// published the fleet directly therefore published every appliance's
// document into precisely the window HASS_DISCOVERY_REFRESH was about to
// clear: the refresh retracted the document it had just written, Home
// Assistant removed the device and all 687 entities, and the settle delay
// plus a republish put them back. The end state was correct and the cost
// was a whole extra migration per boot per appliance, a second concurrent
// fleet-wide snapshot window, and a window with no config at all that a
// shutdown or a link drop inside it makes permanent.
//
// republishDiscovery is the one path that waits on `started`, coalesces
// against the (re)connect pass and honours StopDiscovery, so routing this
// handler through it makes the gate the invariant #45 stated rather than
// the invariant one of the two asynchronous publishers happened to keep.
// It also serialises the two passes against each other, which closes the
// interleaving in which one pass's document write lands before the
// refresh's retraction while its runtime bookkeeping lands after — the
// broker holding a retraction the runtime claims as a document, and the
// sweep then clearing the per-entity leftovers too.
func (b *Bridge) subscribeBirth(ctx context.Context) error {
	if b.hass == nil {
		return nil
	}
	_, err := b.mqtt.Subscribe(ctx, b.hass.BirthTopic(), b.qos, func(msg *mqtt.Message) {
		if !strings.EqualFold(strings.TrimSpace(string(msg.Payload)), "online") {
			return
		}
		// Off the read-loop goroutine: the pass does per-device MQTT
		// publishes and the adapter calls this handler synchronously
		// inline, so a pass run here would stall PUBACK/PINGRESP
		// processing. See [mqtt.MessageHandler].
		go b.republishDiscovery(ctx)
	})
	return err
}

// setRequest is one normalised `set`: the value, and the topic and raw
// payload every rejection is logged with (mqtt-smarthome 2.0 §3.3).
type setRequest struct {
	topic   string
	payload string
	value   publisher.SetValue
}

// reject logs a `set` this daemon will not carry out, at warn, with its
// topic and payload — the spec's MUST for a rejected or failed request.
func (b *Bridge) reject(ctx context.Context, d *Device, req setRequest, msg string, attrs ...slog.Attr) {
	all := append([]slog.Attr{
		slog.String("device", d.name),
		slog.String("topic", req.topic),
		slog.String("payload", req.payload),
	}, attrs...)
	b.logger.LogAttrs(ctx, slog.LevelWarn, msg, all...)
}

// handleSet resolves an incoming `set` to a feature and applies it,
// choosing the device-specific program-start path where applicable (FK-4)
// and gating writes on the dynamic access window (FK-5).
func (b *Bridge) handleSet(parent context.Context, d *Device, req setRequest) {
	rel, ok := d.topics.Relative(req.topic)
	if !ok {
		return // not a command topic of this device
	}

	if b.handleProgramControl(parent, d, rel, req) {
		return // a synthetic start/stop control, not a feature write
	}

	entity, ok := b.resolveEntity(d, rel)
	if !ok {
		b.reject(parent, d, req, "bridge.command_unknown_feature")
		return
	}

	ctx, cancel := context.WithTimeout(parent, b.cfg.SendTimeoutDuration()+b.cmdRetryDelay*time.Duration(b.cmdRetries+1))
	defer cancel()

	if isProgramKind(entity.Desc.Kind) || entity.Desc.Kind == profile.KindProgram {
		if req.value.Structured() {
			b.reject(ctx, d, req, "bridge.command_rejected", slog.String("reason", "a program takes a plain value"))
			return
		}
	}
	value := req.value.Text
	switch entity.Desc.Kind {
	case profile.KindProgram:
		b.startProgram(ctx, d, req, entity.UID(), value)
	case profile.KindSelectedProgram:
		b.selectNamedProgram(ctx, d, req, value)
	case profile.KindActiveProgram:
		if isStopValue(value) {
			b.runProgramCall(ctx, d, req, "stop", func() error { _, err := d.app.StopActiveProgram(ctx); return err })
			return
		}
		b.startNamedProgram(ctx, d, req, value)
	default:
		v, err := b.writeValue(entity, req.value)
		if err != nil {
			b.reject(ctx, d, req, "bridge.command_rejected", slog.String("err", err.Error()))
			return
		}
		b.writeWithWindow(ctx, d, req, entity, v)
	}
}

// writeValue converts a normalised `set` into the value written to the
// appliance, per mqtt-smarthome 2.0 §5.3:
//
//   - an enum by token, case-insensitively, or by a label in the configured
//     language — the reverse mapping Home Assistant's dropdown has always
//     relied on, kept because the spec allows it;
//   - a boolean from true/false, 1/0, on/off or yes/no, in any case;
//   - a number rounded to the feature's step and clamped to its range,
//     where the appliance reported them;
//   - an Object feature from a structured JSON body;
//   - anything else as the plain text.
func (b *Bridge) writeValue(e *homeconnect.Entity, v publisher.SetValue) (any, error) {
	if v.Structured() {
		if e.Desc.ProtocolType != profile.ProtocolObject {
			return nil, errors.New("a structured value is only accepted by an object feature")
		}
		var body any
		if err := json.Unmarshal(v.Params, &body); err != nil {
			return nil, fmt.Errorf("structured value: %w", err)
		}
		return body, nil
	}
	if e.Desc.IsEnum() {
		return i18n.EnumValue(v.Text, b.cfg.Language), nil // accept localized dropdown labels
	}
	switch e.Desc.ProtocolType {
	case profile.ProtocolBoolean:
		return v.Bool()
	case profile.ProtocolInteger, profile.ProtocolFloat:
		bd := e.Bounds()
		lo, hi, step := math.Inf(-1), math.Inf(1), 0.0
		if bd.HasMin {
			lo = bd.Min
		}
		if bd.HasMax {
			hi = bd.Max
		}
		switch {
		case bd.HasStep && bd.Step > 0:
			step = bd.Step
		case e.Desc.ProtocolType == profile.ProtocolInteger:
			step = 1
		}
		return v.Number(lo, hi, step)
	default:
		return v.Text, nil
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

// writeWithWindow writes a value, retrying within the dynamic access
// window: a not-yet-writable feature or a 541 ProcessStateNotCompliant is
// retried a bounded number of times (FK-5, #384).
func (b *Bridge) writeWithWindow(ctx context.Context, d *Device, req setRequest, e *homeconnect.Entity, value any) {
	for attempt := 0; ; attempt++ {
		if e.Writable() {
			err := d.app.WriteValue(ctx, e.UID(), value)
			if err == nil {
				return
			}
			if !isWriteWindowError(err) || attempt >= b.cmdRetries {
				b.reject(ctx, d, req, "bridge.write_failed",
					slog.Int("uid", e.UID()), slog.String("err", err.Error()))
				return
			}
		} else if attempt >= b.cmdRetries {
			b.reject(ctx, d, req, "bridge.write_not_writable",
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
// rel was one (so the caller skips the feature-write path). A control is an
// action item: any non-empty payload fires it, and the router has already
// dropped an empty one.
func (b *Bridge) handleProgramControl(parent context.Context, d *Device, rel string, req setRequest) bool {
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
		b.startSelectedProgram(ctx, d, req)
	case stop:
		b.runProgramCall(ctx, d, req, "stop", func() error { _, err := d.app.StopActiveProgram(ctx); return err })
	}
	return true
}

// startSelectedProgram starts the program currently chosen in the
// selected-program select (the appliances expose no start command; we post the
// selected program to /ro/activeProgram via the device's start strategy).
func (b *Bridge) startSelectedProgram(ctx context.Context, d *Device, req setRequest) {
	sp, ok := d.app.EntityByName("BSH.Common.Root.SelectedProgram")
	if !ok {
		b.reject(ctx, d, req, "bridge.no_selected_program")
		return
	}
	sel, ok := sp.Value().(string)
	if !ok || sel == "" {
		b.reject(ctx, d, req, "bridge.no_program_selected")
		return
	}
	uid, ok := b.resolveProgramUID(d, sel)
	if !ok {
		b.reject(ctx, d, req, "bridge.unknown_program", slog.String("program", sel))
		return
	}
	b.startProgram(ctx, d, req, uid, sel)
}

func (b *Bridge) startProgram(ctx context.Context, d *Device, req setRequest, programUID int, value string) {
	// A program feature may be toggled with an explicit stop.
	if isStopValue(value) {
		b.runProgramCall(ctx, d, req, "stop", func() error { _, err := d.app.StopActiveProgram(ctx); return err })
		return
	}
	strategy := b.startStrategy(d)
	b.runProgramCall(ctx, d, req, "start", func() error {
		_, err := d.app.StartProgram(ctx, programUID, nil, strategy)
		return err
	})
}

func (b *Bridge) startNamedProgram(ctx context.Context, d *Device, req setRequest, name string) {
	uid, ok := b.resolveProgramUID(d, name)
	if !ok {
		b.reject(ctx, d, req, "bridge.unknown_program", slog.String("program", name))
		return
	}
	b.startProgram(ctx, d, req, uid, name)
}

func (b *Bridge) selectNamedProgram(ctx context.Context, d *Device, req setRequest, name string) {
	uid, ok := b.resolveProgramUID(d, name)
	if !ok {
		b.reject(ctx, d, req, "bridge.unknown_program", slog.String("program", name))
		return
	}
	b.runProgramCall(ctx, d, req, "select", func() error { _, err := d.app.SelectProgram(ctx, uid, nil); return err })
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
	if _, after, ok := strings.CutLast(name, "."); ok {
		return after
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
func (b *Bridge) runProgramCall(ctx context.Context, d *Device, req setRequest, action string, fn func() error) {
	if err := fn(); err != nil {
		b.reject(ctx, d, req, "bridge.program_call_failed",
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
