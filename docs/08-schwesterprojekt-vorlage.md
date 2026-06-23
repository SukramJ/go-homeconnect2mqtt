# Schwesterprojekt-Vorlage (verbatim aus `go-mtec2mqtt`)

> Die wiederverwendbaren Schlüsseldateien des Schwesterprojekts `../go-mtec2mqtt`, hier
> **verbatim** gesichert, damit `go-homeconnect2mqtt` auch auf einer Umgebung **ohne**
> go-mtec2mqtt aufgesetzt werden kann. Zwei Kategorien:
>
> - **Direkt wiederverwendbar** (domänen-neutral): MQTT-Client (`internal/mqtt/*`),
>   Config-Loader-Engine (`config/load.go`), die Infrastruktur (Makefile/golangci/Docker/
>   version/githook). Nur Modul-Pfad & ein paar Konstanten anpassen.
> - **Vorlage** (M-TEC-spezifisch, Felder ersetzen): `config/config.go`, `validate.go`,
>   `defaults.go`, `CLAUDE.md`.

## 0. Globale Anpassungen beim Übernehmen

| Suchen | Ersetzen |
|---|---|
| `github.com/SukramJ/go-mtec2mqtt` | `github.com/SukramJ/go-homeconnect2mqtt` |
| `go-mtec2mqtt` (Binärname, Image) | `go-homeconnect2mqtt` |
| `mtec2mqtt` / `mtec-util` (cmd) | `homeconnect2mqtt` / `hc-util` |
| `MTEC_` (Env-Prefix) | `HC2M_` |
| `aiomtec2mqtt` (App-Dir) | `homeconnect2mqtt` |
| `M-TEC-MQTT` (ClientID) | `homeconnect2mqtt` |
| Modbus-/Register-/Coordinator-Pakete | homeconnect/profile/bridge (siehe `06-architektur-konzept.md`) |

Der MQTT-Code stammt aus `SukramJ/openccu-loom` (MIT) — die Copyright-Header (`OpenCCU-Loom
authors` / `SukramJ`) beim Kopieren erhalten.

---

## 1. Infrastruktur

### 1.1 `Makefile`

```makefile
# SPDX-License-Identifier: MIT
# go-mtec2mqtt — developer Makefile
#
# Tabs are required by GNU make. The whitespace rules below pin sane
# shell behaviour so a failing recipe step actually aborts the target
# instead of silently moving on.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

GO            ?= go
GOFUMPT       ?= gofumpt
GOIMPORTS     ?= goimports
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK   ?= govulncheck
GOLICENSES    ?= go-licenses
DOCKER        ?= docker

export CGO_ENABLED := 0

BIN_DIR  := bin
MODULE   := github.com/SukramJ/go-mtec2mqtt
PKG_VER  := $(MODULE)/internal/version

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG_VER).Version=$(VERSION) \
	-X $(PKG_VER).Commit=$(COMMIT) \
	-X $(PKG_VER).BuildDate=$(BUILD_DATE)

GO_BUILD_FLAGS := -trimpath -ldflags="$(LDFLAGS)"

DOCKER_IMAGE ?= go-mtec2mqtt
DOCKER_TAG   ?= $(VERSION)

DIST_DIR         := dist
RELEASE_TARGETS  ?= linux/amd64 linux/arm64 darwin/arm64
RELEASE_VERSION  ?= $(shell awk -F'"' '/^[[:space:]]*Version = /{print $$2; exit}' internal/version/version.go)
RELEASE_PAYLOAD  := registers.yaml config-template.yaml README.md LICENSE changelog.md

.PHONY: help
help: ## show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: setup
setup: hooks ## install developer tooling + git hooks
	$(GO) install mvdan.cc/gofumpt@latest
	$(GO) install golang.org/x/tools/cmd/goimports@latest
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GO) install github.com/google/go-licenses@latest

.PHONY: hooks
hooks: ## point git at the tracked hooks in .githooks/ (blocks direct commits on main)
	@git config core.hooksPath .githooks
	@echo "git core.hooksPath -> .githooks (direct commits on main/master are now blocked)"

.PHONY: build
build: build-daemon build-util ## build both binaries into bin/

.PHONY: build-daemon
build-daemon: ## build the daemon
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/mtec2mqtt ./cmd/mtec2mqtt

.PHONY: build-util
build-util: ## build the interactive CLI
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/mtec-util ./cmd/mtec-util

.PHONY: install
install: ## go install both binaries
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/mtec2mqtt
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/mtec-util

.PHONY: test
test: ## run the full test suite with race detector
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=60s ./...

.PHONY: test-cover
test-cover: ## run tests + coverage report
	CGO_ENABLED=1 $(GO) test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -20

.PHONY: vet
vet: ## run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## format with gofumpt + goimports (writes in place)
	$(GOFUMPT) -w .
	$(GOIMPORTS) -w -local $(MODULE) .

.PHONY: fmt-check
fmt-check: ## fail when sources are not gofumpt-clean
	@diff=$$($(GOFUMPT) -l .); \
	if [ -n "$$diff" ]; then \
	  echo "gofumpt would rewrite:"; echo "$$diff"; exit 1; \
	fi

.PHONY: lint
lint: ## run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: vuln
vuln: ## scan dependencies for known vulnerabilities (govulncheck)
	$(GOVULNCHECK) ./...

.PHONY: licenses
licenses: ## fail on copyleft dependency licenses
	$(GOLICENSES) check ./... --disallowed_types=forbidden,restricted,reciprocal

.PHONY: tidy
tidy: ## sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: vet fmt-check lint test ## the pre-commit / pre-push gate

.PHONY: run
run: build-daemon ## run the daemon
	$(BIN_DIR)/mtec2mqtt --config ./config.yaml --registers ./registers.yaml

.PHONY: clean
clean: ## remove build artefacts
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out

.PHONY: release
release: ## stage cross-compiled release archives into dist/ (no upload)
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@echo "release version: $(RELEASE_VERSION)"
	@version="$(RELEASE_VERSION)"; \
	commit="$$(git rev-parse --short HEAD 2>/dev/null || echo none)"; \
	build_date="$$(date -u +%Y-%m-%dT%H:%M:%SZ)"; \
	ldflags="-s -w \
	  -X $(PKG_VER).Version=$$version \
	  -X $(PKG_VER).Commit=$$commit \
	  -X $(PKG_VER).BuildDate=$$build_date"; \
	for tgt in $(RELEASE_TARGETS); do \
	  goos=$${tgt%/*}; goarch=$${tgt#*/}; \
	  stage="$(DIST_DIR)/go-mtec2mqtt-$$version-$$goos-$$goarch"; \
	  mkdir -p "$$stage"; \
	  echo "==> $$goos/$$goarch -> $$stage"; \
	  GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags="$$ldflags" \
	    -o "$$stage/mtec2mqtt" ./cmd/mtec2mqtt; \
	  GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags="$$ldflags" \
	    -o "$$stage/mtec-util" ./cmd/mtec-util; \
	  cp $(RELEASE_PAYLOAD) "$$stage/"; \
	  ( cd $(DIST_DIR) && tar -czf "$$(basename $$stage).tar.gz" "$$(basename $$stage)" ); \
	  rm -rf "$$stage"; \
	done
	@cd $(DIST_DIR) && shasum -a 256 *.tar.gz > SHA256SUMS
	@ls -lh $(DIST_DIR)

.PHONY: docker
docker: ## build a tagged container image
	$(DOCKER) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(DOCKER_IMAGE):$(DOCKER_TAG) \
	  -t $(DOCKER_IMAGE):latest .

.PHONY: version
version: ## print the resolved build metadata
	@echo "VERSION    = $(VERSION)"
	@echo "COMMIT     = $(COMMIT)"
	@echo "BUILD_DATE = $(BUILD_DATE)"
```

> Anpassen: `MODULE`, Binärnamen (`mtec2mqtt`/`mtec-util` → `homeconnect2mqtt`/`hc-util`),
> `DOCKER_IMAGE`, `RELEASE_PAYLOAD` (statt `registers.yaml` → `devices.yaml`/`mapping.yaml`).

### 1.2 `.golangci.yaml` (direkt übernehmbar; nur `local-prefixes` anpassen)

```yaml
version: "2"
run:
  go: "1.26"
  tests: true
  allow-parallel-runners: true
linters:
  default: none
  enable:
    - bodyclose
    - contextcheck
    - copyloopvar
    - errcheck
    - errorlint
    - exhaustive
    - gocritic
    - gosec
    - govet
    - intrange
    - makezero
    - nilerr
    - noctx
    - prealloc
    - reassign
    - revive
    - sloglint
    - staticcheck
    - thelper
    - tparallel
    - unconvert
    - unparam
    - unused
    - usestdlibvars
    - wastedassign
  settings:
    errcheck:
      check-type-assertions: true
    exhaustive:
      default-signifies-exhaustive: true
    gocritic:
      disabled-checks:
        - ifElseChain
        - hugeParam
      enabled-tags:
        - diagnostic
        - performance
        - style
        - opinionated
    gosec:
      excludes:
        - G104
    sloglint:
      no-mixed-args: true
      static-msg: false
    revive:
      severity: warning
      rules:
        - name: blank-imports
        - name: context-as-argument
        - name: context-keys-type
        - name: dot-imports
        - name: error-return
        - name: error-strings
        - name: error-naming
        - name: exported
        - name: if-return
        - name: increment-decrement
        - name: indent-error-flow
        - name: package-comments
        - name: range
        - name: receiver-naming
        - name: redefines-builtin-id
        - name: superfluous-else
        - name: time-naming
        - name: unexported-return
        - name: unreachable-code
        - name: var-declaration
        - name: var-naming
  exclusions:
    generated: lax
    rules:
      - linters: [contextcheck, errcheck, gosec, noctx, unparam]
        path: _test\.go
      - linters: [gosec]
        path: cmd/
    paths: [third_party$, builtin$, examples$]
issues:
  max-issues-per-linter: 0
  max-same-issues: 0
formatters:
  enable: [gofumpt, goimports]
  settings:
    gofumpt:
      extra-rules: true
    goimports:
      local-prefixes: [github.com/SukramJ/go-mtec2mqtt]
  exclusions:
    generated: lax
    paths: [third_party$, builtin$, examples$]
```

### 1.3 `Dockerfile` (Multi-Stage, distroless)

```dockerfile
# SPDX-License-Identifier: MIT
# Multi-stage build. CGO disabled → static binary → distroless runtime.
# Operator-editable assets (registers.yaml → bei uns devices.yaml/mapping.yaml)
# liegen NEBEN dem Binary, nicht go:embed-ed.

# ---------- Stage 1: build ----------
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-mtec2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-mtec2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-mtec2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/mtec2mqtt ./cmd/mtec2mqtt && \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-mtec2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-mtec2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-mtec2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/mtec-util ./cmd/mtec-util

# ---------- Stage 2: runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/mtec2mqtt /out/mtec-util /app/
COPY --from=builder /src/registers.yaml /src/config-template.yaml /app/
VOLUME ["/config"]
ENV XDG_CONFIG_HOME=/config
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/mtec2mqtt"]
```

> ⚠️ Falls **TLS-PSK** über eine cgo-Lib gelöst wird (siehe `01-protokoll.md` §4), kann
> `CGO_ENABLED=0` für diesen Pfad nicht gelten → separates Build-Target/Image vorsehen.

### 1.4 `.dockerignore`

```
.git
.github
.idea
.vscode
bin/
coverage.out
*.out
*.test
README.md
Makefile
Dockerfile
.dockerignore
.gitignore
```

### 1.5 `.gitignore`

```
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
coverage.*
*.coverprofile
profile.cov
go.work
go.work.sum
/bin/
.env
/.idea
config.yaml
/dist/
```

### 1.6 `internal/version/version.go`

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package version exposes build metadata injected at link time via -ldflags.
package version

var (
	// Version is the human-readable release tag (e.g. "1.0.0").
	Version = "0.1.0"
	// Commit is the short Git SHA the binary was built from.
	Commit = "none"
	// BuildDate is the UTC RFC3339 build timestamp.
	BuildDate = "unknown"
)

// String returns a compact one-line build banner.
func String() string {
	return "go-homeconnect2mqtt " + Version + " (commit " + Commit + ", built " + BuildDate + ")"
}
```

### 1.7 `.githooks/pre-commit` (blockt Direkt-Commits auf main)

```sh
#!/usr/bin/env sh
# SPDX-License-Identifier: MIT
# Pre-commit hook: refuse direct commits on protected branches.
# Override once: ALLOW_MAIN_COMMIT=1 git commit ...   (or --no-verify)
# Enable: git config core.hooksPath .githooks   (via `make setup`/`make hooks`)

protected="main master"
branch="$(git symbolic-ref --short -q HEAD)" || branch=""
for p in $protected; do
	if [ "$branch" = "$p" ]; then
		if [ "${ALLOW_MAIN_COMMIT:-0}" = "1" ]; then
			exit 0
		fi
		printf '\033[31m✗ Direct commits to "%s" are blocked.\033[0m\n' "$branch" >&2
		printf '  Create a feature branch and open a PR:\n' >&2
		printf '      git switch -c feature/my-change\n' >&2
		printf '  Override once: ALLOW_MAIN_COMMIT=1 git commit ...   (or --no-verify)\n' >&2
		exit 1
	fi
done
exit 0
```

---

## 2. MQTT-Client (`internal/mqtt/`) — direkt wiederverwendbar

Domänen-neutraler Pure-Go-MQTT-3.1.1-Client + reconnectender Lifecycle. **1:1 übernehmbar**
(nur `import`-Pfad des `protocol`-Pakets anpassen). Quelle: `SukramJ/openccu-loom` (MIT).

### 2.1 `internal/mqtt/protocol/doc.go`

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 OpenCCU-Loom authors.

// Package protocol implements the subset of MQTT 3.1.1 that the bridge
// needs — CONNECT / PUBLISH / SUBSCRIBE / PINGREQ plus matching inbound
// frames. Pure-Go, zero dependencies, so the daemon stays CGo-free.
//
// Coverage: MQTT 3.1.1 (level 0x04); CONNECT with optional will (LWT) +
// username/password; PUBLISH QoS 0/1 (PUBACK tracked); SUBSCRIBE /
// UNSUBSCRIBE (one filter/frame); PINGREQ/PINGRESP; DISCONNECT.
// QoS 2 is rejected at Publish time.
package protocol
```

### 2.2 `internal/mqtt/protocol/codec.go`

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 OpenCCU-Loom authors.

package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// PacketType is the fixed-header packet type (4 high bits of byte 1).
type PacketType byte

// PacketType values.
const (
	PacketConnect     PacketType = 1
	PacketConnack     PacketType = 2
	PacketPublish     PacketType = 3
	PacketPuback      PacketType = 4
	PacketSubscribe   PacketType = 8
	PacketSuback      PacketType = 9
	PacketUnsubscribe PacketType = 10
	PacketUnsuback    PacketType = 11
	PacketPingreq     PacketType = 12
	PacketPingresp    PacketType = 13
	PacketDisconnect  PacketType = 14
)

// ConnectPacket is the outbound CONNECT.
type ConnectPacket struct {
	ClientID     string
	KeepAlive    uint16 // seconds
	Username     string
	Password     string
	CleanSession bool
	WillTopic    string
	WillPayload  []byte
	WillRetain   bool
	WillQoS      byte
}

// Encode writes the packet to w.
func (p *ConnectPacket) Encode(w io.Writer) error {
	var payload bytes.Buffer
	writeString(&payload, "MQTT")
	payload.WriteByte(4) // level 4 = 3.1.1

	var flags byte
	if p.CleanSession {
		flags |= 0x02
	}
	if p.WillTopic != "" {
		flags |= 0x04
		flags |= (p.WillQoS & 0x03) << 3
		if p.WillRetain {
			flags |= 0x20
		}
	}
	if p.Password != "" {
		flags |= 0x40
	}
	if p.Username != "" {
		flags |= 0x80
	}
	payload.WriteByte(flags)

	_ = binary.Write(&payload, binary.BigEndian, p.KeepAlive)
	writeString(&payload, p.ClientID)
	if p.WillTopic != "" {
		writeString(&payload, p.WillTopic)
		writeBytes(&payload, p.WillPayload)
	}
	if p.Username != "" {
		writeString(&payload, p.Username)
	}
	if p.Password != "" {
		writeString(&payload, p.Password)
	}
	return writePacket(w, byte(PacketConnect)<<4, payload.Bytes())
}

// ConnackPacket is the inbound CONNACK.
type ConnackPacket struct {
	SessionPresent bool
	ReturnCode     byte
}

// DecodeConnack parses a CONNACK payload (the 2-byte variable header).
func DecodeConnack(body []byte) (*ConnackPacket, error) {
	if len(body) < 2 {
		return nil, errors.New("connack: short body")
	}
	return &ConnackPacket{SessionPresent: body[0]&0x01 != 0, ReturnCode: body[1]}, nil
}

// PublishPacket is the outbound PUBLISH.
type PublishPacket struct {
	Topic    string
	Payload  []byte
	QoS      byte // 0 or 1
	Retain   bool
	Dup      bool
	PacketID uint16 // set for QoS > 0
}

// Encode writes the packet.
func (p *PublishPacket) Encode(w io.Writer) error {
	if p.QoS > 1 {
		return fmt.Errorf("publish: unsupported QoS %d", p.QoS)
	}
	head := byte(PacketPublish) << 4
	if p.Dup {
		head |= 0x08
	}
	head |= (p.QoS & 0x03) << 1
	if p.Retain {
		head |= 0x01
	}
	var body bytes.Buffer
	writeString(&body, p.Topic)
	if p.QoS > 0 {
		_ = binary.Write(&body, binary.BigEndian, p.PacketID)
	}
	body.Write(p.Payload)
	return writePacket(w, head, body.Bytes())
}

// PubackPacket is the inbound PUBACK.
type PubackPacket struct{ PacketID uint16 }

// DecodePuback parses a PUBACK.
func DecodePuback(body []byte) (*PubackPacket, error) {
	if len(body) < 2 {
		return nil, errors.New("puback: short body")
	}
	return &PubackPacket{PacketID: binary.BigEndian.Uint16(body[:2])}, nil
}

// SubscribePacket is the outbound SUBSCRIBE.
type SubscribePacket struct {
	PacketID    uint16
	TopicFilter string
	QoS         byte
}

// Encode writes the packet.
func (p *SubscribePacket) Encode(w io.Writer) error {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, p.PacketID)
	writeString(&body, p.TopicFilter)
	body.WriteByte(p.QoS & 0x03)
	return writePacket(w, byte(PacketSubscribe)<<4|0x02, body.Bytes())
}

// UnsubscribePacket is the outbound UNSUBSCRIBE.
type UnsubscribePacket struct {
	PacketID    uint16
	TopicFilter string
}

// Encode writes the packet.
func (p *UnsubscribePacket) Encode(w io.Writer) error {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, p.PacketID)
	writeString(&body, p.TopicFilter)
	return writePacket(w, byte(PacketUnsubscribe)<<4|0x02, body.Bytes())
}

// InboundPublish bundles an incoming PUBLISH as the client cares about it.
type InboundPublish struct {
	Topic    string
	Payload  []byte
	QoS      byte
	PacketID uint16
	Retain   bool
}

// DecodePublish parses a PUBLISH. header is the byte-1 value.
func DecodePublish(header byte, body []byte) (*InboundPublish, error) {
	qos := (header >> 1) & 0x03
	retain := header&0x01 != 0
	idx := 0
	topic, n, err := readString(body)
	if err != nil {
		return nil, err
	}
	idx += n
	var pktID uint16
	if qos > 0 {
		if idx+2 > len(body) {
			return nil, errors.New("publish: missing packet id")
		}
		pktID = binary.BigEndian.Uint16(body[idx : idx+2])
		idx += 2
	}
	return &InboundPublish{Topic: topic, Payload: body[idx:], QoS: qos, PacketID: pktID, Retain: retain}, nil
}

// EncodePingReq writes a PINGREQ.
func EncodePingReq(w io.Writer) error { return writePacket(w, byte(PacketPingreq)<<4, nil) }

// EncodeDisconnect writes DISCONNECT.
func EncodeDisconnect(w io.Writer) error { return writePacket(w, byte(PacketDisconnect)<<4, nil) }

// EncodePuback writes a PUBACK for id.
func EncodePuback(w io.Writer, id uint16) error {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, id)
	return writePacket(w, byte(PacketPuback)<<4, body)
}

// Frame is a decoded fixed-header + remaining bytes tuple.
type Frame struct {
	Header byte
	Body   []byte
}

// PacketType returns the packet type bits of the header.
func (f Frame) PacketType() PacketType { return PacketType(f.Header >> 4) }

// ReadFrame reads one MQTT packet from r.
func ReadFrame(r io.Reader) (Frame, error) {
	head := make([]byte, 1)
	if _, err := io.ReadFull(r, head); err != nil {
		return Frame{}, err
	}
	length, err := readRemainingLength(r)
	if err != nil {
		return Frame{}, err
	}
	body := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Header: head[0], Body: body}, nil
}

// --- helpers ---

func writePacket(w io.Writer, header byte, body []byte) error {
	if _, err := w.Write([]byte{header}); err != nil {
		return err
	}
	length := encodeRemainingLength(len(body))
	if _, err := w.Write(length); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	_, err := w.Write(body)
	return err
}

func encodeRemainingLength(n int) []byte {
	var out []byte
	for {
		digit := byte(n & 0x7F)
		n >>= 7
		if n > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
		if n == 0 {
			break
		}
	}
	return out
}

func readRemainingLength(r io.Reader) (int, error) {
	var mult uint32 = 1
	var length uint32
	buf := make([]byte, 1)
	for range 4 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		length += uint32(buf[0]&0x7F) * mult
		if buf[0]&0x80 == 0 {
			return int(length), nil
		}
		mult *= 128
	}
	return 0, errors.New("mqtt: malformed remaining length")
}

func writeString(w *bytes.Buffer, s string) {
	_ = binary.Write(w, binary.BigEndian, uint16(len(s)))
	w.WriteString(s)
}

func writeBytes(w *bytes.Buffer, b []byte) {
	_ = binary.Write(w, binary.BigEndian, uint16(len(b)))
	w.Write(b)
}

func readString(b []byte) (value string, bytesRead int, err error) {
	if len(b) < 2 {
		return "", 0, errors.New("mqtt: short string header")
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if len(b) < 2+n {
		return "", 0, errors.New("mqtt: short string body")
	}
	return string(b[2 : 2+n]), 2 + n, nil
}
```

### 2.3 `internal/mqtt/client.go` (Interfaces)

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 OpenCCU-Loom authors.

package mqtt

import "context"

// QoS mirrors the MQTT QoS enum.
type QoS byte

// QoS values.
const (
	QoS0 QoS = 0
	QoS1 QoS = 1
	QoS2 QoS = 2
)

// Publisher is the outbound contract the bridge publishes through.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte, qos QoS, retain bool) error
}

// MessageHandler is invoked for every message a subscription receives.
type MessageHandler func(topic string, payload []byte)

// Subscriber is the inbound contract.
type Subscriber interface {
	Subscribe(ctx context.Context, topicFilter string, qos QoS, handler MessageHandler) error
	Unsubscribe(ctx context.Context, topicFilter string) error
}

// Client is the combined role the Bridge uses.
type Client interface {
	Publisher
	Subscriber
}
```

### 2.4 `internal/mqtt/lifecycle.go` (Reconnect mit Backoff + OnConnect-Replay)

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 OpenCCU-Loom authors.

package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Connector is the narrow lifecycle contract a broker adapter must satisfy.
type Connector interface {
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
}

// LifecycleConfig governs the reconnect loop.
type LifecycleConfig struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Jitter         time.Duration
	Logger         *slog.Logger
}

// DefaultLifecycle returns 1s → 30s exponential backoff with ±500ms jitter.
func DefaultLifecycle() LifecycleConfig {
	return LifecycleConfig{
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		Jitter:         500 * time.Millisecond,
	}
}

// Lifecycle drives a [Connector] with automatic reconnect.
type Lifecycle struct {
	cfg       LifecycleConfig
	connector Connector

	mu        sync.Mutex
	started   bool
	cancel    context.CancelFunc
	onConnect []func(context.Context)
}

// NewLifecycle constructs a lifecycle around connector.
func NewLifecycle(cfg LifecycleConfig, connector Connector) *Lifecycle {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.InitialBackoff == 0 {
		cfg.InitialBackoff = DefaultLifecycle().InitialBackoff
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = DefaultLifecycle().MaxBackoff
	}
	return &Lifecycle{cfg: cfg, connector: connector}
}

// OnConnect registers a callback fired on every successful (re)connect.
func (l *Lifecycle) OnConnect(fn func(context.Context)) {
	l.mu.Lock()
	l.onConnect = append(l.onConnect, fn)
	l.mu.Unlock()
}

// Start boots the reconnect loop; returns once the first connect succeeds.
func (l *Lifecycle) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return errors.New("mqtt.lifecycle: already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.started = true
	l.mu.Unlock()

	if err := l.connectOnce(runCtx); err != nil {
		cancel()
		l.mu.Lock()
		l.started = false
		l.mu.Unlock()
		return err
	}
	go l.loop(runCtx)
	return nil
}

// Stop cancels the loop and disconnects the session.
func (l *Lifecycle) Stop(ctx context.Context) error {
	l.mu.Lock()
	cancel := l.cancel
	l.started = false
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if l.connector != nil {
		return l.connector.Disconnect(ctx)
	}
	return nil
}

func (l *Lifecycle) loop(ctx context.Context) {
	backoff := l.cfg.InitialBackoff
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.jittered(backoff)):
		}
		if err := l.connectOnce(ctx); err != nil {
			if isAlreadyConnectedErr(err) {
				backoff = l.cfg.MaxBackoff
				continue
			}
			l.cfg.Logger.Warn("mqtt.reconnect", slog.String("err", err.Error()))
			backoff *= 2
			if backoff > l.cfg.MaxBackoff {
				backoff = l.cfg.MaxBackoff
			}
			continue
		}
		backoff = l.cfg.InitialBackoff
	}
}

func isAlreadyConnectedErr(err error) bool {
	return err != nil && strings.HasSuffix(err.Error(), "already connected")
}

func (l *Lifecycle) connectOnce(ctx context.Context) error {
	if err := l.connector.Connect(ctx); err != nil {
		return err
	}
	l.mu.Lock()
	cbs := make([]func(context.Context), len(l.onConnect))
	copy(cbs, l.onConnect)
	l.mu.Unlock()
	for _, cb := range cbs {
		cb(ctx)
	}
	return nil
}

func (l *Lifecycle) jittered(d time.Duration) time.Duration {
	if l.cfg.Jitter <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(l.cfg.Jitter*2))) - l.cfg.Jitter //nolint:gosec // jitter only
	return d + delta
}
```

> Dieses Backoff+Jitter+Replay-Muster ist exakt das, was die Home-Connect-Geräteverbindung
> braucht (FK-1, `05-resilienz.md`) — die `Connector`/`OnConnect`-Struktur lässt sich 1:1
> auf den Geräte-Worker (`bridge/device.go`) übertragen.

### 2.5 `internal/mqtt/adapter_tcp.go` (TCP/TLS-Client, LWT, KeepAlive, Sub-Replay)

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package mqtt provides the MQTT transport: a TCP/TLS adapter,
// publish/subscribe plumbing, and a reconnecting lifecycle.
package mqtt

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SukramJ/go-mtec2mqtt/internal/mqtt/protocol"
)

// TCPConfig wires a [TCPClient] against a real broker.
type TCPConfig struct {
	BrokerURL    string // tcp://host:1883 or tls://host:8883
	ClientID     string
	Username     string
	Password     string
	KeepAlive    time.Duration // floor: 30s
	DialTimeout  time.Duration // default 10s
	AckTimeout   time.Duration // PUBACK wait, default 20s
	TLSConfig    *tls.Config
	WillTopic    string
	WillPayload  []byte
	WillRetain   bool
	CleanSession bool
	Logger       *slog.Logger
}

// TCPClient is a pure-Go MQTT 3.1.1 client implementing [Client] + [Connector].
type TCPClient struct {
	cfg    TCPConfig
	logger *slog.Logger

	mu     sync.Mutex
	conn   net.Conn
	writer *bufio.Writer
	reader *bufio.Reader

	nextID atomic.Uint32

	ackMu sync.Mutex
	acks  map[uint16]chan struct{}

	subMu       sync.RWMutex
	subscribers map[string]MessageHandler

	sendMu sync.Mutex // serialises frame writes

	stop    chan struct{}
	stopped atomic.Bool
	wg      sync.WaitGroup

	connectedAt atomic.Pointer[time.Time]
}

// IsConnected reports whether an active MQTT session is held.
func (c *TCPClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.stopped.Load()
}

// LastConnectedAt returns the timestamp of the most recent successful connect.
func (c *TCPClient) LastConnectedAt() time.Time {
	p := c.connectedAt.Load()
	if p == nil {
		return time.Time{}
	}
	return *p
}

// NewTCPClient constructs a new client.
func NewTCPClient(cfg TCPConfig) *TCPClient {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.KeepAlive < 30*time.Second {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.AckTimeout == 0 {
		cfg.AckTimeout = 20 * time.Second
	}
	return &TCPClient{
		cfg:         cfg,
		logger:      cfg.Logger,
		acks:        make(map[uint16]chan struct{}),
		subscribers: make(map[string]MessageHandler),
		stop:        make(chan struct{}),
	}
}

// Connect dials, sends CONNECT, waits for CONNACK, starts read + keep-alive loops.
func (c *TCPClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return errors.New("mqtt/tcp: already connected")
	}
	c.mu.Unlock()

	u, err := url.Parse(c.cfg.BrokerURL)
	if err != nil {
		return fmt.Errorf("mqtt/tcp: bad broker url: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()
	conn, err := c.dial(dialCtx, u)
	if err != nil {
		return fmt.Errorf("mqtt/tcp: dial: %w", err)
	}

	pkt := &protocol.ConnectPacket{
		ClientID:     c.cfg.ClientID,
		KeepAlive:    uint16(c.cfg.KeepAlive.Seconds()), //nolint:gosec // clamped above
		Username:     c.cfg.Username,
		Password:     c.cfg.Password,
		CleanSession: c.cfg.CleanSession,
		WillTopic:    c.cfg.WillTopic,
		WillPayload:  c.cfg.WillPayload,
		WillRetain:   c.cfg.WillRetain,
	}
	bw := bufio.NewWriter(conn)
	if err := pkt.Encode(bw); err != nil {
		_ = conn.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = conn.Close()
		return err
	}

	_ = conn.SetReadDeadline(time.Now().Add(c.cfg.DialTimeout))
	br := bufio.NewReader(conn)
	frame, err := protocol.ReadFrame(br)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mqtt/tcp: read connack: %w", err)
	}
	if frame.PacketType() != protocol.PacketConnack {
		_ = conn.Close()
		return fmt.Errorf("mqtt/tcp: unexpected packet %d instead of CONNACK", frame.PacketType())
	}
	ack, err := protocol.DecodeConnack(frame.Body)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if ack.ReturnCode != 0 {
		_ = conn.Close()
		return fmt.Errorf("mqtt/tcp: CONNACK return code %d", ack.ReturnCode)
	}
	_ = conn.SetReadDeadline(time.Time{})

	c.mu.Lock()
	c.conn = conn
	c.writer = bw
	c.reader = br
	c.stop = make(chan struct{})
	stopCh := c.stop
	c.stopped.Store(false)
	c.mu.Unlock()
	now := time.Now()
	c.connectedAt.Store(&now)

	c.wg.Add(2)
	go c.readLoop(stopCh)
	go c.keepAliveLoop(stopCh)

	// Replay prior subscriptions on reconnect (CleanSession=true loses them).
	c.subMu.RLock()
	filters := make([]string, 0, len(c.subscribers))
	for f := range c.subscribers {
		filters = append(filters, f)
	}
	c.subMu.RUnlock()
	for _, f := range filters {
		pkt := &protocol.SubscribePacket{PacketID: c.nextPacketID(), TopicFilter: f, QoS: byte(QoS1)}
		if err := c.writeFrame(pkt); err != nil {
			c.logger.Warn("mqtt.tcp.resubscribe", slog.String("filter", f), slog.String("err", err.Error()))
		}
	}

	c.logger.Info("mqtt.tcp.connected", slog.String("broker", c.cfg.BrokerURL))
	return nil
}

// Disconnect sends DISCONNECT, closes the socket, waits for goroutines.
func (c *TCPClient) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil
	}
	if c.stopped.CompareAndSwap(false, true) {
		close(c.stop)
	}
	c.conn = nil
	c.mu.Unlock()

	c.sendMu.Lock()
	_ = protocol.EncodeDisconnect(c.writer)
	_ = c.writer.Flush()
	c.sendMu.Unlock()
	_ = conn.Close()

	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	c.logger.Info("mqtt.tcp.disconnected")
	return nil
}

// Publish: QoS 0 fire-and-forget; QoS 1 waits for PUBACK up to cfg.AckTimeout.
func (c *TCPClient) Publish(ctx context.Context, topic string, payload []byte, qos QoS, retain bool) error {
	if qos > QoS1 {
		return fmt.Errorf("mqtt/tcp: unsupported QoS %d", qos)
	}
	pkt := &protocol.PublishPacket{Topic: topic, Payload: payload, QoS: byte(qos), Retain: retain}
	if qos == 0 {
		return c.writeFrame(pkt)
	}

	pkt.PacketID = c.nextPacketID()
	// Register the ack channel BEFORE the PUBLISH hits the wire.
	ch := make(chan struct{})
	c.ackMu.Lock()
	c.acks[pkt.PacketID] = ch
	c.ackMu.Unlock()
	defer c.removeAck(pkt.PacketID)

	if err := c.writeFrame(pkt); err != nil {
		return err
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.cfg.AckTimeout):
		return fmt.Errorf("mqtt/tcp: PUBACK timeout (id=%d)", pkt.PacketID)
	}
}

// Subscribe: one handler per topic filter (re-subscribing replaces it).
func (c *TCPClient) Subscribe(ctx context.Context, filter string, qos QoS, handler MessageHandler) error {
	pkt := &protocol.SubscribePacket{PacketID: c.nextPacketID(), TopicFilter: filter, QoS: byte(qos)}
	if err := c.writeFrame(pkt); err != nil {
		return err
	}
	c.subMu.Lock()
	c.subscribers[filter] = handler
	c.subMu.Unlock()
	_ = ctx
	return nil
}

// Unsubscribe implements [Subscriber].
func (c *TCPClient) Unsubscribe(ctx context.Context, filter string) error {
	pkt := &protocol.UnsubscribePacket{PacketID: c.nextPacketID(), TopicFilter: filter}
	if err := c.writeFrame(pkt); err != nil {
		return err
	}
	c.subMu.Lock()
	delete(c.subscribers, filter)
	c.subMu.Unlock()
	_ = ctx
	return nil
}

// --- internals ---

func (c *TCPClient) dial(ctx context.Context, u *url.URL) (net.Conn, error) {
	host := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "tls", "ssl", "mqtts":
			host = net.JoinHostPort(u.Hostname(), "8883")
		default:
			host = net.JoinHostPort(u.Hostname(), "1883")
		}
	}
	dialer := &net.Dialer{}
	switch u.Scheme {
	case "tcp", "mqtt", "":
		return dialer.DialContext(ctx, "tcp", host)
	case "tls", "ssl", "mqtts":
		tcpConn, err := dialer.DialContext(ctx, "tcp", host)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(tcpConn, c.cfg.TLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = tcpConn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	return nil, fmt.Errorf("mqtt/tcp: unsupported scheme %q", u.Scheme)
}

type frameEncoder interface{ Encode(w io.Writer) error }

func (c *TCPClient) writeFrame(pkt frameEncoder) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	writer := c.writer
	c.mu.Unlock()
	if writer == nil {
		return errors.New("mqtt/tcp: not connected")
	}
	if err := pkt.Encode(writer); err != nil {
		return err
	}
	return writer.Flush()
}

func (c *TCPClient) nextPacketID() uint16 {
	for {
		v := c.nextID.Add(1)
		id := uint16(v & 0xFFFF) //nolint:gosec // ringed at 16-bit on purpose
		if id == 0 {
			continue
		}
		return id
	}
}

func (c *TCPClient) removeAck(id uint16) {
	c.ackMu.Lock()
	delete(c.acks, id)
	c.ackMu.Unlock()
}

func (c *TCPClient) readLoop(stop <-chan struct{}) {
	defer c.wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		c.mu.Lock()
		reader := c.reader
		c.mu.Unlock()
		if reader == nil {
			return
		}
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			if !c.stopped.Load() {
				c.logger.Warn("mqtt.tcp.read", slog.String("err", err.Error()))
				c.handleConnectionLost() // let the lifecycle reconnect
			}
			return
		}
		switch frame.PacketType() { //nolint:exhaustive // outbound-only types never reach here
		case protocol.PacketPublish:
			ib, err := protocol.DecodePublish(frame.Header, frame.Body)
			if err != nil {
				c.logger.Warn("mqtt.tcp.malformed_publish", slog.String("err", err.Error()))
				continue
			}
			c.dispatch(ib)
			if ib.QoS == 1 {
				c.sendMu.Lock()
				_ = protocol.EncodePuback(c.writer, ib.PacketID)
				_ = c.writer.Flush()
				c.sendMu.Unlock()
			}
		case protocol.PacketPuback:
			if p, err := protocol.DecodePuback(frame.Body); err == nil {
				c.ackMu.Lock()
				if ch, ok := c.acks[p.PacketID]; ok {
					close(ch)
					delete(c.acks, p.PacketID)
				}
				c.ackMu.Unlock()
			}
		case protocol.PacketPingresp:
			// heartbeat ack — no state to update
		case protocol.PacketSuback, protocol.PacketUnsuback:
			// non-blocking in our MVP
		}
	}
}

func (c *TCPClient) keepAliveLoop(stop <-chan struct{}) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.KeepAlive / 2)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.sendMu.Lock()
			c.mu.Lock()
			writer := c.writer
			c.mu.Unlock()
			if writer == nil {
				c.sendMu.Unlock()
				return
			}
			if err := protocol.EncodePingReq(writer); err != nil {
				c.sendMu.Unlock()
				c.logger.Warn("mqtt.tcp.ping", slog.String("err", err.Error()))
				c.handleConnectionLost()
				return
			}
			if err := writer.Flush(); err != nil {
				c.sendMu.Unlock()
				c.logger.Warn("mqtt.tcp.ping", slog.String("err", err.Error()))
				c.handleConnectionLost()
				return
			}
			c.sendMu.Unlock()
		}
	}
}

// handleConnectionLost resets conn/reader/writer to nil so the next
// Connect() dials fresh instead of returning "already connected". Idempotent.
func (c *TCPClient) handleConnectionLost() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.reader = nil
	c.writer = nil
	if c.stopped.CompareAndSwap(false, true) {
		close(c.stop)
	}
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *TCPClient) dispatch(ib *protocol.InboundPublish) {
	c.subMu.RLock()
	var handler MessageHandler
	for filter, h := range c.subscribers {
		if topicMatches(filter, ib.Topic) {
			handler = h
			break
		}
	}
	c.subMu.RUnlock()
	if handler != nil {
		handler(ib.Topic, ib.Payload)
	}
}

// topicMatches: `+` matches one level, `#` matches multiple.
func topicMatches(filter, topic string) bool {
	if filter == topic {
		return true
	}
	fp, tp := 0, 0
	for fp < len(filter) && tp < len(topic) {
		fc, tc := filter[fp], topic[tp]
		switch fc {
		case '#':
			return true
		case '+':
			for tp < len(topic) && topic[tp] != '/' {
				tp++
			}
			fp++
		default:
			if fc != tc {
				return false
			}
			fp++
			tp++
		}
	}
	return fp == len(filter) && tp == len(topic)
}

var (
	_ Client    = (*TCPClient)(nil)
	_ Connector = (*TCPClient)(nil)
)
```

---

## 3. Config-Loader

### 3.1 `internal/config/load.go` — Engine (direkt wiederverwendbar)

Locate (CWD→XDG/APPDATA→~/.config), Env-Override-Ladder (bool→int→float→string), aggregierte
Validierung. **Nur die Konstanten** `EnvPrefix`/`AppDirName`/`ConfigFile` und der Aufruf von
`Validate`/`applyDefaults` sind projektspezifisch.

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Env abstracts process environment access so tests can inject a hermetic env.
type Env interface {
	LookupEnv(key string) (string, bool)
	Environ() []string
}

// OSEnv is the real-process implementation of [Env].
type OSEnv struct{}

// LookupEnv implements Env.
func (OSEnv) LookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

// Environ implements Env.
func (OSEnv) Environ() []string { return os.Environ() }

// Load reads a config from r, applies <PREFIX>_ overrides, fills defaults, validates.
func Load(r io.Reader, env Env) (*Config, error) {
	var raw map[string]any
	if err := yaml.NewDecoder(r).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			raw = map[string]any{} // empty file allowed; defaults kick in
		} else {
			return nil, fmt.Errorf("config: parse yaml: %w", err)
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if env != nil {
		applyEnvOverrides(raw, env)
	}
	bs, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("config: re-marshal merged config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(bs, &cfg); err != nil {
		return nil, fmt.Errorf("config: decode merged config: %w", err)
	}
	applyDefaults(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadFile opens path and delegates to [Load].
func LoadFile(path string, env Env) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return Load(f, env)
}

// Locate walks CWD → XDG/APPDATA → ~/.config and returns the first config.yaml found.
func Locate(env Env) (string, bool) {
	if env == nil {
		env = OSEnv{}
	}
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ConfigFile))
	}
	switch runtime.GOOS {
	case "windows":
		if v, ok := env.LookupEnv("APPDATA"); ok && v != "" {
			candidates = append(candidates, filepath.Join(v, AppDirName, ConfigFile))
		}
	default:
		if v, ok := env.LookupEnv("XDG_CONFIG_HOME"); ok && v != "" {
			candidates = append(candidates, filepath.Join(v, AppDirName, ConfigFile))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", AppDirName, ConfigFile))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// applyEnvOverrides sets raw[KEY] = coerced(value) for every <PREFIX>KEY=value.
func applyEnvOverrides(raw map[string]any, env Env) {
	for _, kv := range env.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(key, EnvPrefix) {
			continue
		}
		cfgKey := key[len(EnvPrefix):]
		if cfgKey == "" {
			continue
		}
		raw[cfgKey] = coerceEnvValue(val)
	}
}

// coerceEnvValue applies the bool → int → float → string ladder.
func coerceEnvValue(s string) any {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return true
	case "false":
		return false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return int(i)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
```

### 3.2 `config/config.go` — Vorlage (Struct + Konstanten: Felder ERSETZEN)

> M-TEC-Felder unten sind die **Vorlage**; ersetze sie durch die HC2M-Felder aus
> `06-architektur-konzept.md` §4 (MQTT_*, HASS_*, RECONNECT_*, APP_NAME …). Behalte die
> flache Struktur + `*Duration()`-Helfer + die Konstanten-Form.

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ
package config

import "time"

// Daemon-wide constants — ANPASSEN.
const (
	ClientID   = "homeconnect2mqtt"   // war: "M-TEC-MQTT"
	EnvPrefix  = "HC2M_"              // war: "MTEC_"
	AppDirName = "homeconnect2mqtt"   // war: "aiomtec2mqtt"
	ConfigFile = "config.yaml"
)

// Config — flach, 1:1 mit YAML. (Felder = Vorlage, ersetzen.)
type Config struct {
	// --- MQTT ---
	MQTTServer   string `yaml:"MQTT_SERVER"`
	MQTTLogin    string `yaml:"MQTT_LOGIN"`
	MQTTPassword string `yaml:"MQTT_PASSWORD"`
	MQTTTopic    string `yaml:"MQTT_TOPIC"`
	// --- Home Assistant ---
	HASSEnable         bool   `yaml:"HASS_ENABLE"`
	HASSBaseTopic      string `yaml:"HASS_BASE_TOPIC"`
	HASSBirthGracetime int    `yaml:"HASS_BIRTH_GRACETIME"` // seconds
	// --- Verbindung/Resilienz (HC-spezifisch ergänzen) ---
	ReconnectInitial int    `yaml:"RECONNECT_INITIAL"` // s
	ReconnectMax     int    `yaml:"RECONNECT_MAX"`     // s
	HandshakeTimeout int    `yaml:"HANDSHAKE_TIMEOUT"` // s
	SendTimeout      int    `yaml:"SEND_TIMEOUT"`      // s
	Heartbeat        int    `yaml:"HEARTBEAT"`         // s
	// --- Misc ---
	Language string `yaml:"LANGUAGE"`
	Debug    bool   `yaml:"DEBUG"`
}

// Beispiel-Helfer (Sekunden → Duration):
func (c *Config) HASSBirthGracetimeDuration() time.Duration {
	return time.Duration(c.HASSBirthGracetime) * time.Second
}
```

### 3.3 `config/validate.go` — Vorlage (Pattern + ValidationError)

Das **aggregierte `ValidationError`-Muster** ist 1:1 übernehmbar; die konkreten Checks ersetzen.

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ
package config

import (
	"fmt"
	"strings"
)

// ValidationError aggregates all problems (declaration order) for one-shot logging.
type ValidationError struct{ Issues []string }

func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		return "config: " + e.Issues[0]
	}
	return fmt.Sprintf("config: %d validation issue(s):\n  - %s",
		len(e.Issues), strings.Join(e.Issues, "\n  - "))
}

// Validate checks the post-defaults config and returns an aggregated *ValidationError.
func Validate(c *Config) error {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	if c.MQTTServer == "" {
		add("MQTT_SERVER is required")
	}
	if c.MQTTTopic == "" {
		add("MQTT_TOPIC is required")
	}
	if c.HASSBirthGracetime < 0 || c.HASSBirthGracetime > 600 {
		add("HASS_BIRTH_GRACETIME must be 0..600 seconds, got %d", c.HASSBirthGracetime)
	}
	// … weitere Range-/Shape-Checks (RECONNECT_*, HEARTBEAT, LANGUAGE ∈ {de,en}, …)

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
```

> Original-`validate.go` enthält zusätzlich nützliche Muster: `rangeCheck`-Closure,
> `net.SplitHostPort` für Bind-Adressen, „beide-oder-keiner"-Check (User+Passwort),
> Whitelist-Maps (`allowedLanguages`). Bei Bedarf von dort übernehmen.

### 3.4 `config/defaults.go` — Vorlage (applyDefaults)

```go
// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ
package config

const (
	DefaultHASSBaseTopic      = "homeassistant"
	DefaultHASSBirthGracetime = 15
	DefaultReconnectInitial   = 1
	DefaultReconnectMax       = 30
	DefaultHandshakeTimeout   = 60
	DefaultSendTimeout        = 20
	DefaultHeartbeat          = 20
	DefaultLanguage           = "de"
)

// applyDefaults fills zero-valued fields. Mandatory fields stay zero and are caught by Validate.
func applyDefaults(c *Config) {
	if c.HASSBaseTopic == "" {
		c.HASSBaseTopic = DefaultHASSBaseTopic
	}
	if c.HASSBirthGracetime == 0 {
		c.HASSBirthGracetime = DefaultHASSBirthGracetime
	}
	if c.ReconnectInitial == 0 {
		c.ReconnectInitial = DefaultReconnectInitial
	}
	if c.ReconnectMax == 0 {
		c.ReconnectMax = DefaultReconnectMax
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.Heartbeat == 0 {
		c.Heartbeat = DefaultHeartbeat
	}
	if c.Language == "" {
		c.Language = DefaultLanguage
	}
}
```

---

## 4. CLAUDE.md & Konventionen (aus dem Schwesterprojekt)

Das go-mtec2mqtt-`CLAUDE.md` ist M-TEC-spezifisch (Modbus/Coordinator). Die **übertragbaren
Konventionen** (für ein neues `CLAUDE.md`):

- **Go 1.26+**, `CGO_ENABLED=0` für Produktion (Ausnahme TLS-PSK, s. §1.3); Tests mit
  `CGO_ENABLED=1 go test -race`.
- **Externe Assets nicht `go:embed`-en** (`devices.yaml`/`mapping.yaml` operator-patchbar; bei
  Release als separate Dateien ausliefern).
- **Direkt-Commits auf `main` blockiert** (`.githooks/pre-commit` via `make setup`); Feature-Branch + PR.
- **Releases:** `internal/version/version.go` `Version`-Default == neuester Tag; im selben
  Commit `changelog.md` ergänzen.
- **Jede Datei** mit `// SPDX-License-Identifier: MIT` + Copyright-Header.
- **Tests:** schmale Interfaces + injizierte Fakes (ein Collaborator pro Test stubben),
  fixe Uhr via `Deps.Now`; Tabellentests.
- **Errors wrappen** (`errors.Is`), nicht vergleichen; **lenient loading** (skip+log statt fatal);
  strukturiertes `slog` mit Kontext-Keys; Quelle im Kommentar zitieren, wo Python-Verhalten
  nachgebaut wird.
- **Resilienz-Vertrag (analog Coordinator):** „publish what you can, retry next tick" — ein
  Lese-/Publish-Fehler wird geloggt, die Schleife läuft weiter; nur `ctx`-Cancel stoppt einen
  Worker. (Auf Home Connect übertragen: siehe `05-resilienz.md` + `06-architektur-konzept.md` §5.)

> Der eingebettete MQTT-Client erwartet `go.mod` mit `gopkg.in/yaml.v3` (Config) und nutzt
> sonst nur die Standardbibliothek. Für Home Connect zusätzlich eine WebSocket-Lib (und ggf.
> TLS-PSK-Lib) ergänzen — siehe `06-architektur-konzept.md` §2.
```
