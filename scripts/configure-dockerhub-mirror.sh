#!/bin/bash

set -euo pipefail

mirror="${DOCKERHUB_MIRROR:?DOCKERHUB_MIRROR is required}"
daemon_config=/etc/docker/daemon.json
current_config="$(mktemp)"
merged_config="$(mktemp)"
trap 'rm -f "${current_config}" "${merged_config}"' EXIT

if sudo test -f "${daemon_config}"; then
    sudo cp "${daemon_config}" "${current_config}"
else
    printf '{}\n' > "${current_config}"
fi

jq --arg mirror "${mirror}" \
    '.["registry-mirrors"] = (((.["registry-mirrors"] // []) + [$mirror]) | unique)' \
    "${current_config}" > "${merged_config}"

if sudo test -f "${daemon_config}" && sudo cmp -s "${merged_config}" "${daemon_config}"; then
    exit 0
fi

sudo install -D -m 0644 "${merged_config}" "${daemon_config}"
if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl restart docker
else
    sudo service docker restart
fi
docker info --format '{{json .RegistryConfig.Mirrors}}' | \
    tee /dev/stderr | jq -e --arg mirror "${mirror}" 'any(.[]; . == $mirror or . == ($mirror + "/"))'
