#!/bin/bash
# install-envoy-gw.sh - Installs Envoy Gateway for a Kind cluster
# Usage: install-envoy-gw.sh <cluster-name>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENVOY_GW_VERSION="v1.5.4"

CLUSTER_NAME="${1:-}"

if [[ -z "$CLUSTER_NAME" ]]; then
    echo "Usage: $0 <cluster-name>" >&2
    exit 1
fi

CONTEXT="kind-${CLUSTER_NAME}"

source "${PROJECT_ROOT}/scripts/common.sh"

preload_images() {
    log_info "Preloading Envoy Gateway images into cluster..."
    local manifest_output
    manifest_output=$(helm template eg oci://docker.io/envoyproxy/gateway-helm \
        --version "${ENVOY_GW_VERSION}" \
        --namespace envoy-gateway-system)
    preload_manifest_images "${CLUSTER_NAME}" <<<"${manifest_output}"
    log_success "Images preloaded"
}

install_crds() {
    log_info "Installing Envoy Gateway CRDs..."

    local crds name
    crds=$(helm template eg-crds oci://docker.io/envoyproxy/gateway-crds-helm \
        --version "${ENVOY_GW_VERSION}" \
        --set crds.gatewayAPI.enabled=true \
        --set crds.gatewayAPI.channel=experimental \
        --set crds.envoyGateway.enabled=true)
    kubectl apply --server-side -f - --context "${CONTEXT}" <<<"${crds}"
    # Wait for CRD discovery before applying Gateway resources.
    while read -r name; do
        kubectl wait --for=condition=Established "crd/${name}" \
            --timeout=120s --context "${CONTEXT}"
    done < <(yq eval-all -o=json -I=0 \
        'select(.kind == "CustomResourceDefinition") | {"name": .metadata.name}' \
        <<<"${crds}" | jq -r '.name')

    log_success "Envoy Gateway CRDs installed"
}

install_envoy_gateway() {
    log_info "Installing Envoy Gateway ${ENVOY_GW_VERSION}..."
    
    helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
        --version "${ENVOY_GW_VERSION}" \
        --namespace envoy-gateway-system \
        --create-namespace \
        --skip-crds \
        --kube-context "${CONTEXT}"

    log_success "Envoy Gateway submitted"
}

apply_gateway_resources() {
    log_info "Applying Gateway resources..."
    kubectl apply -f "${PROJECT_ROOT}/scripts/${CLUSTER_NAME}/gateway/gateway-class.yaml" \
        --context "${CONTEXT}"
    kubectl apply -f "${PROJECT_ROOT}/scripts/${CLUSTER_NAME}/gateway/gateway.yaml" \
        --context "${CONTEXT}"
}

wait_for_gateway() {
    kubectl rollout status deployment/envoy-gateway \
        --namespace envoy-gateway-system --timeout=5m --context "${CONTEXT}"
    kubectl wait --for=condition=Programmed "gateway/${CLUSTER_NAME}-gateway" \
        -n envoy-gateway-system --timeout=120s --context "${CONTEXT}"
    log_success "Envoy Gateway is ready"
}

display_info() {
    log_success "Envoy Gateway setup complete"
    echo
    echo "Envoy Gateway pods:"
    kubectl get pods -n envoy-gateway-system --context "${CONTEXT}"
}

main() {
    validate_context_exists "${CONTEXT}"
    preload_images
    install_crds
    install_envoy_gateway
    apply_gateway_resources
    wait_for_gateway
    display_info
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main
