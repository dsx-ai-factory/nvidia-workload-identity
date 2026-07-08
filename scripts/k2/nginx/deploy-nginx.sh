#!/bin/bash
# deploy-nginx.sh - Deploys the E2E nginx Pod to k2

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="dsx-identity-e2e"
POD_NAME="e2e-nginx"
NGINX_IMAGE="nginx:1.27.4-alpine"

source "${PROJECT_ROOT}/scripts/common.sh"

preload_images() {
    log_info "Preloading nginx images into cluster..."
    if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${NGINX_IMAGE}" "${CLUSTER_NAME}"; then
        log_error "Failed to load image: ${NGINX_IMAGE}"
        return 1
    fi
    log_success "Images preloaded"
}

deploy_nginx() {
    log_info "Deploying E2E nginx Pod..."
    kubectl apply -f "${SCRIPT_DIR}/e2e-nginx.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for pod..."
    if ! kubectl wait --for=condition=ready pod/"${POD_NAME}" -n "${NAMESPACE}" \
            --timeout=180s --context "${CONTEXT}"; then
        kubectl describe pod "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" || true
        kubectl logs "${POD_NAME}" -n "${NAMESPACE}" -c dsx-token-fetcher \
            --tail=100 --context "${CONTEXT}" || true
        kubectl logs "${POD_NAME}" -n "${NAMESPACE}" -c dsx-token-fetcher \
            --previous --tail=100 --context "${CONTEXT}" || true
        return 1
    fi
    
    log_success "E2E nginx deployed"
}

display_info() {
    log_success "E2E nginx deployment complete"
    echo
    echo "Pod:"
    kubectl get pod "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Check token:"
    echo "kubectl exec ${POD_NAME} -c nginx --context ${CONTEXT} -- cat /var/run/secrets/nvidia.com/dsx/token"
}

cleanup() {
    log_info "Removing E2E nginx..."
    kubectl delete namespace "${NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found=true
    log_success "E2E nginx fixture removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            deploy_nginx
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
