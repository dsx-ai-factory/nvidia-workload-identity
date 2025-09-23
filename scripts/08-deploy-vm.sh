#!/bin/bash

# 08-deploy-vm.sh
# Deploys a lightweight Ubuntu VM using KubeVirt

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
VM_TEMPLATE_FILE="${PROJECT_ROOT}/config/vm-template.yaml"
SPIFFE_POLICY_FILE="${PROJECT_ROOT}/config/spiffe-sidecar-policy.yaml"
VM_NAME="${VM_NAME:-demo-ubuntu-vm}"
VM_NAMESPACE="${VM_NAMESPACE:-default}"

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
    
    # Check if SPIFFE policy file exists
    if [[ ! -f "${SPIFFE_POLICY_FILE}" ]]; then
        log_error "SPIFFE sidecar policy file not found: ${SPIFFE_POLICY_FILE}"
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Create VM configuration from template
create_vm_config() {
    # Use sed to replace placeholders in the template
    sed -e "s/VM_NAME_PLACEHOLDER/${VM_NAME}/g" \
        "${VM_TEMPLATE_FILE}"
}

# Deploy SPIFFE sidecar injection policy
deploy_spiffe_policy() {
    log_info "Deploying SPIFFE sidecar injection policy..."
    
    # Check if policy already exists
    if kubectl get clusterpolicy inject-spiffe-jwt-sidecar &> /dev/null; then
        log_info "SPIFFE sidecar policy already exists, updating..."
        kubectl apply -f "${SPIFFE_POLICY_FILE}"
    else
        log_info "Creating new SPIFFE sidecar policy..."
        kubectl apply -f "${SPIFFE_POLICY_FILE}"
    fi
    
    # Wait for policy to be ready
    kubectl wait --for=condition=Ready clusterpolicy/inject-spiffe-jwt-sidecar --timeout=60s || true
    
    log_success "SPIFFE sidecar policy deployed successfully"
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
    
    # Show DataVolume status
    echo
    echo "Data Volume:"
    kubectl get dv ${VM_NAME}-rootdisk -n ${VM_NAMESPACE} 2>/dev/null || echo "  DataVolume not found"
    
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
    
    echo
    echo "SPIFFE Sidecar Policy:"
    echo "====================="
    kubectl get clusterpolicy inject-spiffe-jwt-sidecar 2>/dev/null || echo "  Policy not found"
    
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Access VM console to verify it's working"
    echo "2. Check if SPIFFE sidecar was injected: kubectl describe pod -l kubevirt.io/domain=${VM_NAME}"
    echo "3. Verify sidecar policy is active: kubectl get clusterpolicy inject-spiffe-jwt-sidecar"
    echo "4. Run integration tests with 'make test'"
}

# Cleanup function
cleanup_vm() {
    log_info "Cleaning up VM '${VM_NAME}'..."
    
    # Delete VM (this will also delete VMI and pods)
    if kubectl get vm ${VM_NAME} -n ${VM_NAMESPACE} &> /dev/null; then
        log_info "Deleting VM..."
        kubectl delete vm ${VM_NAME} -n ${VM_NAMESPACE}
    fi
    
    # Delete DataVolume
    if kubectl get dv ${VM_NAME}-rootdisk -n ${VM_NAMESPACE} &> /dev/null; then
        log_info "Deleting DataVolume..."
        kubectl delete dv ${VM_NAME}-rootdisk -n ${VM_NAMESPACE}
    fi
    
    # Optionally clean up SPIFFE policy (commented out to preserve for other VMs)
    # if kubectl get clusterpolicy inject-spiffe-jwt-sidecar &> /dev/null; then
    #     log_info "Deleting SPIFFE sidecar policy..."
    #     kubectl delete clusterpolicy inject-spiffe-jwt-sidecar
    # fi
    
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
    echo ""
    echo "Prerequisites:"
    echo "  - Kubernetes cluster must be running and accessible"
    echo "  - KubeVirt and CDI must be installed"
    echo "  - kubectl must be configured to access the cluster"
    echo ""
    echo "Files:"
    echo "  - VM template: ${VM_TEMPLATE_FILE}"
    echo "  - SPIFFE policy: ${SPIFFE_POLICY_FILE}"
    echo ""
    echo "Features:"
    echo "  - Lightweight Ubuntu 22.04 VM (2 cores, 2GB RAM, 8GB disk)"
    echo "  - Cloud-init configuration with ubuntu user"
    echo "  - SPIFFE sidecar injection annotation (requires Kyverno policy)"
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
            deploy_spiffe_policy
            deploy_vm
            wait_for_vm
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
