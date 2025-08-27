#!/bin/bash

# cleanup.sh
# Simple cleanup script that deletes the Kind cluster and local files

set -euo pipefail

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Display usage
usage() {
    echo "Usage: $0"
    echo "Deletes the Kind cluster and cleans up local files."
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Clean up Kind cluster
cleanup_kind_cluster() {
    log_info "Cleaning up Kind cluster..."
    
    if ! command -v kind &> /dev/null; then
        log_warning "kind not found, skipping cluster cleanup"
        return 0
    fi
    
    # Check if cluster exists and delete it
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Deleting Kind cluster: ${CLUSTER_NAME}"
        kind delete cluster --name "${CLUSTER_NAME}"
        log_success "Kind cluster deleted"
    else
        log_info "Kind cluster '${CLUSTER_NAME}' not found"
    fi
}





# Main execution
main() {
    log_info "Starting cleanup process..."
    
    cleanup_kind_cluster
    
    log_success "Cleanup completed!"
    echo
    echo "All resources have been cleaned up."
    echo "You can now run './install.sh' to start fresh."
}

# Handle script interruption
trap 'log_error "Cleanup interrupted"; exit 1' INT TERM

# Run main function
main "$@"