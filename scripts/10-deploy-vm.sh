#!/bin/bash

# 10-deploy-vm.sh
# Deploys a lightweight Ubuntu VM using KubeVirt

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
VM_TEMPLATE_FILE="${PROJECT_ROOT}/config/vm-template.yaml.tmpl"
VM_NAME="${VM_NAME:-demo-ubuntu-vm}"
VM_NAMESPACE="${VM_NAMESPACE:-default}"
VM_TENANT="${VM_TENANT:-default}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    # Check if cluster is accessible
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot access Kubernetes cluster. Please ensure cluster is running."
        exit 1
    fi
    
    # Check if KubeVirt is installed
    if ! kubectl get kubevirt -n kubevirt kubevirt &> /dev/null; then
        log_error "KubeVirt is not installed. Please run 'make kubevirt' first."
        exit 1
    fi
    
    # Check if CDI is installed
    if ! kubectl get cdi -n cdi cdi &> /dev/null; then
        log_error "CDI is not installed. Please run 'make kubevirt' first."
        exit 1
    fi
    
    # Check if VM template file exists
    if [[ ! -f "${VM_TEMPLATE_FILE}" ]]; then
        log_error "VM template file not found: ${VM_TEMPLATE_FILE}"
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Create VM configuration from template
create_vm_config() {
    # Use sed to replace placeholders in the template
    sed -e "s/VM_NAME_PLACEHOLDER/${VM_NAME}/g" \
        -e "s/TENANT_PLACEHOLDER/${VM_TENANT}/g" \
        "${VM_TEMPLATE_FILE}"
}

# Label node for VM
label_node_for_vm() {
    log_info "Labeling node for VM..."
    
    # Get the node where the VM is running
    local node_name=$(kubectl get pod -n ${VM_NAMESPACE} -l kubevirt.io/domain=${VM_NAME} -o jsonpath='{.items[0].spec.nodeName}' 2>/dev/null || echo "")
    
    if [[ -z "${node_name}" ]]; then
        log_warning "Could not determine node for VM"
        return 1
    fi
    
    # Label the node
    log_info "Labeling node ${node_name} with VM tenant labels..."
    kubectl label node ${node_name} has-kubevirt-vm=true vm-tenant=${VM_TENANT} --overwrite
    
    log_success "Node ${node_name} labeled successfully"
}

# Unlabel node for VM
unlabel_node_for_vm() {
    log_info "Removing VM labels from node..."
    
    # Get the node where the VM is/was running
    local node_name=$(kubectl get pod -n ${VM_NAMESPACE} -l kubevirt.io/domain=${VM_NAME} -o jsonpath='{.items[0].spec.nodeName}' 2>/dev/null || echo "")
    
    if [[ -z "${node_name}" ]]; then
        log_info "No node to unlabel"
        return 0
    fi
    
    # Remove the labels
    log_info "Removing labels from node ${node_name}..."
    kubectl label node ${node_name} has-kubevirt-vm- vm-tenant- 2>/dev/null || true
    
    log_success "Node labels removed"
}

# Deploy VM
deploy_vm() {
    log_info "Deploying VM '${VM_NAME}' in namespace '${VM_NAMESPACE}'..."
    log_info "Creating VM configuration from template..."
    
    # Create the VM using template
    create_vm_config | kubectl apply -n ${VM_NAMESPACE} -f -
    
    log_info "Waiting for VM to be created..."
    kubectl wait --for=condition=Ready vm/${VM_NAME} -n ${VM_NAMESPACE} --timeout=300s || true
    
    log_success "VM '${VM_NAME}' deployed successfully"
}

# Wait for VM to be running
wait_for_vm() {
    log_info "Waiting for VM to start..."
    
    # Wait for VMI to be running
    local timeout=300
    local elapsed=0
    
    while [ $elapsed -lt $timeout ]; do
        if kubectl get vmi ${VM_NAME} -n ${VM_NAMESPACE} --no-headers 2>/dev/null | grep -q "Running"; then
            log_success "VM is running!"
            return 0
        fi
        
        log_info "VM not ready yet, waiting... (${elapsed}s/${timeout}s)"
        sleep 10
        elapsed=$((elapsed + 10))
    done
    
    log_warning "VM did not start within ${timeout} seconds"
    return 1
}

# Display VM status
display_status() {
    log_success "VM deployment complete!"
    echo
    echo "VM Status:"
    echo "=========="
    
    # Show VM status
    echo "Virtual Machine:"
    kubectl get vm ${VM_NAME} -n ${VM_NAMESPACE} 2>/dev/null || echo "  VM not found"
    
    # Show VMI status
    echo
    echo "Virtual Machine Instance:"
    kubectl get vmi ${VM_NAME} -n ${VM_NAMESPACE} 2>/dev/null || echo "  VMI not found"
    
    
    # Show virt-launcher pod
    echo
    echo "Virt-Launcher Pod:"
    kubectl get pods -n ${VM_NAMESPACE} -l kubevirt.io/domain=${VM_NAME} 2>/dev/null || echo "  Pod not found"
    
    echo
    echo "Access Information:"
    echo "=================="
    echo "Console access: kubectl virt console ${VM_NAME} -n ${VM_NAMESPACE}"
    echo "VM logs: kubectl logs -n ${VM_NAMESPACE} -l kubevirt.io/domain=${VM_NAME}"
    echo "Username: ubuntu"
    echo "Password: ubuntu"
    
    # Show node where VM is running
    echo
    echo "Node Information:"
    echo "================"
    NODE_NAME=$(kubectl get pod -n ${VM_NAMESPACE} -l kubevirt.io/domain=${VM_NAME} -o jsonpath='{.items[0].spec.nodeName}' 2>/dev/null || echo "")
    if [[ -n "${NODE_NAME}" ]]; then
        echo "VM is running on node: ${NODE_NAME}"
        echo "Node labels:"
        kubectl get node ${NODE_NAME} --show-labels 2>/dev/null | grep -o "has-kubevirt-vm=true\|vm-tenant=[^,]*" || echo "  No VM labels found"
    else
        echo "VM not yet scheduled to a node"
    fi
    
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Access VM console to verify it's working"
    echo "2. Check VM pod details: kubectl describe pod -l kubevirt.io/domain=${VM_NAME}"
    echo "3. Verify node labels: kubectl get node ${NODE_NAME} --show-labels"
    echo "4. Run integration tests with 'make test'"
}

# Cleanup function
cleanup_vm() {
    log_info "Cleaning up VM '${VM_NAME}'..."
    
    # Remove node labels first
    unlabel_node_for_vm
    
    # Delete VM (this will also delete VMI and pods)
    if kubectl get vm ${VM_NAME} -n ${VM_NAMESPACE} &> /dev/null; then
        log_info "Deleting VM..."
        kubectl delete vm ${VM_NAME} -n ${VM_NAMESPACE}
    fi
    
    
    
    log_success "VM cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Deploys a lightweight Ubuntu VM using KubeVirt"
    echo ""
    echo "Options:"
    echo "  cleanup    Remove the VM from the cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  VM_NAME       Name of the VM (default: demo-ubuntu-vm)"
    echo "  VM_NAMESPACE  Namespace for the VM (default: default)"
    echo "  VM_TENANT     Tenant identifier for the VM (default: default)"
    echo ""
    echo "Prerequisites:"
    echo "  - Kubernetes cluster must be running and accessible"
    echo "  - KubeVirt and CDI must be installed"
    echo "  - kubectl must be configured to access the cluster"
    echo ""
    echo "Files:"
    echo "  - VM template: ${VM_TEMPLATE_FILE}"
    echo ""
    echo "Features:"
    echo "  - Lightweight Ubuntu 24.04 VM (2 cores, 2GB RAM, 8GB disk)"
    echo "  - Cloud-init configuration with ubuntu user"
    echo "  - Manual node labeling (has-kubevirt-vm, vm-tenant)"
    echo "  - Console and SSH access"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_vm
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting lightweight VM deployment..."
            check_prerequisites
            deploy_vm
            wait_for_vm
            label_node_for_vm
            display_status
            ;;
        *)
            echo "Error: Unknown option '$1'"
            echo ""
            usage
            exit 1
            ;;
    esac
}

# Handle script interruption
trap 'log_error "Script interrupted"; exit 1' INT TERM

# Run main function
main "$@"
