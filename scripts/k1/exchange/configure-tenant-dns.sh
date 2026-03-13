#!/bin/bash
# configure-tenant-dns.sh - Add CoreDNS wildcard rewrites for exchange service
# Required for multi-cluster demo: OpenBao JWT auth resolves tenant domains
# (e.g. tenant1.sjc-1.internal.nvidia.com) to the exchange service.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
CONTEXT="kind-${CLUSTER_NAME}"
EXCHANGE_FQDN="spiffe-exchange.dsx-spiffe.svc.cluster.local"

source "${PROJECT_ROOT}/scripts/common.sh"

configure_tenant_dns() {
    log_info "Configuring CoreDNS wildcard rewrite for tenant domains..."

    local region="sjc-1"
    local base_domain="nvidia.com"
    local services=("internal" "vmaas")

    local corefile
    corefile=$(kubectl get configmap coredns -n kube-system -o jsonpath='{.data.Corefile}' --context "${CONTEXT}")

    local changes_needed=false
    local rules=""

    for service in "${services[@]}"; do
        local service_domain="${region}.${service}.${base_domain}"
        local escaped_domain="${service_domain//./\\.}"
        local rewrite_rule="rewrite name regex .*\\.${escaped_domain} ${EXCHANGE_FQDN}"

        if ! echo "$corefile" | grep -qF "rewrite name regex .*\\.${escaped_domain}"; then
            log_info "Adding wildcard DNS rewrite: *.${service_domain} -> ${EXCHANGE_FQDN}"
            rules="${rules}    ${rewrite_rule}\n"
            changes_needed=true
        else
            log_info "Wildcard DNS rewrite already configured for *.${service_domain}"
        fi
    done

    if [[ "$changes_needed" != "true" ]]; then
        log_info "All service DNS rewrites already configured"
        return 0
    fi

    local new_corefile
    new_corefile=$(echo "$corefile" | awk -v rules="$rules" '/kubernetes cluster.local/{printf "%s", rules}1')

    kubectl create configmap coredns -n kube-system \
        --from-literal=Corefile="$new_corefile" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}"

    log_info "Reloading CoreDNS..."
    kubectl rollout restart deployment coredns -n kube-system --context "${CONTEXT}"
    kubectl rollout status deployment coredns -n kube-system --timeout=60s --context "${CONTEXT}"

    log_success "CoreDNS configured for service domains"
}

validate_context_exists "${CONTEXT}"
configure_tenant_dns
