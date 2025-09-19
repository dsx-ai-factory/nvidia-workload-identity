#!/bin/bash

# 02-install-kubevirt.sh
# Installs KubeVirt and CDI for virtual machine management

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    if ! command -v curl &> /dev/null; then
        log_error "curl is not installed. Please install curl first."
        exit 1
    fi
    
    # Check if cluster is accessible
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot access Kubernetes cluster. Please ensure cluster is running."
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Install KubeVirt
install_kubevirt() {
    log_info "Installing KubeVirt..."
    
    # Get the latest KubeVirt release version
    log_info "Fetching latest KubeVirt release version..."
    local kubevirt_version
    kubevirt_version=$(curl -s https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)
    if [[ -z "${kubevirt_version}" ]]; then
        log_error "Failed to fetch KubeVirt version"
        return 1
    fi
    log_info "Using KubeVirt version: ${kubevirt_version}"
    
    # Deploy the KubeVirt operator
    log_info "Deploying KubeVirt operator..."
    kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/${kubevirt_version}/kubevirt-operator.yaml"
    
    # Wait for operator to be ready
    log_info "Waiting for KubeVirt operator to be ready..."
    kubectl wait --for=condition=Available deployment/virt-operator -n kubevirt --timeout=300s
    
    # Create the KubeVirt custom resource
    log_info "Creating KubeVirt custom resource..."
    kubectl apply -f "https://github.com/kubevirt/kubevirt/releases/download/${kubevirt_version}/kubevirt-cr.yaml"
    
    # Wait for KubeVirt to be available
    log_info "Waiting for KubeVirt to be available..."
    kubectl -n kubevirt wait kv kubevirt --for condition=Available --timeout=600s
    
    log_success "KubeVirt ${kubevirt_version} installed successfully"
}

# Install Containerized Data Importer (CDI)
install_cdi() {
    log_info "Installing Containerized Data Importer (CDI)..."
    
    # Get the latest CDI release version
    log_info "Fetching latest CDI release version..."
    local cdi_version
    cdi_version=$(curl -s https://api.github.com/repos/kubevirt/containerized-data-importer/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    if [[ -z "${cdi_version}" ]]; then
        log_error "Failed to fetch CDI version"
        return 1
    fi
    log_info "Using CDI version: ${cdi_version}"
    
    # Deploy the CDI operator
    log_info "Deploying CDI operator..."
    kubectl create -f "https://github.com/kubevirt/containerized-data-importer/releases/download/${cdi_version}/cdi-operator.yaml"
    
    # Wait for operator to be ready
    log_info "Waiting for CDI operator to be ready..."
    kubectl wait --for=condition=Available deployment/cdi-operator -n cdi --timeout=300s
    
    # Create the CDI custom resource
    log_info "Creating CDI custom resource..."
    kubectl create -f "https://github.com/kubevirt/containerized-data-importer/releases/download/${cdi_version}/cdi-cr.yaml"
    
    # Wait for CDI to be ready
    log_info "Waiting for CDI to be ready..."
    kubectl wait --for=condition=Available cdi/cdi -n cdi --timeout=600s
    
    log_success "CDI ${cdi_version} installed successfully"
}

# Display installation status
display_status() {
    log_success "KubeVirt and CDI installation complete!"
    echo
    echo "Installation Status:"
    echo "==================="
    
    # Check KubeVirt status
    echo "KubeVirt:"
    kubectl get kubevirt -n kubevirt 2>/dev/null || echo "  Not found"
    
    # Check CDI status
    echo "CDI:"
    kubectl get cdi -n cdi 2>/dev/null || echo "  Not found"
    
    echo
    echo "Pod Status:"
    echo "==========="
    echo "KubeVirt Pods:"
    kubectl get pods -n kubevirt --no-headers 2>/dev/null | wc -l | xargs echo "  Running pods:"
    echo "CDI Pods:"
    kubectl get pods -n cdi --no-headers 2>/dev/null | wc -l | xargs echo "  Running pods:"
    
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Run 'scripts/03-install-kyverno.sh' to install Kyverno"
    echo "2. Consider installing virtctl for VM management:"
    echo "   https://kubevirt.io/user-guide/user_workloads/virtctl_client_tool/"
}

# Cleanup function
cleanup_kubevirt() {
    log_info "Cleaning up KubeVirt and CDI..."
    
    # Delete KubeVirt
    if kubectl get kubevirt kubevirt -n kubevirt &> /dev/null; then
        log_info "Deleting KubeVirt custom resource..."
        kubectl delete kubevirt kubevirt -n kubevirt
    fi
    
    if kubectl get namespace kubevirt &> /dev/null; then
        log_info "Deleting KubeVirt operator..."
        kubectl delete namespace kubevirt
    fi
    
    # Delete CDI
    if kubectl get cdi cdi -n cdi &> /dev/null; then
        log_info "Deleting CDI custom resource..."
        kubectl delete cdi cdi -n cdi
    fi
    
    if kubectl get namespace cdi &> /dev/null; then
        log_info "Deleting CDI operator..."
        kubectl delete namespace cdi
    fi
    
    log_success "KubeVirt and CDI cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Installs KubeVirt and CDI for virtual machine management"
    echo ""
    echo "Options:"
    echo "  cleanup    Remove KubeVirt and CDI from the cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Prerequisites:"
    echo "  - Kubernetes cluster must be running and accessible"
    echo "  - kubectl must be configured to access the cluster"
    echo ""
    echo "Features:"
    echo "  - KubeVirt for virtual machine management"
    echo "  - CDI (Containerized Data Importer) for VM image handling"
    echo "  - Automatic version detection (latest stable releases)"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_kubevirt
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting KubeVirt and CDI installation..."
            check_prerequisites
            install_kubevirt
            install_cdi
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
