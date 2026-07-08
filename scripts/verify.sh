#!/bin/bash
# verify.sh - Verifies deployed k1 and k2 components

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/common.sh"

K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"

spire_server_agent() {
    kubectl exec statefulset/dsx-identity-server -n dsx-identity \
        --context "${K2_CONTEXT}" -c spire-server -- \
        /opt/spire/bin/spire-server agent "$@"
}

verify_dsx_imds_attestation() {
    log_info "Verifying the deployed dsx_imds host agents..."

    local pods count index host_id expected_id agent serial
    declare -A old_serials=()
    pods=$(kubectl get pods -n dsx-identity \
        -l identity.dsx.nvidia.com/host-agent=dsx-imds \
        --context "${K2_CONTEXT}" -o json) || return 1
    count=$(jq '.items | length' <<<"${pods}")
    if [[ "${count}" -lt 1 ]]; then
        log_error "No dsx_imds host-agent pods are deployed"
        return 1
    fi
    for ((index = 0; index < count; index++)); do
        host_id=$(jq -er ".items[${index}].spec.nodeName" <<<"${pods}") || return 1
        expected_id="spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/${host_id}"
        agent=$(spire_server_agent show -spiffeID "${expected_id}" -output json) || return 1
        serial=$(jq -er '.x509svid_serial_number' <<<"${agent}") || return 1
        old_serials["${host_id}"]="${serial}"
        if ! spire_server_agent evict -spiffeID "${expected_id}" >/dev/null; then
            log_error "Could not evict dsx_imds agent ${expected_id}"
            return 1
        fi
    done

    kubectl rollout restart daemonset/dsx-identity-dsx-imds-agent -n dsx-identity \
        --context "${K2_CONTEXT}" >/dev/null || return 1
    if ! kubectl rollout status daemonset/dsx-identity-dsx-imds-agent -n dsx-identity \
        --context "${K2_CONTEXT}" --timeout=300s >/dev/null; then
        kubectl logs daemonset/dsx-identity-dsx-imds-agent -n dsx-identity -c metadata-service \
            --tail=100 --context "${K2_CONTEXT}" || true
        kubectl logs daemonset/dsx-identity-dsx-imds-agent -n dsx-identity -c upstream-spire-agent \
            --tail=100 --context "${K2_CONTEXT}" || true
        kubectl logs statefulset/dsx-identity-server -n dsx-identity -c spire-server \
            --tail=100 --context "${K2_CONTEXT}" || true
        return 1
    fi

    pods=$(kubectl get pods -n dsx-identity \
        -l identity.dsx.nvidia.com/host-agent=dsx-imds \
        --context "${K2_CONTEXT}" -o json) || return 1
    count=$(jq '.items | length' <<<"${pods}")
    if [[ "${count}" -lt 1 ]]; then
        log_error "No dsx_imds host-agent pods are deployed"
        return 1
    fi
    for ((index = 0; index < count; index++)); do
        host_id=$(jq -er ".items[${index}].spec.nodeName" <<<"${pods}") || return 1
        expected_id="spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/${host_id}"
        agent=$(spire_server_agent show -spiffeID "${expected_id}" -output json) || return 1
        serial=$(jq -er '.x509svid_serial_number' <<<"${agent}") || return 1
        if ! jq -e --arg id "${expected_id}" '
            ("spiffe://" + .id.trust_domain + .id.path) == $id and
            .attestation_type == "dsx_imds" and
            .can_reattest == true
        ' <<<"${agent}" >/dev/null; then
            log_error "dsx_imds agent registered with an unexpected identity"
            printf '%s\n' "${agent}" >&2
            return 1
        fi
        if [[ "${serial}" == "${old_serials[${host_id}]}" ]]; then
            log_error "dsx_imds agent ${expected_id} did not receive a new SVID after eviction"
            return 1
        fi
    done

    log_success "All deployed host agents attested through metadata-service and dsx_imds"
}

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
    if kubectl rollout status statefulset/dsx-identity-server -n dsx-identity \
        --context "${K1_CONTEXT}" --timeout=1s >/dev/null 2>&1; then
        log_success "k1: SPIRE server running"
    else
        log_error "k1: SPIRE server not running"
        return 1
    fi
    
    # OpenBao
    if kubectl wait --for=condition=Ready pod -n openbao -l app.kubernetes.io/name=openbao \
        --context "${K1_CONTEXT}" --timeout=1s >/dev/null 2>&1; then
        log_success "k1: OpenBao running"
    else
        log_error "k1: OpenBao not running"
        return 1
    fi
    
    # Exchange Service
    if kubectl rollout status deployment/spiffe-exchange -n dsx-identity \
        --context "${K1_CONTEXT}" --timeout=1s >/dev/null 2>&1; then
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
    if kubectl rollout status statefulset/dsx-identity-server -n dsx-identity \
        --context "${K2_CONTEXT}" --timeout=1s >/dev/null 2>&1; then
        log_success "k2: SPIRE server running"
    else
        log_error "k2: SPIRE server not running"
        return 1
    fi

    # Local Exchange readiness includes OAuth-authenticated key synchronization
    # with the k1 Event Bus.
    if kubectl rollout status deployment/spiffe-exchange -n dsx-identity \
        --context "${K2_CONTEXT}" --timeout=1s >/dev/null 2>&1; then
        log_success "k2: Exchange service running and synchronized"
    else
        log_error "k2: Exchange service not ready"
        return 1
    fi

    # KubeVirt
    if kubectl wait --for=condition=Available kubevirt/kubevirt -n kubevirt \
        --context "${K2_CONTEXT}" --timeout=1s >/dev/null 2>&1; then
        log_success "k2: KubeVirt ready"
    else
        log_error "k2: KubeVirt not ready"
        return 1
    fi
    
    # Kyverno
    if kubectl get deployments -n kyverno --context "${K2_CONTEXT}" -o json 2>/dev/null | \
        jq -e '.items | length > 0 and all(.[]; (.status.availableReplicas // 0) == .spec.replicas)' >/dev/null; then
        log_success "k2: Kyverno running"
    else
        log_error "k2: Kyverno not ready"
        return 1
    fi
    
    log_success "k2 cluster verified"
}

main() {
    log_info "Starting multi-cluster verification..."
    echo
    
    local failed=0
    
    verify_k1 || failed=$((failed + 1))
    echo
    verify_k2 || failed=$((failed + 1))
    echo
    verify_dsx_imds_attestation || failed=$((failed + 1))
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
