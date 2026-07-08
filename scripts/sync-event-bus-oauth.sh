#!/bin/bash
# Copy the k1 Event Bus OAuth client credential to the k2 Exchange namespace.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_CONTEXT="kind-k1"
TARGET_CONTEXT="kind-k2"
NAMESPACE="dsx-identity"
SECRET_NAME="spiffe-exchange-mqtt-oauth"

source "${SCRIPT_DIR}/common.sh"

main() {
    validate_context_exists "${SOURCE_CONTEXT}"
    validate_context_exists "${TARGET_CONTEXT}"

    local source
    source=$(kubectl get secret "${SECRET_NAME}" -n "${NAMESPACE}" \
        --context "${SOURCE_CONTEXT}" -o json)
    if ! jq -e '
        (.data["client-id"] | @base64d) == "spiffe-exchange" and
        (.data["client-secret"] | @base64d | length > 0)
    ' <<<"${source}" >/dev/null; then
        log_error "k1 Event Bus OAuth client Secret is invalid"
        return 1
    fi

    kubectl create namespace "${NAMESPACE}" --context "${TARGET_CONTEXT}" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${TARGET_CONTEXT}" >/dev/null
    jq -c --arg namespace "${NAMESPACE}" --arg name "${SECRET_NAME}" '{
        apiVersion: "v1",
        kind: "Secret",
        metadata: {name: $name, namespace: $namespace},
        type,
        data: {"client-id": .data["client-id"], "client-secret": .data["client-secret"]}
    }' <<<"${source}" | kubectl apply -f - --context "${TARGET_CONTEXT}" >/dev/null

    log_success "Event Bus OAuth client credential synchronized to k2"
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
