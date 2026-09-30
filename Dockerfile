ARG BUILDER_IMG=golang:1.26.4

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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3 AS runtime
ARG BINARY
COPY --from=builder /out/${BINARY} /usr/local/bin/${BINARY}
USER 1000:1000
WORKDIR /

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
