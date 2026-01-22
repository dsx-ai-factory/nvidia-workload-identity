#!/bin/bash
# deploy-postgres.sh - Deploys CloudNativePG operator and PostgreSQL cluster for exchange service

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="postgres-system"
CNPG_NAMESPACE="cnpg-system"
CNPG_CHART_VERSION="0.27.0"
CNPG_OPERATOR_IMAGE="ghcr.io/cloudnative-pg/cloudnative-pg:1.28.0"
POSTGRES_IMAGE="ghcr.io/cloudnative-pg/postgresql:16"

source "${PROJECT_ROOT}/scripts/common.sh"

preload_images() {
    log_info "Preloading CloudNativePG images..."
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${CNPG_OPERATOR_IMAGE}" "${CLUSTER_NAME}"
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${POSTGRES_IMAGE}" "${CLUSTER_NAME}"
    log_success "Images preloaded"
}

install_operator() {
    log_info "Installing CloudNativePG operator..."
    
    if helm status cnpg -n "${CNPG_NAMESPACE}" --kube-context "${CONTEXT}" &>/dev/null; then
        log_info "CloudNativePG operator already installed"
        return 0
    fi
    
    helm upgrade --install cnpg cnpg/cloudnative-pg \
        --version "${CNPG_CHART_VERSION}" \
        --namespace "${CNPG_NAMESPACE}" \
        --create-namespace \
        --kube-context "${CONTEXT}" \
        --wait \
        --timeout 120s
    
    log_success "CloudNativePG operator installed"
}

deploy_cluster() {
    log_info "Deploying PostgreSQL cluster..."
    
    kubectl create namespace "${NAMESPACE}" --context "${CONTEXT}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"
    
    kubectl apply -f "${SCRIPT_DIR}/cluster.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for PostgreSQL cluster to be ready..."
    kubectl wait --for=condition=Ready cluster/postgres \
        -n "${NAMESPACE}" --timeout=120s --context "${CONTEXT}"
    
    log_success "PostgreSQL cluster deployed"
}

display_info() {
    log_success "PostgreSQL deployment complete"
    echo
    echo "Cluster:"
    kubectl get cluster postgres -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Pods:"
    kubectl get pods -l cnpg.io/cluster=postgres -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Connection info:"
    echo "  Host: postgres-rw.${NAMESPACE}.svc.cluster.local"
    echo "  Port: 5432"
    echo "  Database: exchange"
    echo "  User: exchange"
    echo "  Secret: postgres-app (contains password)"
}

cleanup() {
    log_info "Removing PostgreSQL cluster..."
    kubectl delete -f "${SCRIPT_DIR}/cluster.yaml" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete namespace "${NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    log_info "Removing CloudNativePG operator..."
    helm uninstall cnpg -n "${CNPG_NAMESPACE}" --kube-context "${CONTEXT}" 2>/dev/null || true
    kubectl delete namespace "${CNPG_NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    log_success "PostgreSQL removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            install_operator
            deploy_cluster
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
