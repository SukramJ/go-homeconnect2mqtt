// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ddShort = `<?xml version="1.0"?><device>
  <description><type>Dishwasher</type><brand>BOSCH</brand><model>SMV6</model><version>2</version></description>
  <settingList uid="0003"><setting access="readWrite" available="true" refCID="01" uid="1005"/></settingList>
</device>`

const fmShort = `<featureMappingFile><featureDescription>
  <feature refUID="1005">BSH.Common.Setting.PowerState</feature>
</featureDescription></featureMappingFile>`

const profJSON = `{"haId":"0102030405","deviceDescriptionFileName":"0102030405_DeviceDescription.xml","featureMappingFileName":"0102030405_FeatureMapping.xml","connectionType":"AES","key":"c2VjcmV0","iv":"aXY","brand":"BOSCH","type":"Dishwasher"}`

func writeZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"0102030405.json":                  profJSON,
		"0102030405_DeviceDescription.xml": ddShort,
		"0102030405_FeatureMapping.xml":    fmShort,
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
}

func TestParseCmd(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "profile.zip")
	writeZip(t, zipPath)
	out := filepath.Join(dir, "profiles")

	var stdout, stderr bytes.Buffer
	if err := parseCmd([]string{"--out", out, zipPath}, &stdout, &stderr); err != nil {
		t.Fatalf("parseCmd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "0102030405.json")); err != nil {
		t.Errorf("description not written: %v", err)
	}
	s := stdout.String()
	if !strings.Contains(s, "connection_type: AES") || !strings.Contains(s, "description:") {
		t.Errorf("devices snippet incomplete:\n%s", s)
	}
}

func TestSafeProfileFileName(t *testing.T) {
	cases := []struct {
		haID string
		want bool
	}{
		{"0102030405", true},
		{"BOSCH-Dishwasher-01", true},
		{"a.b_c-d", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../evil", false},
		{"a/b", false},
		{"/etc/pwn", false},
	}
	for _, tc := range cases {
		if got := safeProfileFileName(tc.haID); got != tc.want {
			t.Errorf("safeProfileFileName(%q) = %v, want %v", tc.haID, got, tc.want)
		}
	}
}

func TestParseCmdContainsTraversalHaID(t *testing.T) {
	// A crafted haId of "../evil" must neither escape --out nor appear in the
	// snippet; the sibling valid profile is still parsed.
	badJSON := `{"haId":"../evil","deviceDescriptionFileName":"0102030405_DeviceDescription.xml",` +
		`"featureMappingFileName":"0102030405_FeatureMapping.xml","connectionType":"AES","key":"c2VjcmV0","iv":"aXY"}`
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "profile.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"0102030405.json":                  profJSON,
		"bad.json":                         badJSON,
		"0102030405_DeviceDescription.xml": ddShort,
		"0102030405_FeatureMapping.xml":    fmShort,
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
	_ = f.Close()

	out := filepath.Join(dir, "profiles")
	var stdout, stderr bytes.Buffer
	if err := parseCmd([]string{"--out", out, zipPath}, &stdout, &stderr); err != nil {
		t.Fatalf("parseCmd: %v", err)
	}
	// filepath.Join(out, "../evil.json") would land here if not rejected.
	if _, err := os.Stat(filepath.Join(dir, "evil.json")); err == nil {
		t.Error("traversal haId escaped the --out directory")
	}
	if _, err := os.Stat(filepath.Join(out, "0102030405.json")); err != nil {
		t.Errorf("valid profile not written: %v", err)
	}
	if strings.Contains(stdout.String(), "evil") {
		t.Errorf("crafted haId leaked into the snippet:\n%s", stdout.String())
	}
}

func TestParseCmdMissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := parseCmd(nil, &stdout, &stderr); err == nil {
		t.Error("expected error for missing zip")
	}
}

func TestDumpCmd(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "profile.zip")
	writeZip(t, zipPath)
	out := filepath.Join(dir, "profiles")
	var b1, b2 bytes.Buffer
	if err := parseCmd([]string{"--out", out, zipPath}, &b1, &b2); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := dumpCmd([]string{filepath.Join(out, "0102030405.json")}, &stdout, &stderr); err != nil {
		t.Fatalf("dumpCmd: %v", err)
	}
	if !strings.Contains(stdout.String(), "BSH.Common.Setting.PowerState") {
		t.Errorf("dump missing feature:\n%s", stdout.String())
	}
}

func TestDumpCmdMissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := dumpCmd(nil, &stdout, &stderr); err == nil {
		t.Error("expected error for missing path")
	}
}

func TestConnTestMissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := connTestCmd(nil, &stdout, &stderr); err == nil {
		t.Error("expected error for missing devices file")
	}
}

func TestConnTestReportsFailure(t *testing.T) {
	dir := t.TempDir()
	devPath := filepath.Join(dir, "devices.yaml")
	// 127.0.0.1 refuses on :80 quickly, so the connect fails fast.
	content := "devices:\n  - name: dw\n    host: 127.0.0.1\n    connection_type: AES\n    psk64: c2VjcmV0\n    iv64: aXY\n    description: x.json\n"
	_ = os.WriteFile(devPath, []byte(content), 0o600)
	var stdout, stderr bytes.Buffer
	err := connTestCmd([]string{devPath}, &stdout, &stderr)
	if err == nil {
		t.Error("expected failure summary for unreachable device")
	}
	if !strings.Contains(stdout.String(), "✗ dw") || !strings.Contains(stdout.String(), "hint:") {
		t.Errorf("expected failure + hint in output:\n%s", stdout.String())
	}
}
