#!/bin/bash

# setup-kind-cluster.sh
# Creates a Kind cluster configured for SPIRE deployment

set -euo pipefail

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
KIND_CONFIG_FILE="${KIND_CONFIG_FILE:-${PROJECT_ROOT}/config/kind-config.yaml}"

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
    
    if ! command -v curl &> /dev/null; then
        log_error "curl is not installed. Please install curl first."
        exit 1
    fi
    
    if ! command -v bao &> /dev/null; then
        log_error "bao CLI is not installed. Please install bao CLI first."
        log_info "Installation: https://openbao.org/docs/install/"
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
    
    if [[ ! -f "${KIND_CONFIG_FILE}" ]]; then
        log_error "Kind configuration file not found: ${KIND_CONFIG_FILE}"
        exit 1
    fi
    
    log_success "Kind configuration validated at ${KIND_CONFIG_FILE}"
}

# Create the Kind cluster
create_cluster() {
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    
    # Check if cluster already exists and delete it
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Deleting existing cluster '${CLUSTER_NAME}'..."
        kind delete cluster --name "${CLUSTER_NAME}"
    fi
    
    # Create the cluster
    log_info "Creating new cluster..."
    kind create cluster --config="${KIND_CONFIG_FILE}" --name="${CLUSTER_NAME}"
    
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
    echo "1. Run 'make install' to complete the full setup"
    echo "2. Run 'make test' to verify the installation"
    echo "3. Use 'make clean' to clean up when done"
}

# Cleanup function
cleanup_cluster() {
    log_info "Cleaning up Kind cluster..."
    
    # Check if cluster exists
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Deleting Kind cluster '${CLUSTER_NAME}'..."
        kind delete cluster --name "${CLUSTER_NAME}"
        log_success "Kind cluster '${CLUSTER_NAME}' deleted"
    else
        log_info "Kind cluster '${CLUSTER_NAME}' not found"
    fi
    
    log_success "Cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Creates or manages a Kind cluster configured for SPIRE deployment"
    echo ""
    echo "Options:"
    echo "  cleanup    Delete the Kind cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  CLUSTER_NAME      Name of the Kind cluster (default: spire-demo)"
    echo "  KIND_CONFIG_FILE  Path to Kind configuration file"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_cluster
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting Kind cluster setup for SPIRE..."
            check_prerequisites
            check_kind_config
            create_cluster
            display_cluster_info
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
