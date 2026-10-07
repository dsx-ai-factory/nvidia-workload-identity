#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONTEXT=kind-k2
NAMESPACE=tenant-tenant1
SECRET=e2e-debian-vm-e2e-ssh
OUTPUT_DIR="${SCRIPT_DIR}/generated"
TEMP_DIR=""

source "${PROJECT_ROOT}/local-e2e-tests/common.sh"

cleanup() {
    [[ -z "${TEMP_DIR}" || ! -d "${TEMP_DIR}" ]] || rm -rf "${TEMP_DIR}"
}

read_existing() {
    local json
    json=$(kubectl get secret "${SECRET}" -n "${NAMESPACE}" --context "${CONTEXT}" -o json)
    jq -er '.data.id_ed25519' <<<"${json}" | base64 -d >"${TEMP_DIR}/id_ed25519"
    jq -er '.data["id_ed25519.pub"]' <<<"${json}" | base64 -d >"${TEMP_DIR}/id_ed25519.pub"
    log_info "Reusing the existing VM SSH key"
}

generate_key() {
    ssh-keygen -q -t ed25519 -N "" -C dsx-identity-e2e \
        -f "${TEMP_DIR}/id_ed25519"
    log_info "Generated the VM SSH key"
}

write_cloud_init() {
    local public_key
    public_key=$(<"${TEMP_DIR}/id_ed25519.pub")
    cat >"${TEMP_DIR}/userdata" <<EOF
#cloud-config
ssh_pwauth: false
users:
  - name: debian
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    shell: /bin/bash
    ssh_authorized_keys:
      - ${public_key}
EOF
}

publish_sources() {
    local previous="${OUTPUT_DIR}.previous"
    rm -rf "${previous}"
    if [[ -d "${OUTPUT_DIR}" ]]; then
        mv "${OUTPUT_DIR}" "${previous}"
    fi
    mv "${TEMP_DIR}" "${OUTPUT_DIR}"
    TEMP_DIR=""
    rm -rf "${previous}"
}

main() {
    validate_context_exists "${CONTEXT}"
    mkdir -p "$(dirname "${OUTPUT_DIR}")"
    TEMP_DIR=$(mktemp -d "$(dirname "${OUTPUT_DIR}")/.generated.XXXXXX")

    if kubectl get secret "${SECRET}" -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
        read_existing
    else
        if kubectl get vm e2e-debian-vm -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
            log_error "The VM exists without its SSH Secret. Refusing to replace its credentials"
            return 1
        fi
        generate_key
    fi
    [[ -s "${TEMP_DIR}/id_ed25519" && -s "${TEMP_DIR}/id_ed25519.pub" ]]
    write_cloud_init
    publish_sources
}

trap cleanup EXIT
trap 'exit 1' INT TERM
main "$@"
