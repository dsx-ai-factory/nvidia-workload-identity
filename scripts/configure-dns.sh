#!/bin/bash
# configure-dns.sh - Resolve the k1 Gateway from the k2 shard.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

source "${PROJECT_ROOT}/scripts/common.sh"

K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"

# Adds a DNS entry to CoreDNS hosts plugin
# Usage: add_coredns_host <hostname> <ip> <context>
add_coredns_host() {
    local hostname="$1"
    local ip="$2"
    local context="$3"
    
    log_info "Configuring CoreDNS: ${hostname} -> ${ip}"
    
    local corefile
    corefile=$(kubectl get configmap coredns -n kube-system -o jsonpath='{.data.Corefile}' --context "${context}")
    
    # Check if the current address is already configured.
    if awk -v host="${hostname}" -v address="${ip}" \
        '$1 == address && $2 == host { found=1 } END { exit !found }' <<<"${corefile}"; then
        log_info "${hostname} already configured in CoreDNS"
        return 0
    fi
    
    # Build modified Corefile
    local new_corefile
    if awk -v host="${hostname}" '$2 == host { found=1 } END { exit !found }' <<<"${corefile}"; then
        new_corefile=$(awk -v host="${hostname}" -v address="${ip}" '
            $2 == host { print "        " address " " host; next }
            { print }
        ' <<<"${corefile}")
    elif grep -q "hosts {" <<<"${corefile}"; then
        new_corefile=$(awk -v entry="        ${ip} ${hostname}" '
            /hosts \{/ { print; in_hosts=1; inserted=0; next }
            in_hosts && /fallthrough/ { print entry; inserted=1 }
            in_hosts && /^[[:space:]]*}/ {
                if (!inserted) print entry
                in_hosts=0
            }
            { print }
        ' <<<"${corefile}")
    else
        # Add new hosts block before kubernetes block
        local hosts_block="hosts {\n        ${ip} ${hostname}\n        fallthrough\n    }"
        new_corefile=$(echo "$corefile" | awk -v hosts="$hosts_block" '/kubernetes cluster.local/{print hosts}1')
    fi
    
    kubectl create configmap coredns -n kube-system \
        --from-literal=Corefile="$new_corefile" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${context}"

    log_success "CoreDNS configured: ${hostname} -> ${ip}"
}

main() {
    log_info "Configuring k2 DNS for the k1 Gateway..."

    local k1_gateway_ip
    k1_gateway_ip=$(get_gateway_address \
        "${K1_CONTEXT}" envoy-gateway-system k1-gateway)
    add_coredns_host "gateway.sjc-1.nvidia.com" "${k1_gateway_ip}" "${K2_CONTEXT}"

    log_success "k2 DNS configured for the k1 Gateway"
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
