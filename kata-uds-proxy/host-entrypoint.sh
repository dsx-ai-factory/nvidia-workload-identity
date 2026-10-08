#!/bin/sh
#
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Host-side proxy for kata-uds-proxy. This is the counterpart to
# guest-entrypoint.sh, which runs inside the Kata guest.

set -euo pipefail

: "${UDS_PATH:?UDS_PATH must be set to the host unix socket to forward to}"
LISTEN_CID="${LISTEN_CID:-2}"
LISTEN_PORT="${LISTEN_PORT:-1234}"

if [ ! -e /proc/1/ns/cgroup ]; then
    echo "error: /proc/1/ns/cgroup is missing; hostPID: true is required" >&2
    exit 1
fi

if [ ! -w /sys/fs/cgroup/cgroup.procs ]; then
    echo "error: /sys/fs/cgroup is not writable; mount the host cgroup2 fs read-write" >&2
    exit 1
fi

if ! nsenter --target 1 --mount -- modprobe vhost_vsock; then
    echo "warning: failed to load vhost_vsock" >&2
fi

if [ ! -S "$UDS_PATH" ]; then
    echo "warning: $UDS_PATH is not a socket yet; connections will fail until it appears" >&2
fi

exec nsenter --cgroup=/proc/1/ns/cgroup -- \
    /usr/local/bin/kata-uds-proxy \
    --uds "$UDS_PATH" \
    --listen-cid "$LISTEN_CID" \
    --listen-port "$LISTEN_PORT"
