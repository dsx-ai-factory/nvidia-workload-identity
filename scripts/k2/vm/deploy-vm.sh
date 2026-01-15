#!/bin/bash
# deploy-vm.sh - Deploys Ubuntu VM to k2 cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
VM_NAME="${VM_NAME:-demo-ubuntu-vm}"
VM_TENANT="${VM_TENANT:-tenant1}"
NAMESPACE="tenant-${VM_TENANT}"
VM_IMAGE="quay.io/containerdisks/ubuntu:24.04"

source "${PROJECT_ROOT}/scripts/common.sh"

preload_images() {
    log_info "Preloading VM images into cluster..."
    if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${VM_IMAGE}" "${CLUSTER_NAME}"; then
        log_error "Failed to load image: ${VM_IMAGE}"
        return 1
    fi
    log_success "Images preloaded"
}

deploy_vm() {
    log_info "Deploying VM '${VM_NAME}'..."
    
    local config
    config=$(cat "${SCRIPT_DIR}/vm-template.yaml.tmpl")
    config="${config//VM_NAME_PLACEHOLDER/$VM_NAME}"
    config="${config//TENANT_PLACEHOLDER/$VM_TENANT}"
    
    echo "$config" | kubectl apply -n "${NAMESPACE}" -f - --context "${CONTEXT}"
    
    log_info "Waiting for VM to be created..."
    kubectl wait --for=condition=Ready vm/"${VM_NAME}" -n "${NAMESPACE}" \
        --timeout=300s --context "${CONTEXT}" || true
    
    log_success "VM deployed"
}

wait_for_vm() {
    log_info "Waiting for VM to start..."
    
    local timeout=300
    local elapsed=0
    
    while [[ $elapsed -lt $timeout ]]; do
        if kubectl get vmi "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
            --no-headers 2>/dev/null | grep -q "Running"; then
            log_success "VM is running"
            return 0
        fi
        sleep 10
        ((elapsed+=10))
        log_info "Waiting for VM... (${elapsed}s/${timeout}s)"
    done
    
    log_warning "VM did not start within ${timeout}s"
}

label_node() {
    log_info "Labeling node for VM..."
    
    local node_name
    node_name=$(kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" \
        -o jsonpath='{.items[0].spec.nodeName}' --context "${CONTEXT}" 2>/dev/null || echo "")
    
    if [[ -z "$node_name" ]]; then
        log_warning "Could not determine node for VM"
        return 1
    fi
    
    kubectl label node "${node_name}" has-kubevirt-vm=true "vm-tenant=${VM_TENANT}" \
        --overwrite --context "${CONTEXT}"
    
    log_success "Node ${node_name} labeled"
}

display_info() {
    log_success "VM deployment complete"
    echo
    echo "Virtual Machine:"
    kubectl get vm "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Virtual Machine Instance:"
    kubectl get vmi "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" 2>/dev/null || echo "VMI not ready"
    echo
    echo "Access: virtctl console ${VM_NAME} -n ${NAMESPACE} --context ${CONTEXT}"
    echo "Credentials: ubuntu / ubuntu"
}

cleanup() {
    log_info "Removing VM..."
    
    # Remove node labels
    local node_name
    node_name=$(kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" \
        -o jsonpath='{.items[0].spec.nodeName}' --context "${CONTEXT}" 2>/dev/null || echo "")
    
    if [[ -n "$node_name" ]]; then
        kubectl label node "${node_name}" has-kubevirt-vm- vm-tenant- \
            --context "${CONTEXT}" 2>/dev/null || true
    fi
    
    kubectl delete vm "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found=true
    
    log_success "VM removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            deploy_vm
            wait_for_vm
            label_node
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

