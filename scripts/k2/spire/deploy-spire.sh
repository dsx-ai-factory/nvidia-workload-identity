#!/bin/bash
# deploy-spire.sh - Deploys SPIRE server to k2 cluster with bundle endpoint

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
SPIRE_NAMESPACE="spire-system"
SPIRE_CRDS_VERSION="0.5.0"
SPIRE_VERSION="0.27.1"

source "${PROJECT_ROOT}/scripts/common.sh"

add_helm_repo() {
    if ! helm repo list | grep -q "spiffe"; then
        helm repo add spiffe https://spiffe.github.io/helm-charts-hardened/
        helm repo update >/dev/null
    fi
}

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

apply_bundle_route() {
    log_info "Applying SPIRE bundle endpoint route..."
    kubectl apply -f "${SCRIPT_DIR}/spire-bundle-route.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for TLSRoute to be accepted..."
    kubectl wait --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True \
        tlsroute/spire-bundle-route -n "${SPIRE_NAMESPACE}" --timeout=60s --context "${CONTEXT}"
    
    log_success "Bundle route accepted"
}

display_info() {
    log_success "SPIRE deployment complete"
    echo
    echo "Pods:"
    kubectl get pods -n "${SPIRE_NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Bundle endpoint exposed on port 8443"
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
            add_helm_repo
            install_spire
            apply_bundle_route
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
