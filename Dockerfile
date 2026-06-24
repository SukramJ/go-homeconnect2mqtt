# SPDX-License-Identifier: MIT
# Multi-stage build. CGO disabled -> static binary -> distroless runtime.
# Operator-editable assets (devices.yaml / mapping.yaml) live NEXT TO the
# binary rather than being go:embed-ed, so they can be patched without a
# rebuild.

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

# ---------- Stage 2: runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/homeconnect2mqtt /out/hc-util /app/
COPY --from=builder /src/config-template.yaml /src/devices-template.yaml /src/mapping.yaml /app/
VOLUME ["/config"]
ENV XDG_CONFIG_HOME=/config
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/homeconnect2mqtt"]
