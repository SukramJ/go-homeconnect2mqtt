# SPDX-License-Identifier: MIT
# Home Assistant add-on Dockerfile.
#
# Multi-stage build: compile the Go binaries, then land them on a Home Assistant
# base image so bashio + /usr/bin/with-contenv are available to script/run.sh,
# which translates the add-on options into HC2M_* config.
#
# This image is built with cgo + OpenSSL and -tags tlspsk, so it supports BOTH
# AES (ws://host:80) and TLS-PSK (wss://host:443, older appliances) — see
# docs/01-protocol.md §4. The add-on is amd64-only, which keeps the cgo build a
# fast native compile (no QEMU cross-build).
#
# BUILD CONTEXT: the Go sources live at the REPOSITORY ROOT, not inside addon/.
# Build with the repo root as context:
#
#   docker build \
#     --build-arg BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest \
#     -f addon/Dockerfile -t go-homeconnect2mqtt-addon .

# Default keeps manual/standalone builds working; the Supervisor and CI always
# override BUILD_FROM with the per-arch HA base image (see addon/build.yaml).
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest

# ---------- Stage 1: build the Go binaries ----------
FROM golang:1.26-alpine AS builder
WORKDIR /src

# TLS-PSK needs cgo + OpenSSL (Go's crypto/tls has no external-PSK ciphers).
RUN apk add --no-cache build-base openssl-dev pkgconf

COPY go.mod go.sum ./
RUN go mod download

# Repo-root build context: brings in cmd/, internal/, mapping.yaml.
COPY . .

ARG VERSION=addon
ARG COMMIT=none
ARG BUILD_DATE=unknown

# cgo + the tlspsk build tag pull in the OpenSSL-backed TLS-PSK transport.
ENV CGO_ENABLED=1
RUN go build -trimpath -tags tlspsk \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/homeconnect2mqtt ./cmd/homeconnect2mqtt && \
    go build -trimpath -tags tlspsk \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/hc-util ./cmd/hc-util

# ---------- Stage 2: Home Assistant add-on runtime ----------
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest
FROM ${BUILD_FROM}

# OpenSSL runtime libs for the cgo TLS-PSK transport (HA base images are Alpine).
RUN apk add --no-cache libssl3 libcrypto3

# Daemon + the onboarding CLI (run.sh uses hc-util to parse a profile ZIP).
COPY --from=builder /out/homeconnect2mqtt /usr/bin/homeconnect2mqtt
COPY --from=builder /out/hc-util /usr/bin/hc-util
# Optional enrichment catalogue resolved from the WORKDIR (--mapping default).
COPY --from=builder /src/mapping.yaml /app/mapping.yaml

WORKDIR /app

# Add-on entrypoint: reads /data/options.json and exec's the daemon.
COPY script/run.sh /run.sh
RUN chmod +x /run.sh

# Diagnostic web UI served through Ingress.
EXPOSE 8080

# Add-on image labels (BUILD_ARCH / BUILD_VERSION injected by the Supervisor
# build; defaults keep manual builds from failing).
ARG BUILD_ARCH=amd64
ARG BUILD_VERSION=0.10.1
LABEL \
  io.hass.version="${BUILD_VERSION}" \
  io.hass.type="addon" \
  io.hass.arch="${BUILD_ARCH}"

CMD ["/run.sh"]
