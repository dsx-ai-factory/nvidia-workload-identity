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

METALLB_MANIFEST=""

preload_images() {
    log_info "Preloading MetalLB images into cluster..."
    preload_manifest_images "${CLUSTER_NAME}" <<<"${METALLB_MANIFEST}"
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
    
    kubectl apply -f - --context "${CONTEXT}" <<<"${METALLB_MANIFEST}"
    
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
    METALLB_MANIFEST=$(curl -fsSL \
        "https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml")
    preload_images
    install_metallb
    configure_ip_pool
    display_info
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main
