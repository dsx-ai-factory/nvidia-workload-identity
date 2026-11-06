# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

ARG BUILDER_IMG=rust:1.97.1-alpine
ARG RUNTIME_IMG=alpine:3.24.3

FROM --platform=$BUILDPLATFORM ${BUILDER_IMG} AS builder

WORKDIR /src
# Refresh the builder's CA store before downloading Rust dependencies.
RUN apk upgrade --no-cache ca-certificates ca-certificates-bundle
COPY kata-uds-proxy/Cargo.toml kata-uds-proxy/Cargo.lock ./
COPY kata-uds-proxy/src ./src

ARG TARGETARCH
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    set -eu; \
    case "${TARGETARCH}" in \
      amd64) target=x86_64-unknown-linux-musl ;; \
      arm64) target=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    rustup target add "${target}"; \
    lld="$(rustc --print sysroot)/lib/rustlib/$(rustc -vV | sed -n 's/^host: //p')/bin/rust-lld"; \
    RUSTFLAGS="-Clinker=${lld} -Clinker-flavor=ld.lld" \
        cargo build --release --locked --target "${target}"; \
    install -D "target/${target}/release/kata-uds-proxy" /out/kata-uds-proxy

FROM ${RUNTIME_IMG}

# The host role execs nsenter, which Alpine ships in util-linux-misc. The guest
# role execs socat.
# Apply security fixes published after the pinned base image was built.
RUN apk upgrade --no-cache && apk add --no-cache socat util-linux-misc

COPY --from=builder /out/kata-uds-proxy /usr/local/bin/kata-uds-proxy
COPY kata-uds-proxy/host-entrypoint.sh /usr/local/bin/host-entrypoint.sh
COPY kata-uds-proxy/guest-entrypoint.sh /usr/local/bin/guest-entrypoint.sh

# Both roles need root: the host role for CAP_SYS_ADMIN on setns and host cgroup
# writes, the guest role for mknod of /dev/vsock. The chart selects the role.
CMD ["/usr/local/bin/host-entrypoint.sh"]
