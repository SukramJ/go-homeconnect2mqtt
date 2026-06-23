// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package profile

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
)

// Categorized onboarding errors (docs/03-profil-format.md §7). They are
// sentinels callers can match with errors.Is to drive the manual-IP
// escalation (FK-7).
var (
	ErrInvalidProfile        = errors.New("invalid_profile_file")
	ErrParser                = errors.New("profile_file_parser_error")
	ErrApplianceNotInProfile = errors.New("appliance_not_in_profile_file")
)

// ProfileJSON is the per-device index file from the archive
// (docs/03-profil-format.md §3).
type ProfileJSON struct {
	HaID                      string `json:"haId"`
	DeviceDescriptionFileName string `json:"deviceDescriptionFileName"`
	FeatureMappingFileName    string `json:"featureMappingFileName"`
	ConnectionType            string `json:"connectionType"`
	Key                       string `json:"key"`
	IV                        string `json:"iv"`
	Brand                     string `json:"brand"`
	Type                      string `json:"type"`
	Vib                       string `json:"vib"`
	Model                     string `json:"model"`
}

// DeviceProfile is a fully parsed appliance from the archive: its index
// fields plus the parsed description. Secret fields (PSK64/IV64) are kept
// for runtime but must never be logged or published (use Redact).
type DeviceProfile struct {
	HaID           string
	ConnectionType ConnectionType
	PSK64          string
	IV64           string
	Brand          string
	Type           string
	Vib            string
	Model          string
	Description    *Description
}

// ConnectionType is the transport mode; duplicated here (the homeconnect
// package owns the canonical type) to avoid an import cycle.
type ConnectionType string

// Connection type values.
const (
	ConnectionAES ConnectionType = "AES"
	ConnectionTLS ConnectionType = "TLS"
)

// DefaultHost returns the mDNS-resolvable default host name for the
// appliance (docs/03 §3): the haId for AES, brand-type-haId for TLS.
func (p *DeviceProfile) DefaultHost() string {
	if p.ConnectionType == ConnectionTLS {
		return fmt.Sprintf("%s-%s-%s", p.Brand, p.Type, p.HaID)
	}
	return p.HaID
}

// ParseArchiveFile reads a profile ZIP from disk and parses every device.
func ParseArchiveFile(zipPath string, logger *slog.Logger) ([]*DeviceProfile, error) {
	data, err := os.ReadFile(zipPath) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	return ParseArchiveBytes(data, logger)
}

// ParseArchiveBytes parses an in-memory profile ZIP. It finds every *.json
// index, resolves the referenced XML files explicitly (not by naming
// convention, docs/03 §2) and parses each device.
func ParseArchiveBytes(data []byte, logger *slog.Logger) ([]*DeviceProfile, error) {
	if logger == nil {
		logger = slog.Default()
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: open %s: %v", ErrInvalidProfile, f.Name, err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %v", ErrInvalidProfile, f.Name, err)
		}
		files[f.Name] = b
	}
	return parseFileSet(files, logger)
}

// parseFileSet builds device profiles from a name->bytes map (shared by
// the ZIP and loose-file paths).
func parseFileSet(files map[string][]byte, logger *slog.Logger) ([]*DeviceProfile, error) {
	var profiles []*DeviceProfile
	var jsonNames []string
	for name := range files {
		if strings.HasSuffix(strings.ToLower(name), ".json") {
			jsonNames = append(jsonNames, name)
		}
	}
	if len(jsonNames) == 0 {
		return nil, fmt.Errorf("%w: no .json index in archive", ErrInvalidProfile)
	}
	for _, name := range jsonNames {
		var pj ProfileJSON
		if err := json.Unmarshal(files[name], &pj); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidProfile, name, err)
		}
		descXML, ok := lookupFile(files, pj.DeviceDescriptionFileName, name)
		if !ok {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalidProfile, pj.DeviceDescriptionFileName)
		}
		fmXML, ok := lookupFile(files, pj.FeatureMappingFileName, name)
		if !ok {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalidProfile, pj.FeatureMappingFileName)
		}
		desc, err := ParseDescription(descXML, fmXML, logger)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrParser, err)
		}
		profiles = append(profiles, &DeviceProfile{
			HaID:           pj.HaID,
			ConnectionType: ConnectionType(strings.ToUpper(pj.ConnectionType)),
			PSK64:          pj.Key,
			IV64:           pj.IV,
			Brand:          pj.Brand,
			Type:           pj.Type,
			Vib:            pj.Vib,
			Model:          pj.Model,
			Description:    desc,
		})
	}
	return profiles, nil
}

// lookupFile resolves a referenced file name within the archive, trying
// the exact name, the base name and the path relative to the index file.
func lookupFile(files map[string][]byte, ref, indexName string) ([]byte, bool) {
	if ref == "" {
		return nil, false
	}
	if b, ok := files[ref]; ok {
		return b, true
	}
	base := path.Base(ref)
	if b, ok := files[base]; ok {
		return b, true
	}
	if dir := path.Dir(indexName); dir != "." {
		if b, ok := files[path.Join(dir, base)]; ok {
			return b, true
		}
	}
	// Last resort: match by base name anywhere in the archive.
	for name, b := range files {
		if path.Base(name) == base {
			return b, true
		}
	}
	return nil, false
}
