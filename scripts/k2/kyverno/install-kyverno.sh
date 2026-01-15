#!/bin/bash
# install-kyverno.sh - Installs Kyverno for k2 cluster
# Note: Helm repo setup is in scripts/setup-helm-repos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
KYVERNO_VERSION="${KYVERNO_VERSION:-3.6.1}"

source "${PROJECT_ROOT}/scripts/common.sh"

preload_images() {
    log_info "Extracting images from Kyverno Helm chart..."
    
    # Template the chart to extract image references
    local template_output
    template_output=$(helm template kyverno/kyverno \
        --version "${KYVERNO_VERSION}" \
        --set admissionController.replicas=1 \
        --set backgroundController.replicas=1 \
        --set cleanupController.replicas=1 \
        --set reportsController.replicas=1 \
        --namespace kyverno 2>/dev/null)
    
    # Extract unique image references (image: field in YAML)
    local images
    images=$(echo "$template_output" | grep -E '^\s+image:\s+' | \
        awk '{print $2}' | tr -d '"' | sort -u)
    
    if [[ -z "$images" ]]; then
        log_info "No images found in Helm chart, skipping preload"
        return 0
    fi
    
    log_info "Preloading Kyverno images into cluster..."
    while IFS= read -r img; do
        [[ -z "$img" ]] && continue
        if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "$img" "${CLUSTER_NAME}"; then
            log_error "Failed to load image: $img"
            return 1
        fi
    done <<< "$images"
    
    log_success "Images preloaded"
}

install_kyverno() {
    log_info "Installing Kyverno ${KYVERNO_VERSION}..."
    
    helm upgrade --install kyverno kyverno/kyverno \
        --namespace kyverno \
        --create-namespace \
        --version "${KYVERNO_VERSION}" \
        --set admissionController.replicas=1 \
        --set backgroundController.replicas=1 \
        --set cleanupController.replicas=1 \
        --set reportsController.replicas=1 \
        --kube-context "${CONTEXT}" \
        --wait \
        --timeout 5m
    
    log_success "Kyverno installed"
}

display_info() {
    log_success "Kyverno installation complete"
    echo
    echo "Kyverno pods:"
    kubectl get pods -n kyverno --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing Kyverno..."
    helm uninstall kyverno --namespace kyverno --kube-context "${CONTEXT}" || true
    kubectl delete namespace kyverno --context "${CONTEXT}" --ignore-not-found=true
    log_success "Kyverno removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            install_kyverno
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

