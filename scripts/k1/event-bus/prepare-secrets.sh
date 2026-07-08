#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONTEXT="${CONTEXT:-kind-k1}"
NAMESPACE="${NAMESPACE:-dsx-identity}"
GENERATED_DIR=""

source "${PROJECT_ROOT}/scripts/common.sh"

cleanup() {
    if [[ -n "${GENERATED_DIR}" && -d "${GENERATED_DIR}" ]]; then
        rm -rf "${GENERATED_DIR}"
    fi
}

secret_exists() {
    kubectl get secret "$1" -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1
}

create_secret() {
    local name="$1"
    local source="${GENERATED_DIR}/csc/nkeys/${name}"
    local args=()
    local file

    if [[ ! -d "${source}" ]]; then
        log_error "Generated NKey directory is missing: ${source}"
        return 1
    fi

    for file in "${source}"/*; do
        [[ -f "${file}" ]] || continue
        args+=("--from-file=$(basename "${file}")=${file}")
    done
    if [[ "${#args[@]}" -eq 0 ]]; then
        log_error "Generated NKey directory is empty: ${source}"
        return 1
    fi

    kubectl create secret generic "${name}" \
        -n "${NAMESPACE}" \
        --context "${CONTEXT}" \
        "${args[@]}" >/dev/null
}

validate_secrets() {
    validate_secret auth-callout-keys nkey-seed issuer-seed xkey-seed
    validate_secret nats-auth-signing seed pubkey
    validate_secret nats-authx-user seed pubkey
    validate_secret nats-nack-user seed pubkey nack-user.nk
    validate_secret nats-surveyor seed pubkey
    validate_secret nats-xkey seed pubkey
}

validate_secret() {
    local name="$1"
    shift
    local key
    for key in "$@"; do
        if ! kubectl get secret "${name}" -n "${NAMESPACE}" --context "${CONTEXT}" \
            -o json | jq -e --arg key "${key}" '.data[$key] != null' >/dev/null; then
            log_error "Secret ${name} is missing required key ${key}. Refusing to rotate the NKey hierarchy"
            return 1
        fi
    done
}

main() {
    local secrets=(
        auth-callout-keys
        nats-auth-signing
        nats-authx-user
        nats-nack-user
        nats-surveyor
        nats-xkey
    )
    local existing=0 name

    validate_context_exists "${CONTEXT}"
    kubectl create namespace "${NAMESPACE}" --context "${CONTEXT}" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}"

    for name in "${secrets[@]}"; do
        if secret_exists "${name}"; then
            existing=$((existing + 1))
        fi
    done
    if [[ "${existing}" -eq "${#secrets[@]}" ]]; then
        validate_secrets
        log_info "DSX Event Bus NKey Secrets already exist"
        return 0
    fi
    if [[ "${existing}" -ne 0 ]]; then
        log_error "Partial DSX Event Bus NKey Secret state detected (${existing}/${#secrets[@]}). Refusing to rotate existing credentials"
        return 1
    fi

    if ! command -v nsc >/dev/null 2>&1 || ! command -v nk >/dev/null 2>&1; then
        log_error "nsc and nk are required. Run 'make event-bus-tools'"
        return 1
    fi

    GENERATED_DIR="$(mktemp -d)"
    chmod 700 "${GENERATED_DIR}"
    "${PROJECT_ROOT}/third-party/dsx-exchange/deploy/scripts/generate-nkeys.sh" \
        --output "${GENERATED_DIR}"

    for name in "${secrets[@]}"; do
        create_secret "${name}"
    done
    validate_secrets
    log_success "DSX Event Bus NKey Secrets are ready"
}

trap cleanup EXIT
main "$@"
