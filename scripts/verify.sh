#!/bin/bash
# verify.sh - Verifies k1 and k2 clusters and workloads

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/common.sh"

K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"

get_k1_gateway_ip() {
    kubectl get gateway k1-gateway -n envoy-gateway-system \
        -o jsonpath='{.status.addresses[0].value}' \
        --context "${K1_CONTEXT}" 2>/dev/null || echo ""
}

verify_k1() {
    log_info "Verifying k1 cluster..."
    
    if ! is_k1_ready; then
        log_error "k1 cluster is not available"
        return 1
    fi
    
    log_info "Checking k1 components..."
    
    # SPIRE
    if kubectl get pods -n spire-system -l app.kubernetes.io/name=server \
        --context "${K1_CONTEXT}" --no-headers 2>/dev/null | grep -q Running; then
        log_success "k1: SPIRE server running"
    else
        log_error "k1: SPIRE server not running"
        return 1
    fi
    
    # OpenBao
    if kubectl get pods -n bao -l app.kubernetes.io/name=openbao \
        --context "${K1_CONTEXT}" --no-headers 2>/dev/null | grep -q Running; then
        log_success "k1: OpenBao running"
    else
        log_error "k1: OpenBao not running"
        return 1
    fi
    
    # Exchange Service
    if kubectl get pods -l app=spiffe-exchange-service \
        --context "${K1_CONTEXT}" --no-headers 2>/dev/null | grep -q Running; then
        log_success "k1: Exchange service running"
    else
        log_error "k1: Exchange service not running"
        return 1
    fi
    
    # Gateway
    local gateway_ip
    gateway_ip=$(get_k1_gateway_ip)
    if [[ -n "$gateway_ip" ]]; then
        log_success "k1: Gateway IP available (${gateway_ip})"
    else
        log_error "k1: Gateway IP not available"
        return 1
    fi
    
    log_success "k1 cluster verified"
}

verify_k2() {
    log_info "Verifying k2 cluster..."
    
    if ! is_k2_ready; then
        log_error "k2 cluster is not available"
        return 1
    fi
    
    log_info "Checking k2 components..."
    
    # SPIRE
    if kubectl get pods -n spire-system -l app.kubernetes.io/name=server \
        --context "${K2_CONTEXT}" --no-headers 2>/dev/null | grep -q Running; then
        log_success "k2: SPIRE server running"
    else
        log_error "k2: SPIRE server not running"
        return 1
    fi
    
    # KubeVirt
    if kubectl get kubevirt -n kubevirt --context "${K2_CONTEXT}" \
        --no-headers 2>/dev/null | grep -q .; then
        log_success "k2: KubeVirt installed"
    else
        log_warning "k2: KubeVirt not installed"
    fi
    
    # Kyverno
    if kubectl get pods -n kyverno --context "${K2_CONTEXT}" \
        --no-headers 2>/dev/null | grep -q Running; then
        log_success "k2: Kyverno running"
    else
        log_warning "k2: Kyverno not running"
    fi
    
    log_success "k2 cluster verified"
}

main() {
    log_info "Starting multi-cluster verification..."
    echo
    
    local failed=0
    
    verify_k1 || ((failed++))
    echo
    verify_k2 || ((failed++))
    echo
    
    if [[ $failed -eq 0 ]]; then
        log_success "All cluster verification tests passed"
    else
        log_error "${failed} verification(s) failed"
        exit 1
    fi
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

