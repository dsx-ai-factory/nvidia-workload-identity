#!/bin/bash

# install.sh
# Simple demo installer for SPIRE on Kind

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Main installation sequence
main() {
    log_info "Starting SPIRE on Kind demo installation..."
    echo
    
    log_info "Step 1/4: Setting up Kind cluster..."
    "${SCRIPT_DIR}/01-setup-kind-cluster.sh"
    echo
    
    log_info "Step 2/4: Installing SPIRE with Helm..."
    "${SCRIPT_DIR}/02-install-spire-helm.sh"
    echo
    
    log_info "Step 3/4: Verifying installation..."
    "${SCRIPT_DIR}/03-verify-spire.sh"
    echo
    
    log_info "Step 4/4: Deploying demo nginx pod with SPIFFE access..."
    "${SCRIPT_DIR}/04-demo-nginx.sh"
    echo
    
    log_success "SPIRE demo installation completed!"
    echo
    echo "Your SPIRE cluster is ready with:"
    echo "- SPIRE server and agent running"
    echo "- Demo nginx pod with SPIFFE identity: spiffe://example.org/nginx"
    echo "- JWT tokens automatically fetched by Go sidecar"
    echo "- Run './cleanup.sh' when done"
}

# Handle script interruption
trap 'log_error "Installation interrupted"; exit 1' INT TERM

# Run main function
main "$@"