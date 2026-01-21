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

MANIFEST_CACHE="/tmp/envoy-gw-${ENVOY_GW_VERSION}-manifest.yaml"
CHART_CACHE="/tmp/gateway-helm-${ENVOY_GW_VERSION}.tgz"
CRDS_CHART_CACHE="/tmp/gateway-crds-helm-${ENVOY_GW_VERSION}.tgz"

get_manifest() {
    if [[ ! -f "$MANIFEST_CACHE" ]]; then
        log_info "Caching Envoy Gateway manifest..." >&2
        helm template eg oci://docker.io/envoyproxy/gateway-helm \
            --version "${ENVOY_GW_VERSION}" \
            --namespace envoy-gateway-system > "$MANIFEST_CACHE" 2>/dev/null
    fi
    cat "$MANIFEST_CACHE"
}

get_chart() {
    if [[ ! -f "$CHART_CACHE" ]]; then
        log_info "Caching Envoy Gateway Helm chart..." >&2
        helm pull oci://docker.io/envoyproxy/gateway-helm \
            --version "${ENVOY_GW_VERSION}" \
            --destination "$(dirname "$CHART_CACHE")" >/dev/null 2>&1
        if [[ ! -f "$CHART_CACHE" ]]; then
            log_error "Failed to download Helm chart" >&2
            return 1
        fi
    fi
    echo "$CHART_CACHE"
}

get_crds_chart() {
    if [[ ! -f "$CRDS_CHART_CACHE" ]]; then
        log_info "Caching Envoy Gateway CRDs Helm chart..." >&2
        helm pull oci://docker.io/envoyproxy/gateway-crds-helm \
            --version "${ENVOY_GW_VERSION}" \
            --destination "$(dirname "$CRDS_CHART_CACHE")" >/dev/null 2>&1
        if [[ ! -f "$CRDS_CHART_CACHE" ]]; then
            log_error "Failed to download CRDs Helm chart" >&2
            return 1
        fi
    fi
    echo "$CRDS_CHART_CACHE"
}

preload_images() {
    log_info "Preloading Envoy Gateway images into cluster..."
    
    # Get cached manifest and extract image references
    local manifest_output
    manifest_output=$(get_manifest)
    
    # Extract unique image references
    local images
    images=$(echo "$manifest_output" | grep -E '^\s+image:\s+' | \
        awk '{print $2}' | tr -d '"' | sort -u)
    
    if [[ -z "$images" ]]; then
        log_info "No images found in Helm chart, skipping preload"
        return 0
    fi
    
    while IFS= read -r img; do
        [[ -z "$img" ]] && continue
        if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "$img" "${CLUSTER_NAME}"; then
            log_error "Failed to load image: $img"
            return 1
        fi
    done <<< "$images"
    
    log_success "Images preloaded"
}

install_crds() {
    log_info "Installing Envoy Gateway CRDs..."
    
    local chart_path
    chart_path=$(get_crds_chart)
    
    helm template eg-crds "$chart_path" \
        --set crds.gatewayAPI.enabled=true \
        --set crds.gatewayAPI.channel=experimental \
        --set crds.envoyGateway.enabled=true | \
        kubectl apply --server-side -f - --context "${CONTEXT}"
    
    log_success "Envoy Gateway CRDs installed"
}

install_envoy_gateway() {
    log_info "Installing Envoy Gateway ${ENVOY_GW_VERSION}..."
    
    local chart_path
    chart_path=$(get_chart)
    
    helm upgrade --install eg "$chart_path" \
        --namespace envoy-gateway-system \
        --create-namespace \
        --skip-crds \
        --kube-context "${CONTEXT}" \
        --wait \
        --timeout 5m
    
    kubectl wait --for=condition=available deployment/envoy-gateway \
        --namespace envoy-gateway-system \
        --timeout=5m \
        --context "${CONTEXT}"
    
    log_success "Envoy Gateway installed"
}

apply_gateway_class() {
    log_info "Applying GatewayClass..."
    
    kubectl apply --context "${CONTEXT}" -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
EOF
    
    log_success "GatewayClass applied"
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
    apply_gateway_class
    display_info
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main
