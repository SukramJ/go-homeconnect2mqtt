# SPDX-License-Identifier: MIT
# go-homeconnect2mqtt — developer Makefile
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
MODULE   := github.com/SukramJ/go-homeconnect2mqtt
PKG_VER  := $(MODULE)/internal/version

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG_VER).Version=$(VERSION) \
	-X $(PKG_VER).Commit=$(COMMIT) \
	-X $(PKG_VER).BuildDate=$(BUILD_DATE)

GO_BUILD_FLAGS := -trimpath -ldflags="$(LDFLAGS)"

DOCKER_IMAGE ?= go-homeconnect2mqtt
DOCKER_TAG   ?= $(VERSION)

DIST_DIR         := dist
RELEASE_TARGETS  ?= linux/amd64 linux/arm64 darwin/arm64
RELEASE_VERSION  ?= $(shell awk -F'"' '/^[[:space:]]*Version = /{print $$2; exit}' internal/version/version.go)
RELEASE_PAYLOAD  := config-template.yaml devices-template.yaml mapping.yaml README.md LICENSE changelog.md

.PHONY: help
help: ## show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Tool versions, pinned to match .github/workflows/ci.yml. A local gate is
# only usable if it reports what CI reports, so the two lists must be bumped
# together — raise both, run `make check`, and fix or justify whatever the
# new release finds in the same change.
#
# gofumpt is the sharpest case: it is a formatter, so an upstream release
# rewraps untouched code. On @latest that turns CI red on a diff that did
# not cause it, and leaves a developer's clean local run meaningless.
#
# govulncheck and go-licenses are pinned for the same reason even though CI
# does not run them yet: `make vuln` / `make licenses` are gates developers
# feel. Pinning the govulncheck binary does not freeze the answer it gives —
# the vulnerability database is resolved at run time, so a new advisory still
# turns the gate red without a commit here.
#
# goimports stays on @latest deliberately: it has no gate of its own,
# gofumpt is the formatting authority.
GOFUMPT_VERSION       ?= v0.12.0
GOLANGCI_LINT_VERSION ?= v2.13.2
GOVULNCHECK_VERSION   ?= v1.8.0
GOLICENSES_VERSION    ?= v1.6.0

.PHONY: setup
setup: hooks ## install developer tooling + git hooks
	$(GO) install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	$(GO) install golang.org/x/tools/cmd/goimports@latest
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/google/go-licenses@$(GOLICENSES_VERSION)

.PHONY: hooks
hooks: ## point git at the tracked hooks in .githooks/ (blocks direct commits on main)
	@git config core.hooksPath .githooks
	@echo "git core.hooksPath -> .githooks (direct commits on main/master are now blocked)"

.PHONY: build
build: build-daemon build-util ## build both binaries into bin/

.PHONY: build-daemon
build-daemon: ## build the daemon
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/homeconnect2mqtt ./cmd/homeconnect2mqtt

.PHONY: build-util
build-util: ## build the CLI utility
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GO_BUILD_FLAGS) -o $(BIN_DIR)/hc-util ./cmd/hc-util

.PHONY: build-tlspsk
build-tlspsk: ## build the daemon with cgo TLS-PSK support (older appliances)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build -tags tlspsk $(GO_BUILD_FLAGS) -o $(BIN_DIR)/homeconnect2mqtt-tlspsk ./cmd/homeconnect2mqtt

.PHONY: install
install: ## go install both binaries
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/homeconnect2mqtt
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/hc-util

.PHONY: test
test: ## run the full test suite with race detector
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=120s ./...

.PHONY: test-cover
test-cover: ## run tests + coverage report
	CGO_ENABLED=1 $(GO) test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -30

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

.PHONY: addon-changelog
addon-changelog: ## sync the add-on changelog (addon/CHANGELOG.md) from changelog.md
	cp changelog.md addon/CHANGELOG.md

.PHONY: addon-changelog-check
addon-changelog-check: ## fail when addon/CHANGELOG.md drifts from changelog.md
	@if ! diff -q changelog.md addon/CHANGELOG.md >/dev/null; then \
	  echo "addon/CHANGELOG.md is out of sync — run 'make addon-changelog'"; exit 1; \
	fi

.PHONY: check
check: vet fmt-check lint addon-changelog-check test ## the pre-commit / pre-push gate

.PHONY: run
run: build-daemon ## run the daemon
	$(BIN_DIR)/homeconnect2mqtt --config ./config.yaml --devices ./devices.yaml

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
	  stage="$(DIST_DIR)/go-homeconnect2mqtt-$$version-$$goos-$$goarch"; \
	  mkdir -p "$$stage"; \
	  echo "==> $$goos/$$goarch -> $$stage"; \
	  GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags="$$ldflags" \
	    -o "$$stage/homeconnect2mqtt" ./cmd/homeconnect2mqtt; \
	  GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags="$$ldflags" \
	    -o "$$stage/hc-util" ./cmd/hc-util; \
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
