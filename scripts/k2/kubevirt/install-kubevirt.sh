#!/bin/bash
# install-kubevirt.sh - Installs KubeVirt and CDI for k2 cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
KUBEVIRT_VERSION="${KUBEVIRT_VERSION:-v1.5.1}"
CDI_VERSION="${CDI_VERSION:-v1.63.0}"

source "${PROJECT_ROOT}/scripts/common.sh"

# Cached manifest paths
KUBEVIRT_OPERATOR_CACHE="/tmp/kubevirt-${KUBEVIRT_VERSION}-operator.yaml"
KUBEVIRT_CR_CACHE="/tmp/kubevirt-${KUBEVIRT_VERSION}-cr.yaml"
CDI_OPERATOR_CACHE="/tmp/cdi-${CDI_VERSION}-operator.yaml"
CDI_CR_CACHE="/tmp/cdi-${CDI_VERSION}-cr.yaml"

get_kubevirt_operator() {
    if [[ ! -f "$KUBEVIRT_OPERATOR_CACHE" ]]; then
        curl -sL "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-operator.yaml" > "$KUBEVIRT_OPERATOR_CACHE"
    fi
    cat "$KUBEVIRT_OPERATOR_CACHE"
}

get_kubevirt_cr() {
    if [[ ! -f "$KUBEVIRT_CR_CACHE" ]]; then
        curl -sL "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-cr.yaml" > "$KUBEVIRT_CR_CACHE"
    fi
    cat "$KUBEVIRT_CR_CACHE"
}

get_cdi_operator() {
    if [[ ! -f "$CDI_OPERATOR_CACHE" ]]; then
        curl -sL "https://github.com/kubevirt/containerized-data-importer/releases/download/${CDI_VERSION}/cdi-operator.yaml" > "$CDI_OPERATOR_CACHE"
    fi
    cat "$CDI_OPERATOR_CACHE"
}

get_cdi_cr() {
    if [[ ! -f "$CDI_CR_CACHE" ]]; then
        curl -sL "https://github.com/kubevirt/containerized-data-importer/releases/download/${CDI_VERSION}/cdi-cr.yaml" > "$CDI_CR_CACHE"
    fi
    cat "$CDI_CR_CACHE"
}

preload_images() {
    log_info "Extracting images from KubeVirt manifests..."
    
    # Extract unique image references (image: field in YAML)
    local images
    images=$(echo -e "$(get_kubevirt_operator)\n$(get_cdi_operator)" | \
        grep -E '^\s+image:\s+' | awk '{print $2}' | tr -d '"' | sort -u)
    
    if [[ -z "$images" ]]; then
        log_info "No images found in manifests, skipping preload"
        return 0
    fi
    
    log_info "Preloading KubeVirt images into cluster..."
    while IFS= read -r img; do
        [[ -z "$img" ]] && continue
        if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "$img" "${CLUSTER_NAME}"; then
            log_error "Failed to load image: $img"
            return 1
        fi
    done <<< "$images"
    
    log_success "Images preloaded"
}

install_kubevirt() {
    log_info "Installing KubeVirt ${KUBEVIRT_VERSION}..."
    
    get_kubevirt_operator | kubectl apply -f - --context "${CONTEXT}"
    get_kubevirt_cr | kubectl apply -f - --context "${CONTEXT}"
    
    # Enable software emulation for nested virtualization
    kubectl patch kubevirt kubevirt -n kubevirt --type=merge \
        -p '{"spec":{"configuration":{"developerConfiguration":{"useEmulation":true}}}}' \
        --context "${CONTEXT}"
    
    # Configure l2bridge network binding for Primary UDN support
    log_info "Configuring l2bridge network binding..."
    kubectl patch kubevirt kubevirt -n kubevirt --type=merge \
        -p '{"spec":{"configuration":{"network":{"binding":{"l2bridge":{"domainAttachmentType":"managedTap","migration":{}}}}}}}' \
        --context "${CONTEXT}"
    
    log_info "Waiting for KubeVirt..."
    kubectl wait --for=condition=Available kubevirt kubevirt -n kubevirt \
        --timeout=600s --context "${CONTEXT}"
    
    log_success "KubeVirt installed"
}

install_cdi() {
    log_info "Installing CDI ${CDI_VERSION}..."
    
    get_cdi_operator | kubectl apply -f - --context "${CONTEXT}"
    get_cdi_cr | kubectl apply -f - --context "${CONTEXT}"
    
    log_info "Waiting for CDI..."
    kubectl wait --for=condition=Available cdi cdi -n cdi \
        --timeout=300s --context "${CONTEXT}"
    
    log_success "CDI installed"
}

display_info() {
    log_success "KubeVirt and CDI installation complete"
    echo
    echo "KubeVirt:"
    kubectl get kubevirt -n kubevirt --context "${CONTEXT}"
    echo
    echo "CDI:"
    kubectl get cdi -n cdi --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing KubeVirt and CDI..."
    get_cdi_cr | kubectl delete -f - --context "${CONTEXT}" --ignore-not-found=true
    get_cdi_operator | kubectl delete -f - --context "${CONTEXT}" --ignore-not-found=true
    get_kubevirt_cr | kubectl delete -f - --context "${CONTEXT}" --ignore-not-found=true
    get_kubevirt_operator | kubectl delete -f - --context "${CONTEXT}" --ignore-not-found=true
    log_success "KubeVirt and CDI removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            install_kubevirt
            install_cdi
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

