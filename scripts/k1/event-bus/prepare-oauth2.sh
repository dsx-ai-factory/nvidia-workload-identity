#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONTEXT="${CONTEXT:-kind-k1}"
NAMESPACE="dsx-identity"
CLIENT_SECRET_NAME="spiffe-exchange-mqtt-oauth"

source "${PROJECT_ROOT}/scripts/common.sh"

prepare_oauth_secrets() {
    local client_id client_secret secret_json
    if secret_json=$(kubectl get secret "${CLIENT_SECRET_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json 2>/dev/null); then
        client_id=$(jq -er '.data["client-id"]' <<<"${secret_json}" | base64 -d)
        client_secret=$(jq -er '.data["client-secret"]' <<<"${secret_json}" | base64 -d)
        if [[ "${client_id}" != "spiffe-exchange" || -z "${client_secret}" ]]; then
            log_error "OAuth2 Secret is invalid. Refusing to rotate credentials"
            return 1
        fi
        log_info "DSX Event Bus OAuth2 Secret already exists"
        return 0
    fi

    if kubectl get deployment keycloak -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
        log_error "Keycloak already exists but its OAuth2 Secret is missing. Refusing to generate a mismatched credential"
        return 1
    fi

    client_secret="$(openssl rand -hex 32)"
    kubectl create secret generic "${CLIENT_SECRET_NAME}" \
        -n "${NAMESPACE}" --context "${CONTEXT}" \
        --from-literal=client-id=spiffe-exchange \
        --from-literal="client-secret=${client_secret}" >/dev/null
    log_info "Created DSX Event Bus OAuth2 Secret"
}

main() {
    validate_context_exists "${CONTEXT}"
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml \
        --context "${CONTEXT}" | kubectl apply -f - --context "${CONTEXT}"
    prepare_oauth_secrets

    kubectl apply -k "${SCRIPT_DIR}" --context "${CONTEXT}"
    log_success "DSX Event Bus OAuth2 provider submitted"
}

main "$@"
