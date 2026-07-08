#!/bin/bash
# install-kubevirt.sh - Installs KubeVirt for the k2 VM E2E path

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
KUBEVIRT_VERSION="${KUBEVIRT_VERSION:-v1.7.4}"

source "${PROJECT_ROOT}/scripts/common.sh"

KUBEVIRT_OPERATOR=""
KUBEVIRT_CR=""

load_manifests() {
    KUBEVIRT_OPERATOR=$(curl -fsSL \
        "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-operator.yaml")
    KUBEVIRT_CR=$(curl -fsSL \
        "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-cr.yaml")
    # Kind runners need software emulation for nested virtualization.
    KUBEVIRT_CR=$(yq '.spec.configuration.developerConfiguration.useEmulation = true' \
        <<<"${KUBEVIRT_CR}")
}

preload_images() {
    log_info "Extracting images from KubeVirt manifests..."
    preload_manifest_images "${CLUSTER_NAME}" <<<"${KUBEVIRT_OPERATOR}"
    log_success "Images preloaded"
}

install_kubevirt() {
    log_info "Installing KubeVirt ${KUBEVIRT_VERSION}..."
    
    kubectl apply -f - --context "${CONTEXT}" <<<"${KUBEVIRT_OPERATOR}"
    kubectl apply -f - --context "${CONTEXT}" <<<"${KUBEVIRT_CR}"

    log_info "Waiting for KubeVirt..."
    kubectl wait --for=condition=Available kubevirt kubevirt -n kubevirt \
        --timeout=600s --context "${CONTEXT}"
    
    log_success "KubeVirt installed"
}

display_info() {
    log_success "KubeVirt installation complete"
    echo
    echo "KubeVirt:"
    kubectl get kubevirt -n kubevirt --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing KubeVirt..."
    kubectl delete -f - --context "${CONTEXT}" --ignore-not-found=true <<<"${KUBEVIRT_CR}"
    kubectl delete -f - --context "${CONTEXT}" --ignore-not-found=true <<<"${KUBEVIRT_OPERATOR}"
    log_success "KubeVirt removed"
}

main() {
    case "${1:-}" in
        cleanup)
            load_manifests
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            load_manifests
            preload_images
            install_kubevirt
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
