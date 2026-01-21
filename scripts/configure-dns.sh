#!/bin/bash
# configure-dns.sh - Configure cross-cluster DNS for both gateways

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

source "${PROJECT_ROOT}/scripts/common.sh"

K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"

# Get k1 gateway IP from Gateway resource
get_k1_gateway_ip() {
    kubectl get gateway k1-gateway -n envoy-gateway-system \
        -o jsonpath='{.status.addresses[0].value}' \
        --context "${K1_CONTEXT}" 2>/dev/null || echo ""
}

# Get k2 gateway IP from Gateway resource
get_k2_gateway_ip() {
    kubectl get gateway k2-gateway -n envoy-gateway-system \
        -o jsonpath='{.status.addresses[0].value}' \
        --context "${K2_CONTEXT}" 2>/dev/null || echo ""
}

# Adds a DNS entry to CoreDNS hosts plugin
# Usage: add_coredns_host <hostname> <ip> <context>
# Returns 0 if change was made, 1 if already configured
add_coredns_host() {
    local hostname="$1"
    local ip="$2"
    local context="$3"
    
    log_info "Configuring CoreDNS: ${hostname} -> ${ip}"
    
    local corefile
    corefile=$(kubectl get configmap coredns -n kube-system -o jsonpath='{.data.Corefile}' --context "${context}")
    
    # Check if already configured
    if echo "$corefile" | grep -q "${hostname}"; then
        log_info "${hostname} already configured in CoreDNS"
        return 1
    fi
    
    # Build modified Corefile
    local new_corefile
    if echo "$corefile" | grep -q "hosts {"; then
        # Add to existing hosts block - insert before fallthrough line
        new_corefile=$(echo "$corefile" | awk -v entry="        ${ip} ${hostname}" '
            /hosts \{/ { print; in_hosts=1; next }
            in_hosts && /fallthrough/ { print entry; print; in_hosts=0; next }
            { print }
        ')
    else
        # Add new hosts block before kubernetes block
        local hosts_block="hosts {\n        ${ip} ${hostname}\n        fallthrough\n    }"
        new_corefile=$(echo "$corefile" | awk -v hosts="$hosts_block" '/kubernetes cluster.local/{print hosts}1')
    fi
    
    kubectl create configmap coredns -n kube-system \
        --from-literal=Corefile="$new_corefile" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${context}"
    
    log_success "CoreDNS configured: ${hostname} -> ${ip}"
    return 0
}

# Configures gateway.sjc-1.nvidia.com DNS in a cluster
# Returns 0 if change was made, 1 if no change
configure_k1_gateway_dns() {
    local context="$1"
    
    local k1_gateway_ip
    k1_gateway_ip=$(get_k1_gateway_ip)
    
    if [[ -z "$k1_gateway_ip" ]]; then
        log_error "Could not get k1 gateway IP. Ensure k1 gateway is set up first."
        return 1
    fi
    
    add_coredns_host "gateway.sjc-1.nvidia.com" "$k1_gateway_ip" "$context"
}

# Configures gateway.k2.sjc-1.nvidia.com DNS in a cluster
# Returns 0 if change was made, 1 if no change
configure_k2_gateway_dns() {
    local context="$1"
    
    local k2_gateway_ip
    k2_gateway_ip=$(get_k2_gateway_ip)
    
    if [[ -z "$k2_gateway_ip" ]]; then
        log_error "Could not get k2 gateway IP. Ensure k2 gateway is set up first."
        return 1
    fi
    
    add_coredns_host "gateway.k2.sjc-1.nvidia.com" "$k2_gateway_ip" "$context"
}

main() {
    log_info "Configuring cross-cluster DNS..."
    
    local changes_made=false
    
    # Configure k1 to resolve both gateways
    if configure_k1_gateway_dns "${K1_CONTEXT}"; then
        changes_made=true
    fi
    if configure_k2_gateway_dns "${K1_CONTEXT}"; then
        changes_made=true
    fi
    
    # Configure k2 to resolve both gateways
    if configure_k1_gateway_dns "${K2_CONTEXT}"; then
        changes_made=true
    fi
    if configure_k2_gateway_dns "${K2_CONTEXT}"; then
        changes_made=true
    fi
    
    # Reload CoreDNS if changes were made
    if [[ "$changes_made" == "true" ]]; then
        log_info "Reloading CoreDNS in both clusters..."
        kubectl rollout restart deployment coredns -n kube-system --context "${K1_CONTEXT}" &
        kubectl rollout restart deployment coredns -n kube-system --context "${K2_CONTEXT}" &
        wait
        
        log_info "Waiting for CoreDNS rollouts to complete..."
        kubectl wait --for=condition=available deployment/coredns -n kube-system --timeout=60s --context "${K1_CONTEXT}" &
        kubectl wait --for=condition=available deployment/coredns -n kube-system --timeout=60s --context "${K2_CONTEXT}" &
        wait
    else
        log_info "No DNS changes needed"
    fi
    
    log_success "Cross-cluster DNS configured"
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
