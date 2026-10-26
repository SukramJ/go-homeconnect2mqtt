// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-homeconnect2mqtt/internal/config"
)

// mustClientConfig is mqttClientConfig for a config the test expects to be
// accepted.
func mustClientConfig(t *testing.T, cfg *config.Config, will publisher.Will) mqtt.TCPConfig {
	t.Helper()
	got, err := mqttClientConfig(cfg, will, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("mqttClientConfig: %v", err)
	}
	return got
}

// writeTestCA writes a self-signed CA certificate as PEM and returns its path.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	return path
}

func tlsTestConfig(server, ca string, insecure bool) *config.Config {
	return &config.Config{
		MQTTServer: server, MQTTTopic: "homeconnect", HASSBaseTopic: "homeassistant", MQTTQoS: new(1),
		MQTTTLSCA: ca, MQTTTLSInsecure: insecure,
	}
}

// TestMQTTClientConfigTLS: the scheme of MQTT_SERVER selects TLS, and
// MQTT_TLS_CA / MQTT_TLS_INSECURE reach the tls.Config the client is
// handed, verified for the broker's HOSTNAME (never the whole URL).
func TestMQTTClientConfigTLS(t *testing.T) {
	t.Parallel()
	ca := writeTestCA(t)
	for _, tc := range []struct {
		name, server, ca string
		insecure         bool
		wantTLS, wantCA  bool
		wantInsecure     bool
		wantWarn         string
	}{
		{name: "plaintext", server: "tcp://broker:1883"},
		{name: "plaintext ignores the CA", server: "tcp://broker:1883", ca: ca, wantWarn: "config.mqtt_tls_ignored"},
		{name: "plaintext ignores insecure", server: "mqtt://broker", insecure: true, wantWarn: "config.mqtt_tls_ignored"},
		{name: "system roots", server: "ssl://broker.example:8883", wantTLS: true},
		{name: "private CA", server: "ssl://broker.example:8883", ca: ca, wantTLS: true, wantCA: true},
		{name: "tls scheme", server: "tls://broker.example", ca: ca, wantTLS: true, wantCA: true},
		{name: "mqtts scheme insecure", server: "mqtts://broker.example:8883", insecure: true, wantTLS: true, wantInsecure: true, wantWarn: "config.mqtt_tls_insecure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			cfg := tlsTestConfig(tc.server, tc.ca, tc.insecure)
			got, err := mqttClientConfig(cfg, testWill(t, cfg), slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatalf("mqttClientConfig: %v", err)
			}
			if (got.TLSConfig != nil) != tc.wantTLS {
				t.Fatalf("TLSConfig = %v, want TLS %v", got.TLSConfig, tc.wantTLS)
			}
			if tc.wantTLS {
				if got.TLSConfig.ServerName != "broker.example" {
					t.Errorf("ServerName = %q, want the hostname broker.example", got.TLSConfig.ServerName)
				}
				if (got.TLSConfig.RootCAs != nil) != tc.wantCA {
					t.Errorf("RootCAs set = %v, want %v", got.TLSConfig.RootCAs != nil, tc.wantCA)
				}
				if got.TLSConfig.InsecureSkipVerify != tc.wantInsecure {
					t.Errorf("InsecureSkipVerify = %v, want %v", got.TLSConfig.InsecureSkipVerify, tc.wantInsecure)
				}
			}
			if tc.wantWarn == "" {
				if strings.Contains(logs.String(), "level=WARN") {
					t.Errorf("unexpected warning: %s", logs.String())
				}
			} else if !strings.Contains(logs.String(), tc.wantWarn) {
				t.Errorf("log = %q, want a %s warning", logs.String(), tc.wantWarn)
			}
		})
	}
}

// TestMQTTClientConfigRefusesAnUnusableCA: a CA file that cannot be used
// stops the daemon at start with an error naming the file, instead of a
// reconnect loop that never says why.
func TestMQTTClientConfigRefusesAnUnusableCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.crt"), garbage} {
		cfg := tlsTestConfig("ssl://broker:8883", path, false)
		_, err := mqttClientConfig(cfg, testWill(t, cfg), slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), filepath.Base(path)) || !strings.Contains(err.Error(), "MQTT_TLS_CA") {
			t.Errorf("CA %s: err = %v, want an MQTT_TLS_CA error naming the file", path, err)
		}
	}
}
