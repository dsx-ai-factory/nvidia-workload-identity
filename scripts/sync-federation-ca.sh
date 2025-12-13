#!/bin/bash
# sync-federation-ca.sh - Copies k2's CA certificate to k1 for SPIRE federation
# Must run after cert-manager-k2 and before k1 spire

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

source "${PROJECT_ROOT}/scripts/common.sh"

K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"
CA_SECRET_SRC="k2-ca-secret"
CA_SECRET_DST="k2-federation-ca"

sync_ca() {
    log_info "Extracting CA from k2..."
    
    # Get CA cert from k2
    local ca_cert
    ca_cert=$(kubectl get secret "${CA_SECRET_SRC}" -n cert-manager \
        --context "${K2_CONTEXT}" \
        -o jsonpath='{.data.tls\.crt}' | base64 -d)
    
    if [[ -z "$ca_cert" ]]; then
        log_error "Failed to extract CA certificate from k2"
        return 1
    fi
    
    log_info "Creating CA Secret in k1..."
    
    # Create namespace if needed
    kubectl create namespace spire-system --context "${K1_CONTEXT}" --dry-run=client -o yaml | \
        kubectl apply --context "${K1_CONTEXT}" -f -
    
    # Create Secret in k1 with the CA cert
    kubectl create secret generic "${CA_SECRET_DST}" \
        --from-literal=ca.crt="${ca_cert}" \
        -n spire-system \
        --context "${K1_CONTEXT}" \
        --dry-run=client -o yaml | \
        kubectl apply --context "${K1_CONTEXT}" -f -
    
    log_success "CA synced from k2 to k1"
}

main() {
    sync_ca
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
