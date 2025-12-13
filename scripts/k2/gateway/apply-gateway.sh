#!/bin/bash
# apply-gateway.sh - Applies the Gateway resource for k2

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"

source "${PROJECT_ROOT}/scripts/common.sh"

apply_gateway() {
    log_info "Applying Gateway resource..."
    kubectl apply -f "${SCRIPT_DIR}/gateway.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for Gateway to be ready..."
    kubectl wait --for=condition=Programmed gateway/k2-gateway \
        -n envoy-gateway-system --timeout=120s --context "${CONTEXT}" || true
    
    log_success "Gateway ready"
}

main() {
    validate_context_exists "${CONTEXT}"
    apply_gateway
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
