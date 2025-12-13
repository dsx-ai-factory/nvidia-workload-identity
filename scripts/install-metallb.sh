#!/bin/bash
# install-metallb.sh - Installs MetalLB for a Kind cluster
# Usage: install-metallb.sh <cluster-name> <ip-range-start> <ip-range-end>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
METALLB_VERSION="v0.14.9"

CLUSTER_NAME="${1:-}"
IP_START="${2:-200}"
IP_END="${3:-210}"

if [[ -z "$CLUSTER_NAME" ]]; then
    echo "Usage: $0 <cluster-name> [ip-range-start] [ip-range-end]" >&2
    exit 1
fi

CONTEXT="kind-${CLUSTER_NAME}"

source "${PROJECT_ROOT}/scripts/common.sh"

MANIFEST_CACHE="/tmp/metallb-${METALLB_VERSION}-manifest.yaml"

get_manifest() {
    if [[ ! -f "$MANIFEST_CACHE" ]]; then
        curl -sL "https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml" > "$MANIFEST_CACHE"
    fi
    cat "$MANIFEST_CACHE"
}

preload_images() {
    log_info "Preloading MetalLB images into cluster..."
    
    # Fetch the manifest and extract image references
    local manifest_output
    manifest_output=$(get_manifest)
    
    # Extract unique image references
    local images
    images=$(echo "$manifest_output" | grep -E '^\s+image:\s+' | \
        awk '{print $2}' | tr -d '"' | sort -u)
    
    if [[ -z "$images" ]]; then
        log_info "No images found in manifest, skipping preload"
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

get_docker_network_subnet() {
    local network_name="kind"
    local subnet
    subnet=$(docker network inspect "${network_name}" -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' | tr ' ' '\n' | grep -v ':' | head -1)
    echo "${subnet}" | cut -d'.' -f1-2
}

install_metallb() {
    log_info "Installing MetalLB ${METALLB_VERSION}..."
    
    get_manifest | kubectl apply -f - --context "${CONTEXT}"
    
    log_info "Waiting for MetalLB controller..."
    kubectl rollout status deployment/controller -n metallb-system \
        --timeout=120s --context "${CONTEXT}"
    
    log_success "MetalLB installed"
}

configure_ip_pool() {
    log_info "Configuring MetalLB IP address pool..."
    
    local subnet_prefix
    subnet_prefix=$(get_docker_network_subnet)
    
    log_info "Detected Docker network subnet prefix: ${subnet_prefix}"
    
    kubectl apply --context "${CONTEXT}" -f - <<EOF
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: ${CLUSTER_NAME}-pool
  namespace: metallb-system
spec:
  addresses:
  - ${subnet_prefix}.255.${IP_START}-${subnet_prefix}.255.${IP_END}
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: ${CLUSTER_NAME}-l2
  namespace: metallb-system
spec:
  ipAddressPools:
  - ${CLUSTER_NAME}-pool
EOF
    
    log_success "MetalLB IP pool configured: ${subnet_prefix}.255.${IP_START}-${IP_END}"
}

display_info() {
    log_success "MetalLB setup complete"
    echo
    echo "IP Pool:"
    kubectl get ipaddresspools -n metallb-system --context "${CONTEXT}"
}

main() {
    validate_context_exists "${CONTEXT}"
    preload_images
    install_metallb
    configure_ip_pool
    display_info
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main

