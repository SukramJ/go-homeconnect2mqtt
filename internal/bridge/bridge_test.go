// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/haplane"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/hass"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/homeconnect"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/layout"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/profile"
	"github.com/SukramJ/go-homeconnect2mqtt/internal/slug"
)

// stubMQTT records publishes and subscriptions for assertions.
type stubMQTT struct {
	mu   sync.Mutex
	pubs map[string]string
	subs map[string]mqtt.MessageHandler
}

func newStubMQTT() *stubMQTT {
	return &stubMQTT{pubs: map[string]string{}, subs: map[string]mqtt.MessageHandler{}}
}

func (s *stubMQTT) Publish(_ context.Context, topic string, payload []byte, _ mqtt.QoS, _ bool, _ ...mqtt.PublishOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pubs[topic] = string(payload)
	return nil
}

func (s *stubMQTT) Subscribe(_ context.Context, filter string, _ mqtt.QoS, h mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[filter] = h
	return mqtt.SubscribeResult{}, nil
}

func (s *stubMQTT) Unsubscribe(_ context.Context, filter string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, filter)
	return nil
}

func (s *stubMQTT) get(topic string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubs[topic]
}

func testCfg() *config.Config {
	return &config.Config{
		MQTTTopic: "homeconnect", MQTTQoS: new(1), AppName: "test",
		ReconnectInitial: 1, ReconnectMax: 30, ReconnectJitter: 0,
		HandshakeTimeout: 60, SendTimeout: 20, Heartbeat: 20, Language: "en",
	}
}

func smallDescription(t *testing.T) *profile.Description {
	t.Helper()
	dd := `<?xml version="1.0"?><device>
      <description><type>Dishwasher</type><brand>BOSCH</brand><model>M</model><version>2</version></description>
      <statusList uid="0001">
        <status access="read" available="true" enumerationType="3000" refCID="03" uid="1002"/>
      </statusList>
      <settingList uid="0003">
        <setting access="readWrite" available="true" refCID="01" uid="1005"/>
      </settingList>
      <enumerationTypeList>
        <enumerationType enid="3000"><enumeration value="0"/><enumeration value="3"/></enumerationType>
      </enumerationTypeList>
    </device>`
	fm := `<featureMappingFile><featureDescription>
        <feature refUID="1002">BSH.Common.Status.OperationState</feature>
        <feature refUID="1005">BSH.Common.Setting.PowerState</feature>
      </featureDescription>
      <enumDescriptionList><enumDescription refENID="3000">
        <enumMember refValue="0">Inactive</enumMember>
        <enumMember refValue="3">Run</enumMember>
      </enumDescription></enumDescriptionList></featureMappingFile>`
	d, err := profile.ParseDescription([]byte(dd), []byte(fm), nil)
	if err != nil {
		t.Fatalf("ParseDescription: %v", err)
	}
	return d
}

func b64(n int) string { return base64.RawURLEncoding.EncodeToString(make([]byte, n)) }

// testPlane builds the real go-hamqtt publish plane over a stub client,
// with the shipped defaults. Every test that builds a Bridge needs one:
// the state plane is where entity values, availability and
// connection_state now go, so a Bridge without it cannot publish at all.
func testPlane(c mqtt.Client) *haplane.Plane {
	cfg := testCfg()
	inst := testLayout(cfg.MQTTTopic)
	return haplane.New(hagomqtt.Transport(c), haplane.Config{
		Prefix:      "homeassistant",
		StatusTopic: inst.Connected(),
		Layout:      hass.NewLayout(inst),
		QoS:         haplane.QoS(cfg.QoSLevel()),
		SetFilter:   inst.SetFilter(),
		Logger:      slog.New(slog.DiscardHandler),
	})
}

// testLayout is the topic layout of an instance name.
func testLayout(name string) layout.Instance {
	inst, err := layout.New(name)
	if err != nil {
		panic(err)
	}
	return inst
}

// haIDFor is the haId a test appliance named name carries. Any stable,
// valid id will do; one derived from the name keeps fixtures readable and
// two differently named appliances apart.
func haIDFor(name string) string { return "HAID-" + slug.Slug(name) }

func buildTestBridge(t *testing.T) (*Bridge, *stubMQTT) {
	t.Helper()
	stub := newStubMQTT()
	b, err := New(Deps{
		Config: testCfg(),
		MQTT:   stub,
		Plane:  testPlane(stub),
		Devices: []DeviceSpec{{
			Config: profile.DeviceConfig{
				Name: "dishwasher", HaID: haIDFor("dishwasher"), Host: "192.168.1.50",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, stub
}

// startPublisher runs the device's async publish drain for the test's
// lifetime, mirroring what Bridge.Run wires up.
func startPublisher(t *testing.T, b *Bridge, d *Device) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.pub.run(ctx, b.publish) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestFeaturePath(t *testing.T) {
	if got := layout.FeaturePath("BSH.Common.Status.OperationState", 0x1002); got != "BSH/Common/Status/OperationState" {
		t.Errorf("featurePath = %q", got)
	}
	if got := layout.FeaturePath("", 0x1234); got != "_uid/4660" {
		t.Errorf("unnamed featurePath = %q", got)
	}
}

func TestDeviceTopics(t *testing.T) {
	tp := newDeviceTopics(testLayout("homeconnect"), "HAID-1")
	if tp.Online() != "homeconnect/status/HAID-1/online" {
		t.Errorf("online = %q", tp.Online())
	}
	if tp.ConnectionState() != "homeconnect/status/HAID-1/connection_state" {
		t.Errorf("connection_state = %q", tp.ConnectionState())
	}
}

// statusVal decodes the `val` of a status object, or reports the payload
// as it is when it is not one.
func statusVal(payload string) any {
	var obj struct {
		Val any `json:"val"`
	}
	if json.Unmarshal([]byte(payload), &obj) != nil {
		return payload
	}
	return obj.Val
}

// waitForVal polls the stub until topic carries a status object whose
// `val` is want, or the deadline hits.
func waitForVal(t *testing.T, stub *stubMQTT, topic string, want any) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for statusVal(stub.get(topic)) != want {
		select {
		case <-deadline:
			t.Fatalf("topic %q = %q, want val %v", topic, stub.get(topic), want)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

const testStatus = "homeconnect/status/HAID-dishwasher"

// TestOnUpdatePublishesState: an enum carries its TOKEN as `val`, in a
// status object — the localized label it used to publish is discovery's.
func TestOnUpdatePublishesState(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	startPublisher(t, b, dev)
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1002, "value": 3}})
	waitForVal(t, stub, testStatus+"/BSH/Common/Status/OperationState", "Run")
}

// TestOnUpdatePublishesBool: a boolean is a JSON boolean.
func TestOnUpdatePublishesBool(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	startPublisher(t, b, dev)
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1005, "value": true}})
	waitForVal(t, stub, testStatus+"/BSH/Common/Setting/PowerState", true)
	if raw := stub.get(testStatus + "/BSH/Common/Setting/PowerState"); !strings.HasPrefix(raw, `{"val":true,"ts":`) {
		t.Errorf("payload = %q, want a status object with a JSON boolean", raw)
	}
}

// TestOnStatePublishesOnlineAndConnected: the appliance's reachability is
// its `online` item, a boolean, and the instance's `connected` follows the
// fleet — 2 while an appliance is reachable, 1 while none is.
func TestOnStatePublishesOnlineAndConnected(t *testing.T) {
	b, stub := buildTestBridge(t)
	dev := b.devices[0]
	b.onState(dev, homeconnect.StateConnected)
	if got := statusVal(stub.get(testStatus + "/connection_state")); got != "connected" {
		t.Errorf("connection_state = %v", got)
	}
	if got := statusVal(stub.get(testStatus + "/online")); got != true {
		t.Errorf("online = %v, want true", got)
	}
	if got := stub.get("homeconnect/connected"); got != "2" {
		t.Errorf("connected = %q, want 2 while an appliance is reachable", got)
	}
	b.onState(dev, homeconnect.StateReconnecting)
	if got := statusVal(stub.get(testStatus + "/online")); got != false {
		t.Errorf("online after reconnecting = %v, want false", got)
	}
	if got := stub.get("homeconnect/connected"); got != "1" {
		t.Errorf("connected = %q, want 1 once no appliance is reachable", got)
	}
}

// TestConnectedNeedsEveryApplianceDown: with two appliances, one dropping
// leaves the instance operational — its own `online` item says it is gone.
func TestConnectedNeedsEveryApplianceDown(t *testing.T) {
	stub := newStubMQTT()
	specs := make([]DeviceSpec, 0, 2)
	for _, name := range []string{"dw", "oven"} {
		specs = append(specs, DeviceSpec{
			Config: profile.DeviceConfig{
				Name: name, HaID: haIDFor(name), Host: "h",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		})
	}
	b, err := New(Deps{Config: testCfg(), MQTT: stub, Plane: testPlane(stub), Devices: specs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	steps := []struct {
		dev  int
		s    homeconnect.ConnectionState
		want string
	}{
		{0, homeconnect.StateConnected, "2"},
		{1, homeconnect.StateConnected, "2"},
		{0, homeconnect.StateOffline, "2"},
		{1, homeconnect.StateReconnecting, "1"},
		{1, homeconnect.StateConnected, "2"},
	}
	for i, st := range steps {
		b.onState(b.devices[st.dev], st.s)
		if got := stub.get("homeconnect/connected"); got != st.want {
			t.Fatalf("step %d: connected = %q, want %s", i, got, st.want)
		}
	}
}

// TestStatusValue pins what reaches `val`: the enum token, never a label.
func TestStatusValue(t *testing.T) {
	b, _ := buildTestBridge(t)
	dev := b.devices[0]
	dev.app.ApplyValues([]map[string]any{{"uid": 0x1002, "value": 3}})
	e, _ := dev.app.Entity(0x1002)
	if got := statusValue(e); got != "Run" {
		t.Errorf("statusValue enum = %v", got)
	}
	unset, _ := dev.app.Entity(0x1005)
	if got := statusValue(unset); got != nil {
		t.Errorf("statusValue without a value = %v, want nil (an empty retained payload)", got)
	}
}

func TestNewValidations(t *testing.T) {
	stub := newStubMQTT()
	if _, err := New(Deps{Config: nil, MQTT: stub}); err == nil {
		t.Error("expected error for nil config")
	}
	if _, err := New(Deps{Config: testCfg(), MQTT: nil}); err == nil {
		t.Error("expected error for nil mqtt")
	}
	// A nil plane is refused at construction rather than at the first
	// publish: since ADR 0070 phase 7 step 5 the state plane is the only
	// way a device worker reaches the broker, and a daemon that built
	// without one would run silently and mirror nothing.
	// The REASON is asserted, not merely that something failed: this call
	// is also missing its devices, so a New that stopped checking the
	// plane would still error and still pass a bare err != nil test. The
	// daemon would then build without a state plane and mirror nothing.
	if _, err := New(Deps{Config: testCfg(), MQTT: stub}); err == nil ||
		!strings.Contains(err.Error(), "plane") {
		t.Errorf("New without a plane = %v, want an error naming the plane", err)
	}
	if _, err := New(Deps{Config: testCfg(), MQTT: stub, Plane: testPlane(stub)}); err == nil {
		t.Error("expected error for no devices")
	}
}

func TestTLSDeviceBuilds(t *testing.T) {
	// A TLS device builds (so AES siblings still run); it only fails at
	// connect with ErrTLSPSKUnsupported unless built with the tlspsk tag.
	stub := newStubMQTT()
	b, err := New(Deps{
		Config: testCfg(), MQTT: stub, Plane: testPlane(stub),
		Devices: []DeviceSpec{{
			Config:      profile.DeviceConfig{Name: "old", HaID: haIDFor("old"), Host: "h", ConnectionType: profile.ConnectionTLS, PSK64: b64(32)},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("TLS device should build, got %v", err)
	}
	if len(b.devices) != 1 {
		t.Errorf("expected 1 device, got %d", len(b.devices))
	}
}

func TestBridgeRunStopsOnCancel(t *testing.T) {
	stub := newStubMQTT()
	b, err := New(Deps{
		Config: testCfg(),
		MQTT:   stub,
		Plane:  testPlane(stub),
		Devices: []DeviceSpec{{
			// 127.0.0.1:80 refuses fast, so the worker cycles into the
			// offline backoff path; cancel must end Run promptly.
			Config: profile.DeviceConfig{
				Name: "dishwasher", HaID: haIDFor("dishwasher"), Host: "127.0.0.1",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait until the worker has published a connection_state (it reached at
	// least the connecting/offline phase), then cancel.
	deadline := time.After(5 * time.Second)
	for stub.get(testStatus+"/connection_state") == "" {
		select {
		case <-deadline:
			t.Fatal("no connection_state publish before timeout")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestNewRejectsMissingHost(t *testing.T) {
	stub := newStubMQTT()
	_, err := New(Deps{
		Config: testCfg(), MQTT: stub, Plane: testPlane(stub),
		Devices: []DeviceSpec{{
			Config:      profile.DeviceConfig{Name: "x", HaID: haIDFor("x"), ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16)},
			Description: smallDescription(t),
		}},
	})
	if err == nil {
		t.Error("device without host should be rejected")
	}
}

// TestNewRefusesAnApplianceWithoutAnHaIDOrTwiceTheSame: the haId is every
// topic's device segment, so an appliance whose haId is unknown cannot be
// published at all, and two entries for one appliance would publish over
// each other and fight over one command route.
func TestNewRefusesAnApplianceWithoutAnHaIDOrTwiceTheSame(t *testing.T) {
	stub := newStubMQTT()
	spec := func(name, haID string) DeviceSpec {
		return DeviceSpec{
			Config: profile.DeviceConfig{
				Name: name, HaID: haID, Host: "h",
				ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
			},
			Description: smallDescription(t),
		}
	}
	_, err := New(Deps{Config: testCfg(), MQTT: stub, Plane: testPlane(stub), Devices: []DeviceSpec{spec("dw", "")}})
	if !errors.Is(err, profile.ErrNoHaID) {
		t.Errorf("an appliance without an haId: err = %v, want ErrNoHaID", err)
	}
	_, err = New(Deps{Config: testCfg(), MQTT: stub, Plane: testPlane(stub), Devices: []DeviceSpec{
		spec("dw", "SAME"), spec("dw2", "SAME"),
	}})
	if err == nil || !strings.Contains(err.Error(), "SAME") ||
		!strings.Contains(err.Error(), `"dw"`) || !strings.Contains(err.Error(), `"dw2"`) {
		t.Errorf("two entries for one appliance: err = %v, want a refusal naming the segment and both devices", err)
	}
	// The cached description's haId is the fallback hc-util provides.
	withDesc := spec("dw", "")
	withDesc.Description.HaID = "FROM-PROFILE"
	b, err := New(Deps{Config: testCfg(), MQTT: stub, Plane: testPlane(stub), Devices: []DeviceSpec{withDesc}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.devices[0].HaID(); got != "FROM-PROFILE" {
		t.Errorf("HaID = %q, want the description's", got)
	}
}

// oldInstall is a devices.yaml entry and description cache exactly as an
// installation from before 0.15.0 has them: no `haid`, a cache without
// HaID, at the path hc-util parse wrote and printed.
func oldInstall(t *testing.T, name, path string) DeviceSpec {
	t.Helper()
	return DeviceSpec{
		Config: profile.DeviceConfig{
			Name: name, Host: "h", Description: path,
			ConnectionType: profile.ConnectionAES, PSK64: b64(32), IV64: b64(16),
		},
		Description: smallDescription(t), // no HaID: the field did not exist
	}
}

// TestAnExistingInstallationStartsAfterTheUpgrade is the upgrade promise:
// an installation from before 0.15.0 changes nothing and still starts. The
// segment comes from the description's file name, which hc-util parse has
// always made <haId>.json, so its topics land under the real haId; a
// renamed file starts too, under whatever it is called, and both are said
// at warn with the remedy. Two entries that resolve to one segment are
// refused, naming both.
func TestAnExistingInstallationStartsAfterTheUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"the file hc-util wrote", "./profiles/0102030405.json", "0102030405"},
		{"a renamed file", "/share/homeconnect/dishwasher.json", "dishwasher"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			stub := newStubMQTT()
			b, err := New(Deps{
				Config: testCfg(), MQTT: stub, Plane: testPlane(stub),
				Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
				Devices: []DeviceSpec{oldInstall(t, "Geschirrspüler", tc.path)},
			})
			if err != nil {
				t.Fatalf("an existing installation refused to start: %v", err)
			}
			dev := b.devices[0]
			if dev.HaID() != tc.want {
				t.Errorf("segment = %q, want %q", dev.HaID(), tc.want)
			}
			if got, want := dev.topics.Online(), "homeconnect/status/"+tc.want+"/online"; got != want {
				t.Errorf("online = %q, want %q", got, want)
			}
			line := logs.String()
			for _, s := range []string{"level=WARN", "bridge.haid_from_description_filename", "Geschirrspüler", "haid=" + tc.want, "hc-util parse"} {
				if !strings.Contains(line, s) {
					t.Errorf("the warning lacks %q: %s", s, line)
				}
			}
			// The sweep still finds the old tree, which is keyed by the NAME,
			// and reads the new one back under the resolved segment.
			plan := b.legacySweepPlan()
			if !slices.Contains(plan.filters, "homeconnect/Geschirrspüler/#") ||
				!slices.Contains(plan.filters, "homeconnect/status/"+tc.want+"/#") {
				t.Errorf("sweep windows = %v", plan.filters)
			}
			if !plan.clears("homeconnect/Geschirrspüler/availability") ||
				plan.clears("homeconnect/status/"+tc.want+"/online") {
				t.Error("the sweep does not keep the old and the new tree of this install apart")
			}
		})
	}

	stub := newStubMQTT()
	_, err := New(Deps{Config: testCfg(), MQTT: stub, Plane: testPlane(stub), Devices: []DeviceSpec{
		oldInstall(t, "Spüler oben", "/a/dishwasher.json"),
		oldInstall(t, "Spüler unten", "/b/dishwasher.json"),
	}})
	if err == nil || !strings.Contains(err.Error(), "Spüler oben") || !strings.Contains(err.Error(), "Spüler unten") {
		t.Errorf("two descriptions of one file name: err = %v, want a refusal naming both devices", err)
	}
}
