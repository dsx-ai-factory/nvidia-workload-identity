#!/bin/bash

# setup-kind-cluster.sh
# Creates a Kind cluster configured for SPIRE deployment

set -euo pipefail

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
KIND_CONFIG_FILE="${KIND_CONFIG_FILE:-config/kind-config.yaml}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kind &> /dev/null; then
        log_error "kind is not installed. Please install kind first."
        log_info "Installation: https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    if ! command -v docker &> /dev/null; then
        log_error "docker is not installed. Please install docker first."
        exit 1
    fi
    
    # Check if docker is running
    if ! docker info &> /dev/null; then
        log_error "Docker is not running. Please start Docker first."
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Check Kind configuration
check_kind_config() {
    log_info "Checking Kind cluster configuration..."
    
    if [[ ! -f "${SCRIPT_DIR}/${KIND_CONFIG_FILE}" ]]; then
        log_error "Kind configuration file not found: ${SCRIPT_DIR}/${KIND_CONFIG_FILE}"
        log_info "Expected file: ${KIND_CONFIG_FILE}"
        exit 1
    fi
    

    
    log_success "Kind configuration validated at ${SCRIPT_DIR}/${KIND_CONFIG_FILE}"
}

# Create the Kind cluster
create_cluster() {
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    
    # Check if cluster already exists
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_warning "Cluster '${CLUSTER_NAME}' already exists"
        read -p "Do you want to delete and recreate it? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            log_info "Deleting existing cluster..."
            kind delete cluster --name "${CLUSTER_NAME}"
        else
            log_info "Using existing cluster"
            return 0
        fi
    fi
    
    
    # Create the cluster
    kind create cluster --config="${SCRIPT_DIR}/${KIND_CONFIG_FILE}" --name="${CLUSTER_NAME}"
    
    # Wait for cluster to be ready
    log_info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=300s
    
    log_success "Cluster '${CLUSTER_NAME}' created successfully"
}


# Display cluster information
display_cluster_info() {
    log_success "Kind cluster setup complete!"
    echo
    echo "Cluster Information:"
    echo "==================="
    echo "Cluster Name: ${CLUSTER_NAME}"
    echo "Kubernetes Version: $(kubectl version --short --client 2>/dev/null | grep 'Client Version' || echo 'N/A')"
    echo "Nodes:"
    kubectl get nodes -o wide
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Run './install-spire-helm.sh' to install SPIRE server with Helm"
    echo "2. Run './verify-spire.sh' to verify the installation"
    echo "3. Use './cleanup.sh' to clean up when done"
}

# Main execution
main() {
    log_info "Starting Kind cluster setup for SPIRE..."
    
    check_prerequisites
    check_kind_config
    create_cluster
    display_cluster_info
}

# Handle script interruption
trap 'log_error "Script interrupted"; exit 1' INT TERM

# Run main function
main "$@"
