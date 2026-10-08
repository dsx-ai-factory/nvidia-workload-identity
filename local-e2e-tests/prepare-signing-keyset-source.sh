#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTEXT="${1:-}"
VALUES_FILE="${2:-}"
OUTPUT_DIR="${3:-}"
NAMESPACE=dsx-identity
TEMP_DIR=""

source "${SCRIPT_DIR}/common.sh"

cleanup() {
    [[ -z "${TEMP_DIR}" || ! -d "${TEMP_DIR}" ]] || rm -rf "${TEMP_DIR}"
}

write_existing() {
    kubectl get secret "$1" -n "${NAMESPACE}" --context "${CONTEXT}" -o json \
        | jq -er '.data["keyset.json"]' | base64 -d >"${TEMP_DIR}/keyset.json"
    kubectl get secret "$2" -n "${NAMESPACE}" --context "${CONTEXT}" -o json \
        | jq -er '.data["active-kid.json"]' | base64 -d >"${TEMP_DIR}/active-kid.json"
    log_info "Reusing the existing DSX Identity Keys signing keyset in ${CONTEXT}"
}

generate_sources() {
    local generated
    generated=$(cd "${SCRIPT_DIR}/.." && go run ./cmd/generate-signing-keyset generate)
    jq -jc '{keys}' <<<"${generated}" >"${TEMP_DIR}/keyset.json"
    jq -jc '{active_kid, alg}' <<<"${generated}" >"${TEMP_DIR}/active-kid.json"
    log_info "Generated the DSX Identity Keys signing keyset source for ${CONTEXT}"
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
    local keyset_secret active_kid_secret
    if [[ -z "${CONTEXT}" || -z "${VALUES_FILE}" || -z "${OUTPUT_DIR}" ]]; then
        log_error "Usage: $0 <kube-context> <values-file> <output-directory>"
        return 1
    fi

    validate_context_exists "${CONTEXT}"
    keyset_secret=$(yq -r '.identityKeys.signingKeysetSecret // "dsx-identity-keys-signing-keyset"' \
        "${VALUES_FILE}")
    active_kid_secret=$(yq -r '.identityKeys.activeKIDSecret // "dsx-identity-keys-active-kid"' \
        "${VALUES_FILE}")
    mkdir -p "$(dirname "${OUTPUT_DIR}")"
    TEMP_DIR=$(mktemp -d "$(dirname "${OUTPUT_DIR}")/.generated.XXXXXX")

    if kubectl get secret "${keyset_secret}" -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1 ||
        kubectl get secret "${active_kid_secret}" -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
        write_existing "${keyset_secret}" "${active_kid_secret}"
    else
        generate_sources
    fi
    publish_sources
    log_success "DSX Identity Keys signing keyset source is ready for ${CONTEXT}"
}

trap cleanup EXIT
trap 'exit 1' INT TERM
main "$@"
