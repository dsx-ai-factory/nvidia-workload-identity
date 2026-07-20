#!/bin/bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONTEXT=kind-k1
NAMESPACE=dsx-identity
OUTPUT_DIR="${PROJECT_ROOT}/scripts/secret-generators/event-bus/generated"
TEMP_DIR=""

source "${PROJECT_ROOT}/scripts/common.sh"

NKEY_SECRETS=(
    auth-callout-keys
    nats-auth-signing
    nats-authx-user
    nats-nack-user
    nats-surveyor
    nats-xkey
)

required_keys() {
    case "$1" in
        auth-callout-keys) printf '%s\n' nkey-seed issuer-seed xkey-seed ;;
        nats-auth-signing|nats-authx-user|nats-surveyor|nats-xkey) printf '%s\n' seed pubkey ;;
        nats-nack-user) printf '%s\n' seed pubkey nack-user.nk ;;
    esac
}

cleanup() {
    [[ -z "${TEMP_DIR}" || ! -d "${TEMP_DIR}" ]] || rm -rf "${TEMP_DIR}"
}

secret_exists() {
    kubectl get secret "$1" -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1
}

write_existing_secret() {
    local name="$1" json key target
    json=$(kubectl get secret "${name}" -n "${NAMESPACE}" --context "${CONTEXT}" -o json)
    while IFS= read -r key; do
        target="${TEMP_DIR}/csc/nkeys/${name}/${key}"
        mkdir -p "$(dirname "${target}")"
        if ! jq -er --arg key "${key}" '.data[$key]' <<<"${json}" | base64 -d >"${target}"; then
            log_error "Secret ${name} is missing required key ${key}. Refusing to rotate the NKey hierarchy"
            return 1
        fi
        if [[ ! -s "${target}" ]]; then
            log_error "Secret ${name} has an empty ${key}. Refusing to rotate the NKey hierarchy"
            return 1
        fi
    done < <(required_keys "${name}")
}

validate_nkey_sources() {
    local name key source
    for name in "${NKEY_SECRETS[@]}"; do
        while IFS= read -r key; do
            source="${TEMP_DIR}/csc/nkeys/${name}/${key}"
            if [[ ! -s "${source}" ]]; then
                log_error "Generated NKey source is missing or empty: ${source}"
                return 1
            fi
        done < <(required_keys "${name}")
    done
}

prepare_nkeys() {
    local existing=0 name
    for name in "${NKEY_SECRETS[@]}"; do
        if secret_exists "${name}"; then
            existing=$((existing + 1))
        fi
    done

    if [[ "${existing}" -eq "${#NKEY_SECRETS[@]}" ]]; then
        for name in "${NKEY_SECRETS[@]}"; do
            write_existing_secret "${name}"
        done
        log_info "Reusing existing DSX Event Bus NKey Secrets"
    elif [[ "${existing}" -eq 0 ]]; then
        "${PROJECT_ROOT}/third-party/dsx-exchange/deploy/scripts/generate-nkeys.sh" \
            --output "${TEMP_DIR}"
        log_info "Generated DSX Event Bus NKey sources"
    else
        log_error "Partial DSX Event Bus NKey Secret state detected (${existing}/${#NKEY_SECRETS[@]}). Refusing to rotate existing credentials"
        return 1
    fi
    validate_nkey_sources
}

prepare_oauth_secret() {
    local secret_name="$1"
    local expected_client_id="$2"
    local client_secret_dir="$3"
    local json client_id
    mkdir -p "${client_secret_dir}"

    if secret_exists "${secret_name}"; then
        json=$(kubectl get secret "${secret_name}" -n "${NAMESPACE}" \
            --context "${CONTEXT}" -o json)
        if ! jq -er '.data["client-id"]' <<<"${json}" | base64 -d >"${client_secret_dir}/client-id" ||
            ! jq -er '.data["client-secret"]' <<<"${json}" | base64 -d >"${client_secret_dir}/client-secret"; then
            log_error "OAuth2 Secret is missing a required key. Refusing to rotate credentials"
            return 1
        fi
        client_id=$(<"${client_secret_dir}/client-id")
        if [[ "${client_id}" != "${expected_client_id}" ||
            "$(wc -c <"${client_secret_dir}/client-secret")" -ne 64 ]] ||
            ! grep -Eq '^[0-9a-f]{64}$' "${client_secret_dir}/client-secret"; then
            log_error "OAuth2 Secret ${secret_name} is invalid. Refusing to rotate credentials"
            return 1
        fi
        log_info "Reusing OAuth2 Secret ${secret_name}"
        return 0
    fi

    if kubectl get deployment keycloak -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
        log_error "Keycloak exists but its OAuth2 Secret is missing. Refusing to generate a mismatched credential"
        return 1
    fi

    printf '%s' "${expected_client_id}" >"${client_secret_dir}/client-id"
    printf '%s' "$(openssl rand -hex 32)" >"${client_secret_dir}/client-secret"
    log_info "Generated OAuth2 Secret source ${secret_name}"
}

prepare_oauth() {
    prepare_oauth_secret dsx-identity-keys-mqtt-oauth dsx-identity-keys "${TEMP_DIR}/oauth"
    prepare_oauth_secret event-bus-e2e-observer-oauth event-bus-e2e-observer \
        "${TEMP_DIR}/observer-oauth"
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
    prepare_nkeys
    prepare_oauth
    publish_sources
    log_success "Event Bus and OAuth Secret sources are ready"
}

trap cleanup EXIT
trap 'exit 1' INT TERM
main "$@"
