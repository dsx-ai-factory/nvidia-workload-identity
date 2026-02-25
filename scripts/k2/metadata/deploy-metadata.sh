#!/bin/bash
# deploy-metadata.sh - Deploys MDS infrastructure to k2 cluster
# This sets up:
# - Tenant namespace with Primary UDN for VM isolation
# - Kyverno policies for MDS sidecar injection

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
TENANT="${METADATA_TENANT:-tenant1}"
TENANT_NAMESPACE="tenant-${TENANT}"
TENANT_SUBNET_OCTET="${TENANT_SUBNET_OCTET:-1}"
MDS_NETWORK_SETUP_IMAGE="mds-network-setup"
METADATA_SERVICE_IMAGE="metadata-service"
EXCHANGE_PROXY_CSI_IMAGE="exchange-proxy-csi"
IMAGE_TAG="latest"
EXCHANGE_SERVICE_HOST="gateway.sjc-1.nvidia.com"
EXCHANGE_SERVICE_PORT="8080"
SOCAT_IMAGE="alpine/socat:1.8.0.3"
CSI_NODE_DRIVER_REGISTRAR_IMAGE="registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.12.0"

source "${PROJECT_ROOT}/scripts/common.sh"

build_images() {
    log_info "Building MDS network setup image..."
    docker build -t "${MDS_NETWORK_SETUP_IMAGE}:${IMAGE_TAG}" \
        -f "${PROJECT_ROOT}/Dockerfile.mds-network-setup" "${PROJECT_ROOT}"
    
    log_info "Building metadata service image..."
    docker build -t "${METADATA_SERVICE_IMAGE}:${IMAGE_TAG}" \
        -f "${PROJECT_ROOT}/Dockerfile.metadata-service" "${PROJECT_ROOT}"
    
    log_info "Building exchange-proxy-csi image..."
    docker build -t "${EXCHANGE_PROXY_CSI_IMAGE}:${IMAGE_TAG}" \
        -f "${PROJECT_ROOT}/Dockerfile.exchange-proxy-csi" "${PROJECT_ROOT}"
    
    log_success "Images built"
}

load_images() {
    log_info "Loading images into Kind cluster..."
    # Load locally built images
    kind load docker-image "${MDS_NETWORK_SETUP_IMAGE}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    kind load docker-image "${METADATA_SERVICE_IMAGE}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    kind load docker-image "${EXCHANGE_PROXY_CSI_IMAGE}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    # Load external images
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${SOCAT_IMAGE}" "${CLUSTER_NAME}"
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${CSI_NODE_DRIVER_REGISTRAR_IMAGE}" "${CLUSTER_NAME}"
    log_success "Images loaded"
}

deploy_tenant_namespace() {
    log_info "Deploying tenant namespace..."
    
    local config
    config=$(cat "${PROJECT_ROOT}/scripts/k2/vm/tenant-namespace.yaml.tmpl")
    config="${config//TENANT_PLACEHOLDER/$TENANT}"
    
    echo "$config" | kubectl apply -f - --context "${CONTEXT}"
    
    log_success "Tenant namespace deployed"
}

deploy_tenant_udn() {
    log_info "Deploying tenant UDN..."
    
    local config
    config=$(cat "${PROJECT_ROOT}/scripts/k2/vm/tenant-udn.yaml.tmpl")
    config="${config//TENANT_PLACEHOLDER/$TENANT}"
    config="${config//TENANT_SUBNET_OCTET/$TENANT_SUBNET_OCTET}"
    
    echo "$config" | kubectl apply -f - --context "${CONTEXT}"
    
    log_success "Tenant UDN deployed"
}

deploy_mds_network_policy() {
    log_info "Deploying MDS Sidecar Policy..."
    kubectl apply -f "${PROJECT_ROOT}/scripts/k2/kyverno/mds-network-policy.yaml" \
        --context "${CONTEXT}"
    log_success "MDS Sidecar Policy deployed"
}

deploy_exchange_proxy_csi() {
    log_info "Deploying Exchange Proxy CSI..."
    
    local config
    config=$(cat "${SCRIPT_DIR}/exchange-proxy-csi.yaml.tmpl")
    config="${config//EXCHANGE_SERVICE_URL_PLACEHOLDER/${EXCHANGE_SERVICE_HOST}:${EXCHANGE_SERVICE_PORT}}"
    
    echo "$config" | kubectl apply -f - --context "${CONTEXT}"
    kubectl rollout status daemonset/exchange-proxy-csi \
        -n exchange-proxy-system --timeout=120s --context "${CONTEXT}"
    log_success "Exchange Proxy CSI deployed"
}

display_info() {
    log_success "MDS infrastructure deployment complete"
    echo
    echo "Tenant Namespace: ${TENANT_NAMESPACE}"
    echo
    echo "Exchange Proxy CSI DaemonSet:"
    kubectl get daemonset exchange-proxy-csi -n exchange-proxy-system --context "${CONTEXT}"
    echo
    echo "Tenant UDN:"
    kubectl get userdefinednetwork "tenant-${TENANT}-network" -n "${TENANT_NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Kyverno Policies:"
    kubectl get clusterpolicy inject-mds-sidecar --context "${CONTEXT}" 2>/dev/null || true
}

cleanup() {
    log_info "Removing MDS infrastructure..."
    kubectl delete userdefinednetwork "tenant-${TENANT}-network" -n "${TENANT_NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete -f "${SCRIPT_DIR}/mds-network-daemonset.yaml" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete -f "${SCRIPT_DIR}/exchange-proxy-csi.yaml.tmpl" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete -f "${PROJECT_ROOT}/scripts/k2/kyverno/mds-network-policy.yaml" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete namespace "${TENANT_NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete namespace exchange-proxy-system \
        --context "${CONTEXT}" --ignore-not-found=true
    log_success "MDS infrastructure removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            build_images
            load_images
            deploy_tenant_namespace
            deploy_tenant_udn
            deploy_mds_network_policy
            deploy_exchange_proxy_csi
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
