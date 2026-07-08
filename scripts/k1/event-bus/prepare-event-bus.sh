#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONTEXT="${CONTEXT:-kind-k1}"
CLUSTER_NAME="${CONTEXT#kind-}"
AUTH_CALLOUT_IMAGE="auth-callout:v2.6.0"
EVENT_BUS_CHART="${PROJECT_ROOT}/third-party/dsx-exchange/deploy/nats-event-bus"
EXPOSE_GATEWAY="${EXPOSE_GATEWAY:-true}"

source "${PROJECT_ROOT}/scripts/common.sh"

main() {
    validate_context_exists "${CONTEXT}"
    if ! command -v nsc >/dev/null 2>&1 || ! command -v nk >/dev/null 2>&1; then
        log_error "nsc and nk are required. Run 'make event-bus-tools'"
        return 1
    fi

    docker build -t "${AUTH_CALLOUT_IMAGE}" \
        -f "${PROJECT_ROOT}/third-party/dsx-exchange/auth-callout/build/package/Dockerfile" \
        "${PROJECT_ROOT}/third-party/dsx-exchange/auth-callout"
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${AUTH_CALLOUT_IMAGE}" "${CLUSTER_NAME}"

    CONTEXT="${CONTEXT}" "${SCRIPT_DIR}/prepare-oauth2.sh"
    CONTEXT="${CONTEXT}" PATH="${PROJECT_ROOT}/bin:${PATH}" "${SCRIPT_DIR}/prepare-secrets.sh"

    if [[ "${EXPOSE_GATEWAY}" == "true" ]]; then
        kubectl apply -f "${SCRIPT_DIR}/oauth-route.yaml" --context "${CONTEXT}" >/dev/null
    fi

    local helm_args=(
        upgrade --install event-bus "${EVENT_BUS_CHART}"
        --namespace dsx-identity
        --create-namespace
        --values "${SCRIPT_DIR}/values.yaml"
        --kube-context "${CONTEXT}"
        --wait
        --timeout 10m
    )
    if [[ "${EXPOSE_GATEWAY}" == "true" ]]; then
        helm_args+=(--set global.eventBus.gateway.routes.mqtt.enabled=true)
    fi
    helm "${helm_args[@]}"

    kubectl rollout status deployment/keycloak -n dsx-identity \
        --timeout=600s --context "${CONTEXT}"

    if [[ "${EXPOSE_GATEWAY}" == "true" ]]; then
        kubectl wait --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True \
            httproute/event-bus-oauth-token -n dsx-identity --timeout=60s --context "${CONTEXT}"
        kubectl wait --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True \
            tcproute/nats-mqtt -n dsx-identity --timeout=60s --context "${CONTEXT}"
    fi
    log_success "External DSX Event Bus and OAuth provider are ready"
}

main "$@"
