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
    
    log_info "Step 1/5: Setting up Kind cluster..."
    "${SCRIPT_DIR}/01-setup-kind-cluster.sh"
    echo
    
    log_info "Step 2/5: Installing SPIRE with Helm..."
    "${SCRIPT_DIR}/02-install-spire-helm.sh"
    echo
    
    log_info "Step 3/5: Verifying installation..."
    "${SCRIPT_DIR}/03-verify-spire.sh"
    echo
    
    log_info "Step 4/5: Deploying demo nginx pod with SPIFFE access..."
    "${SCRIPT_DIR}/04-demo-nginx.sh"
    echo
    
    log_info "Step 5/5: Deploying OpenBao with SPIFFE authentication..."
    "${SCRIPT_DIR}/05-deploy-bao.sh"
    echo
    
    log_success "Complete demo installation finished!"
    echo
    echo "Your cluster is ready with:"
    echo "- SPIRE server and agent running"
    echo "- Demo nginx pod with SPIFFE identity: spiffe://example.org/nginx"
    echo "- JWT tokens automatically fetched by Go sidecar"
    echo "- OpenBao with SPIFFE JWT authentication configured"
    echo "- Run './cleanup.sh' when done"
}

# Handle script interruption
trap 'log_error "Installation interrupted"; exit 1' INT TERM

# Run main function
main "$@"