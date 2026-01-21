#!/bin/bash
# deploy-nginx.sh - Deploys demo nginx pod to k2 cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="default"
POD_NAME="demo-nginx"
IMAGE_NAME="spiffe-token-fetcher"
IMAGE_TAG="latest"
NGINX_IMAGE="nginx:alpine"

source "${PROJECT_ROOT}/scripts/common.sh"

build_image() {
    log_info "Building SPIFFE token fetcher image..."
    docker build -f "${PROJECT_ROOT}/Dockerfile.spiffe-token-fetcher" \
        -t "${IMAGE_NAME}:${IMAGE_TAG}" "${PROJECT_ROOT}"
    log_success "Image built"
}

preload_images() {
    log_info "Preloading nginx images into cluster..."
    if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${NGINX_IMAGE}" "${CLUSTER_NAME}"; then
        log_error "Failed to load image: ${NGINX_IMAGE}"
        return 1
    fi
    log_success "Images preloaded"
}

load_image() {
    log_info "Loading image into Kind cluster..."
    kind load docker-image "${IMAGE_NAME}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    log_success "Image loaded"
}

deploy_nginx() {
    # Delete existing pod
    kubectl delete pod "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found=true
    
    log_info "Deploying demo nginx pod..."
    kubectl apply -f "${SCRIPT_DIR}/demo-nginx.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for pod..."
    kubectl wait --for=condition=ready pod/"${POD_NAME}" -n "${NAMESPACE}" \
        --timeout=120s --context "${CONTEXT}"
    
    log_success "Demo nginx deployed"
}

verify_token() {
    log_info "Verifying SPIFFE token..."
    
    local timeout=60
    local count=0
    
    while [[ $count -lt $timeout ]]; do
        if kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
            test -f /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null; then
            log_success "SPIFFE token found"
            return 0
        fi
        sleep 2
        ((count+=2))
    done
    
    log_error "Token not found after ${timeout}s"
    log_info "Pod logs (all containers):"
    kubectl logs "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" --all-containers --tail=30 2>/dev/null || true
    return 1
}

display_info() {
    log_success "Demo nginx deployment complete"
    echo
    echo "Pod:"
    kubectl get pod "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Check token:"
    echo "kubectl exec ${POD_NAME} -c nginx --context ${CONTEXT} -- cat /var/run/secrets/nvidia.com/spiffe/token"
}

cleanup() {
    log_info "Removing demo nginx..."
    kubectl delete pod "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found=true
    log_success "Demo nginx removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            build_image
            load_image
            preload_images
            deploy_nginx
            verify_token
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
