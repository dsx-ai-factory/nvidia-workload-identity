#!/bin/bash
# deploy-spire.sh - Deploys SPIRE server to k1 cluster with federation support
# Note: Helm repo setup is in scripts/setup-helm-repos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
CONTEXT="kind-${CLUSTER_NAME}"
SPIRE_NAMESPACE="spire-system"
SPIRE_CRDS_VERSION="0.5.0"
SPIRE_VERSION="0.27.1"

source "${PROJECT_ROOT}/scripts/common.sh"

install_spire() {
    log_info "Installing SPIRE CRDs..."
    helm upgrade --install spire-crds spiffe/spire-crds \
        --namespace "${SPIRE_NAMESPACE}" \
        --create-namespace \
        --version "${SPIRE_CRDS_VERSION}" \
        --kube-context "${CONTEXT}" \
        --timeout 5m \
        --wait
    
    log_info "Installing SPIRE..."
    helm upgrade --install spire spiffe/spire \
        --namespace "${SPIRE_NAMESPACE}" \
        --version "${SPIRE_VERSION}" \
        --values "${SCRIPT_DIR}/spire-values.yaml" \
        --kube-context "${CONTEXT}" \
        --timeout 10m \
        --wait
    
    log_success "SPIRE installed"
}

display_info() {
    log_success "SPIRE deployment complete"
    echo
    echo "Pods:"
    kubectl get pods -n "${SPIRE_NAMESPACE}" --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing SPIRE..."
    helm uninstall spire --namespace "${SPIRE_NAMESPACE}" --kube-context "${CONTEXT}" || true
    helm uninstall spire-crds --namespace "${SPIRE_NAMESPACE}" --kube-context "${CONTEXT}" || true
    kubectl delete namespace "${SPIRE_NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true
    log_success "SPIRE removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            install_spire
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
