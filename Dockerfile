# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

ARG BUILDER_IMG=golang:1.26.11

FROM --platform=$BUILDPLATFORM ${BUILDER_IMG} AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
ARG BINARY
COPY cmd/${BINARY}/ ./cmd/${BINARY}/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/${BINARY} ./cmd/${BINARY}

FROM nvcr.io/nvidia/distroless/go:v4.2.1 AS runtime
ARG BINARY
COPY --from=builder /out/${BINARY} /usr/local/bin/${BINARY}
USER 1000:1000

FROM runtime AS dsx-identity-keys
CMD ["/usr/local/bin/dsx-identity-keys"]

FROM runtime AS dsx-token-fetcher
CMD ["/usr/local/bin/dsx-token-fetcher"]

FROM runtime AS metadata-service
CMD ["/usr/local/bin/metadata-service"]

FROM runtime AS dsx-imds-network-setup
CMD ["/usr/local/bin/dsx-imds-network-setup"]

FROM runtime AS dsx-identity-keys-proxy-csi
CMD ["/usr/local/bin/dsx-identity-keys-proxy-csi"]

FROM runtime AS dsx-identity-controller
CMD ["/usr/local/bin/dsx-identity-controller"]
