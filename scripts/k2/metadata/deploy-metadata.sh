#!/bin/bash
# deploy-metadata.sh - Deploys SPIFFE metadata service to k2 cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="default"
TENANT="${METADATA_TENANT:-default}"
TENANT_SUBNET_OCTET="${TENANT_SUBNET_OCTET:-1}"
IMAGE_NAME="metadata-service"
CSI_IMAGE_NAME="metadata-bridge-csi-driver"
IMAGE_TAG="latest"

source "${PROJECT_ROOT}/scripts/common.sh"

build_images() {
    log_info "Building metadata service image..."
    docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" \
        -f "${PROJECT_ROOT}/Dockerfile.metadata-service" "${PROJECT_ROOT}"
    
    log_info "Building CSI driver image..."
    docker build -t "${CSI_IMAGE_NAME}:${IMAGE_TAG}" \
        -f "${PROJECT_ROOT}/Dockerfile.metadata-bridge-csi" "${PROJECT_ROOT}"
    
    log_success "Images built"
}

load_images() {
    log_info "Loading images into Kind cluster..."
    kind load docker-image "${IMAGE_NAME}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    kind load docker-image "${CSI_IMAGE_NAME}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    log_success "Images loaded"
}

apply_rbac() {
    log_info "Applying RBAC..."
    
    local config
    config=$(cat "${SCRIPT_DIR}/metadata-service-rbac.yaml.tmpl")
    config="${config//NAMESPACE_PLACEHOLDER/$NAMESPACE}"
    
    echo "$config" | kubectl apply -n "${NAMESPACE}" -f - --context "${CONTEXT}"
    
    log_success "RBAC applied"
}

deploy_csi_driver() {
    log_info "Deploying metadata bridge CSI driver..."
    kubectl apply -f "${SCRIPT_DIR}/metadata-bridge-csi-daemonset.yaml" \
        -n "${NAMESPACE}" --context "${CONTEXT}"
    
    log_info "Waiting for CSI driver..."
    kubectl rollout status daemonset/metadata-bridge-csi-driver \
        -n "${NAMESPACE}" --timeout=120s --context "${CONTEXT}" || true
    
    log_success "CSI driver deployed"
}

deploy_workload_api_proxy_policy() {
    log_info "Deploying Workload API Proxy Policy..."
    kubectl apply -f "${PROJECT_ROOT}/scripts/k2/kyverno/workload-api-proxy-policy.yaml" \
        --context "${CONTEXT}"
    log_success "Workload API Proxy Policy deployed"
}

deploy_tenant_udn() {
    log_info "Deploying tenant UDN..."
    
    local config
    config=$(cat "${PROJECT_ROOT}/scripts/k2/vm/tenant-udn.yaml.tmpl")
    config="${config//TENANT_PLACEHOLDER/$TENANT}"
    config="${config//TENANT_SUBNET_OCTET/$TENANT_SUBNET_OCTET}"
    
    echo "$config" | kubectl apply -n "${NAMESPACE}" -f - --context "${CONTEXT}"
    
    log_success "Tenant UDN deployed"
}

deploy_metadata_service() {
    log_info "Deploying metadata service..."
    
    # DaemonSet
    local daemonset_config
    daemonset_config=$(cat "${SCRIPT_DIR}/metadata-service-daemonset.yaml.tmpl")
    daemonset_config="${daemonset_config//TENANT_PLACEHOLDER/$TENANT}"
    echo "$daemonset_config" | kubectl apply -n "${NAMESPACE}" -f - --context "${CONTEXT}"
    
    # Service
    local service_config
    service_config=$(cat "${SCRIPT_DIR}/metadata-service.yaml.tmpl")
    service_config="${service_config//TENANT_PLACEHOLDER/$TENANT}"
    echo "$service_config" | kubectl apply -n "${NAMESPACE}" -f - --context "${CONTEXT}"
    
    log_success "Metadata service deployed"
}

display_info() {
    log_success "Metadata service deployment complete"
    echo
    echo "CSI Driver:"
    kubectl get daemonset metadata-bridge-csi-driver -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Metadata Service:"
    kubectl get daemonset "metadata-service-${TENANT}" -n "${NAMESPACE}" --context "${CONTEXT}" 2>/dev/null || \
        echo "DaemonSet waiting for nodes with label vm-tenant=${TENANT}"
}

cleanup() {
    log_info "Removing metadata service..."
    kubectl delete daemonset "metadata-service-${TENANT}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete service "metadata-service-${TENANT}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete -f "${SCRIPT_DIR}/metadata-bridge-csi-daemonset.yaml" \
        -n "${NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true
    log_success "Metadata service removed"
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
            apply_rbac
            deploy_csi_driver
            deploy_workload_api_proxy_policy
            deploy_tenant_udn
            deploy_metadata_service
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

