#!/bin/bash
# verify-spiffe.sh - Smoke test for SPIFFE stack (single-cluster, after make spiffe-deploy)
# Verifies: SPIRE server/agent/CSI, exchange service health, DB migration, CNPG, Kyverno

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

CONTEXT="kind-k1"
SPIFFE_NS="dsx-spiffe"
CNPG_NS="cnpg-shared"

PASS=0
FAIL=0

check() {
    local name="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        log_success "$name"
        PASS=$((PASS + 1))
    else
        log_error "$name"
        FAIL=$((FAIL + 1))
    fi
}

check_warn() {
    local name="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        log_success "$name"
        PASS=$((PASS + 1))
    else
        log_warning "$name (optional)"
        PASS=$((PASS + 1))
    fi
}

main() {
    log_info "SPIFFE stack smoke test (context: ${CONTEXT})"
    echo

    # SPIRE — umbrella chart deploys to SPIFFE_NS; individual chart deploys to spire-system
    check "SPIRE server running" \
        bash -c "kubectl get pods -n ${SPIFFE_NS} -l app.kubernetes.io/name=server \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running || \
            kubectl get pods -n spire-system -l app.kubernetes.io/name=server \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    check "SPIRE agent running" \
        bash -c "kubectl get pods -A -l app.kubernetes.io/name=agent \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    check "SPIFFE CSI driver registered" \
        kubectl get csidriver csi.spiffe.io --context "${CONTEXT}"

    check "ClusterSPIFFEIDs created" \
        kubectl get clusterspiffeids --context "${CONTEXT}" --no-headers -o name

    echo

    # Exchange
    check "Exchange pod running" \
        bash -c "kubectl get pods -n ${SPIFFE_NS} -l app=spiffe-exchange \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    check "Exchange pod ready (health probe passing)" \
        bash -c "kubectl get pods -n ${SPIFFE_NS} -l app=spiffe-exchange \
            --context ${CONTEXT} -o jsonpath='{.items[0].status.conditions[?(@.type==\"Ready\")].status}' \
            2>/dev/null | grep -q True"

    check "DB migration completed" \
        bash -c "kubectl get jobs -n ${SPIFFE_NS} --context ${CONTEXT} \
            --no-headers 2>/dev/null | grep -i migration | grep -q '1/1'"

    echo

    # PostgreSQL
    check "Shared CNPG cluster healthy" \
        bash -c "kubectl get cluster shared-pg-cluster -n ${CNPG_NS} \
            --context ${CONTEXT} --no-headers 2>/dev/null | grep -qi healthy"

    echo

    # Kyverno
    check "Kyverno running" \
        bash -c "kubectl get pods -n kyverno --context ${CONTEXT} --no-headers 2>/dev/null | grep -q Running"

    # Workload-support policy is optional (chart template is WIP)
    check_warn "Workload-support policy exists" \
        bash -c "kubectl get clusterpolicy --context ${CONTEXT} --no-headers 2>/dev/null | grep -qi spiffe"

    echo
    echo "========================================"
    if [[ $FAIL -eq 0 ]]; then
        log_success "All ${PASS} checks passed"
    else
        log_error "${FAIL} check(s) failed, ${PASS} passed"
        exit 1
    fi
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
