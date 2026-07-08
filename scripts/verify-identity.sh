#!/bin/bash
# verify-identity.sh - Verifies the k1 identity smoke environment
# Verifies SPIRE, the OAuth-only DSX Event Bus, Exchange, and Kyverno.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
source "${SCRIPT_DIR}/common.sh"

CONTEXT="kind-k1"
IDENTITY_NAMESPACE="dsx-identity"
POD_NAME="e2e-nginx"
POD_NAMESPACE="dsx-identity-e2e"

PASS=0
FAIL=0
MQTT_PF_PID=""
KEYCLOAK_PF_PID=""

check() {
    local name="$1"
    shift
    if "$@"; then
        log_success "$name"
        PASS=$((PASS + 1))
    else
        log_error "$name"
        FAIL=$((FAIL + 1))
    fi
}

cleanup_port_forwards() {
    cleanup_command_output_follower
    if [[ -n "${MQTT_PF_PID}" ]]; then
        kill "${MQTT_PF_PID}" 2>/dev/null || true
        wait "${MQTT_PF_PID}" 2>/dev/null || true
        MQTT_PF_PID=""
    fi
    if [[ -n "${KEYCLOAK_PF_PID}" ]]; then
        kill "${KEYCLOAK_PF_PID}" 2>/dev/null || true
        wait "${KEYCLOAK_PF_PID}" 2>/dev/null || true
        KEYCLOAK_PF_PID=""
    fi
}

event_bus_enforces_oauth() {
    kubectl port-forward svc/nats 11883:1883 -n "${IDENTITY_NAMESPACE}" \
        --context "${CONTEXT}" >/dev/null 2>&1 &
    MQTT_PF_PID=$!
    kubectl port-forward svc/keycloak-service 18081:8080 -n "${IDENTITY_NAMESPACE}" \
        --context "${CONTEXT}" >/dev/null 2>&1 &
    KEYCLOAK_PF_PID=$!

    local client_id client_secret access_token started_at
    client_id=$(kubectl get secret spiffe-exchange-mqtt-oauth -n "${IDENTITY_NAMESPACE}" \
        --context "${CONTEXT}" -o json | jq -er '.data["client-id"]' | base64 -d) || return 1
    client_secret=$(kubectl get secret spiffe-exchange-mqtt-oauth -n "${IDENTITY_NAMESPACE}" \
        --context "${CONTEXT}" -o json | jq -er '.data["client-secret"]' | base64 -d) || return 1
    access_token=$(curl -fsS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 --connect-timeout 1 --max-time 10 \
        --data-urlencode grant_type=client_credentials \
        --data-urlencode scope=mqtt \
        --data-urlencode "client_id=${client_id}" \
        --data-urlencode "client_secret=${client_secret}" \
        http://127.0.0.1:18081/realms/event-bus/protocol/openid-connect/token | \
        jq -er '.access_token') || return 1
    started_at=$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')

    local status=0
    (
        cd "${PROJECT_ROOT}"
        DSX_E2E_MQTT_BROKER=tcp://127.0.0.1:11883 \
            DSX_E2E_MQTT_TOKEN="${access_token}" \
            go test -count=1 -run '^TestLiveMQTTAuthorization$' \
            ./cmd/spiffe-exchange-service
    ) || status=$?
    if [[ "${status}" -eq 0 ]]; then
        # MQTT 3.1.1 has no negative PUBACK. Observe NATS rejecting the publish.
        if ! wait_for_command_output 30s \
            'Publish Violation - Subject "outside.dsx-identity-e2e"' '' \
            kubectl logs -f statefulset/nats -n "${IDENTITY_NAMESPACE}" \
            --all-containers=true --since-time="${started_at}" --context "${CONTEXT}"; then
            printf 'Event Bus did not reject the out-of-prefix MQTT publish\n' >&2
            status=1
        fi
    fi
    cleanup_port_forwards
    return "${status}"
}

pod_receives_exchanged_identity() {
    if ! kubectl wait --for=condition=Ready "pod/${POD_NAME}" -n "${POD_NAMESPACE}" \
        --context "${CONTEXT}" --timeout=180s >/dev/null; then
        kubectl describe pod "${POD_NAME}" -n "${POD_NAMESPACE}" --context "${CONTEXT}" || true
        kubectl logs "${POD_NAME}" -n "${POD_NAMESPACE}" -c dsx-token-fetcher \
            --tail=100 --context "${CONTEXT}" || true
        return 1
    fi

    local token uid expected_subject encoded padding payload now
    token=$(kubectl exec "${POD_NAME}" -n "${POD_NAMESPACE}" -c nginx \
        --context "${CONTEXT}" -- \
        cat /var/run/secrets/nvidia.com/dsx/token) || return 1
    uid=$(kubectl get pod "${POD_NAME}" -n "${POD_NAMESPACE}" --context "${CONTEXT}" \
        -o jsonpath='{.metadata.uid}') || return 1
    expected_subject="spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/e2e-nginx/pod/${uid}"
    encoded=$(cut -d. -f2 <<<"${token}")
    padding=$(( (4 - ${#encoded} % 4) % 4 ))
    encoded="${encoded}$(printf '%*s' "${padding}" '' | tr ' ' '=')"
    payload=$(tr '_-' '/+' <<<"${encoded}" | base64 -d 2>/dev/null) || return 1
    now=$(date +%s)
    jq -e --arg subject "${expected_subject}" --argjson now "${now}" '
      .iss == "spiffe://ds9.dsx.nvid.id" and
      .aud == ["spiffe://ds9.dsx.nvid.id/openbao"] and
      .sub == $subject and
      (.iat | type == "number") and
      (.exp | type == "number") and
      (.exp > .iat) and
      (.exp > $now) and
      ((.exp - .iat) <= 300)
    ' <<<"${payload}" >/dev/null || return 1

}

main() {
    log_info "DSX identity smoke test (context: ${CONTEXT})"
    echo

    # SPIRE
    check "SPIRE server running" \
        bash -c "kubectl get pods -n ${IDENTITY_NAMESPACE} -l app.kubernetes.io/name=server \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    check "SPIRE agent running" \
        bash -c "kubectl get pods -A -l app.kubernetes.io/name=agent \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    echo

    # DSX Event Bus and its mandatory OAuth2 path.
    check "Keycloak ready" \
        kubectl rollout status deployment/keycloak -n "${IDENTITY_NAMESPACE}" \
            --context "${CONTEXT}" --timeout=1s

    check "NATS Event Bus ready" \
        kubectl rollout status statefulset/nats -n "${IDENTITY_NAMESPACE}" \
            --context "${CONTEXT}" --timeout=1s

    check "Event Bus auth callout ready" \
        kubectl rollout status deployment/auth-callout -n "${IDENTITY_NAMESPACE}" \
            --context "${CONTEXT}" --timeout=1s

    check "Event Bus NACK controller ready" \
        kubectl rollout status deployment/nack -n "${IDENTITY_NAMESPACE}" \
            --context "${CONTEXT}" --timeout=1s

    check "Event Bus surveyor ready" \
        kubectl rollout status deployment/event-bus-surveyor -n "${IDENTITY_NAMESPACE}" \
            --context "${CONTEXT}" --timeout=1s

    check "Exchange ready with its retained MQTT subscription" \
        bash -c "kubectl get pods -n ${IDENTITY_NAMESPACE} -l app=spiffe-exchange \
            --context ${CONTEXT} -o jsonpath='{.items[0].status.conditions[?(@.type==\"Ready\")].status}' \
            2>/dev/null | grep -q True"

    check "Event Bus enforces OAuth and topic ACLs" event_bus_enforces_oauth

    check "Injected Pod receives its canonical exchanged identity" \
        pod_receives_exchanged_identity

    echo

    # Kyverno
    check "Kyverno running" \
        bash -c "kubectl get pods -n kyverno --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    echo
    echo "========================================"
    if [[ $FAIL -eq 0 ]]; then
        log_success "All ${PASS} checks passed"
    else
        log_error "${FAIL} check(s) failed, ${PASS} passed"
        exit 1
    fi
}

trap cleanup_port_forwards EXIT
trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
