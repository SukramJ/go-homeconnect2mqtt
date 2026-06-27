# SPDX-License-Identifier: MIT
# Home Assistant add-on Dockerfile.
#
# Multi-stage build: compile the (CGo-free) Go binaries, then land them on a
# Home Assistant base image so bashio + /usr/bin/with-contenv are available to
# script/run.sh, which translates the add-on options into HC2M_* config.
#
# BUILD CONTEXT: the Go sources live at the REPOSITORY ROOT, not inside addon/.
# Build with the repo root as context:
#
#   docker build \
#     --build-arg BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest \
#     -f addon/Dockerfile -t go-homeconnect2mqtt-addon .
#
# Note: this CGo-free build supports AES appliances. TLS-PSK appliances need a
# separate cgo build (see the main README / docs/01-protocol.md §4).

# Default keeps manual/standalone builds working; the Supervisor and CI always
# override BUILD_FROM with the per-arch HA base image (see addon/build.yaml).
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest

# ---------- Stage 1: build the Go binaries ----------
FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

# Repo-root build context: brings in cmd/, internal/, mapping.yaml.
COPY . .

ARG VERSION=addon
ARG COMMIT=none
ARG BUILD_DATE=unknown

ENV CGO_ENABLED=0
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/homeconnect2mqtt ./cmd/homeconnect2mqtt && \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Version=${VERSION} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.Commit=${COMMIT} \
        -X github.com/SukramJ/go-homeconnect2mqtt/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/hc-util ./cmd/hc-util

# ---------- Stage 2: Home Assistant add-on runtime ----------
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:latest
FROM ${BUILD_FROM}

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
ARG BUILD_VERSION=0.1.0
LABEL \
  io.hass.version="${BUILD_VERSION}" \
  io.hass.type="addon" \
  io.hass.arch="${BUILD_ARCH}"

CMD ["/run.sh"]
