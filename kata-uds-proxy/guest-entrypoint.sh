#!/bin/bash
#
# Copyright (c) 2026 NVIDIA Corporation
#
# SPDX-License-Identifier: Apache-2.0
#
# Guest-side proxy. Run this inside a Kata pod to provide
# a unix socket based on a vsock.

set -euo pipefail

: "${SOCK_PATH:?SOCK_PATH must be set to the guest unix socket to serve}"
: "${VSOCK_PORT:?VSOCK_PORT must be set to the host proxy listen port}"

# Kata exposes the vsock misc device in sysfs but the node must be created.
if [ ! -c /dev/vsock ]; then
    if [ ! -r /sys/class/misc/vsock/dev ]; then
        echo "error: /sys/class/misc/vsock/dev is unreadable; this is not a vsock-capable guest" >&2
        exit 1
    fi
    dev_num="$(cat /sys/class/misc/vsock/dev)"
    mknod /dev/vsock c "${dev_num%%:*}" "${dev_num##*:}"
fi

mkdir -p "$(dirname "$SOCK_PATH")"

rm -f "$SOCK_PATH"

# Always conenct to host vsock (CID 2)
exec socat -d -d \
    "UNIX-LISTEN:${SOCK_PATH},fork,reuseaddr" \
    "VSOCK-CONNECT:2:${VSOCK_PORT}"
