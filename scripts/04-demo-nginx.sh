#!/bin/bash

# 04-demo-nginx.sh
# Deploys a demo nginx pod and verifies SPIFFE token access

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
NAMESPACE="${DEMO_NAMESPACE:-default}"
POD_NAME="${POD_NAME:-demo-nginx}"
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Build SPIFFE token fetcher image
build_spiffe_token_fetcher() {
    log_info "Building SPIFFE token fetcher Docker image..."
    
    cd "${PROJECT_ROOT}"
    
    # Build the Docker image
    docker build -f Dockerfile.spiffe-token-fetcher -t spiffe-token-fetcher:latest .
    
    log_success "Docker image built successfully"
}

# Load images into Kind cluster
load_images_to_kind() {
    log_info "Loading Docker images into Kind cluster..."
    
    # Check if Kind cluster exists
    if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_error "Kind cluster '${CLUSTER_NAME}' not found. Please run install.sh first."
        exit 1
    fi
    
    # Load the image into Kind
    kind load docker-image spiffe-token-fetcher:latest --name "${CLUSTER_NAME}"
    
    log_success "Images loaded into Kind cluster"
}

# Create nginx pod with SPIFFE socket mount
create_nginx_pod() {
    log_info "Creating demo nginx pod with SPIFFE access..."
    
    local config_file="${PROJECT_ROOT}/config/demo-nginx.yaml"
    
    if [[ ! -f "$config_file" ]]; then
        log_error "Demo nginx configuration file not found: $config_file"
        return 1
    fi
    
    # Apply the configuration
    kubectl apply -f "$config_file"
    
    log_success "Nginx pod created"
}

# Wait for pod to be ready
wait_for_pod() {
    log_info "Waiting for nginx pod to be ready (timeout: 60s)..."
    
    if kubectl wait --for=condition=ready pod/${POD_NAME} -n ${NAMESPACE} --timeout=60s; then
        log_success "Nginx pod is ready"
    else
        log_error "Nginx pod failed to become ready"
        return 1
    fi
}

# Verify SPIFFE token access
verify_token_access() {
    log_info "Verifying SPIFFE sidecar token extraction in nginx pod..."
    
    # Check if SPIFFE token directory exists
    log_info "Checking SPIFFE token directory..."
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- test -d /var/run/secrets/nvidia.com/spiffe; then
        log_success "✓ SPIFFE token directory exists"
    else
        log_error "✗ SPIFFE token directory not found"
        return 1
    fi
    
    # Wait for the sidecar to generate the token file (up to 60 seconds)
    log_info "Waiting for SPIFFE sidecar to generate JWT token (timeout: 60s)..."
    local timeout=60
    local count=0
    while [[ $count -lt $timeout ]]; do
        if kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- test -f /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null; then
            log_success "✓ SPIFFE token file found after ${count} seconds"
            break
        fi
        sleep 1
        ((count++))
    done
    
    # List files in the SPIFFE directory
    log_info "Listing files in SPIFFE token directory..."
    kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- ls -la /var/run/secrets/nvidia.com/spiffe/ || true
    
    # Check for the SPIFFE token file
    log_info "Checking for SPIFFE token file..."
    
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- test -f /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null; then
        log_success "✓ SPIFFE token file found"
        
        # Check if token has content
        local token_size
        token_size=$(kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- wc -c < /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null || echo "0")
        
        if [[ "$token_size" -gt 0 ]]; then
            log_success "✓ SPIFFE JWT token has content ($token_size bytes)"
            
            # Check if it looks like a JWT token (should start with eyJ)
            local token_start
            token_start=$(kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- head -c 10 /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null || echo "")
            
            if [[ "$token_start" =~ ^eyJ ]]; then
                log_success "✓ Token appears to be a valid JWT format"
                log_info "JWT token preview: ${token_start}..."
            else
                log_warning "Token doesn't appear to be JWT format, got: ${token_start}..."
            fi
        else
            log_warning "SPIFFE token file is empty"
        fi
    else
        log_error "✗ SPIFFE token file not found"
        return 1
    fi
    
    # Verify environment variables
    log_info "Checking SPIFFE environment variables..."
    local token_path
    token_path=$(kubectl exec -n ${NAMESPACE} ${POD_NAME} -c nginx -- printenv SPIFFE_TOKEN_PATH 2>/dev/null || echo "")
    
    if [[ "$token_path" == "/var/run/secrets/nvidia.com/spiffe/token" ]]; then
        log_success "✓ SPIFFE_TOKEN_PATH correctly configured: $token_path"
    else
        log_warning "SPIFFE_TOKEN_PATH not set or incorrect: '$token_path'"
    fi
    
    # Check sidecar status
    log_info "Checking SPIFFE sidecar status..."
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -c spiffe-sidecar -- ps | grep -q "go\|spiffe-fetcher" 2>/dev/null; then
        log_success "✓ SPIFFE Go sidecar is active"
    else
        log_warning "SPIFFE Go sidecar may not be running"
    fi
    
    log_success "✓ Demo nginx pod has SPIFFE JWT access via sidecar"
    log_info "Note: JWT is automatically refreshed by the sidecar at 80% of expiry time"
}

# Display pod information
show_pod_info() {
    log_info "Demo nginx pod information:"
    echo
    echo "Pod Status:"
    kubectl get pod ${POD_NAME} -n ${NAMESPACE} -o wide
    echo
    echo "Pod Description:"
    kubectl describe pod ${POD_NAME} -n ${NAMESPACE} | grep -A 10 -B 5 -E "(Mounts|Volumes|Environment)"
    echo
    echo "Access the nginx pod:"
    echo "kubectl exec -it ${POD_NAME} -n ${NAMESPACE} -c nginx -- sh"
    echo
    echo "Check SPIFFE JWT token:"
    echo "kubectl exec ${POD_NAME} -n ${NAMESPACE} -c nginx -- cat /var/run/secrets/nvidia.com/spiffe/token"
    echo
    echo "Check sidecar logs:"
    echo "kubectl logs ${POD_NAME} -n ${NAMESPACE} -c spiffe-sidecar"
    echo
    echo "Clean up when done:"
    echo "kubectl delete pod ${POD_NAME} -n ${NAMESPACE}"
}

# Cleanup function
cleanup_pod() {
    log_info "Cleaning up demo nginx pod..."
    kubectl delete pod ${POD_NAME} -n ${NAMESPACE} --ignore-not-found=true
    log_success "Demo nginx pod cleaned up"
}

# Main function
main() {
    log_info "Starting SPIFFE demo nginx deployment..."
    echo
    
    # Check if SPIRE is running
    if ! kubectl get pods -n spire-server -l app.kubernetes.io/name=server | grep -q Running; then
        log_error "SPIRE server not found or not running. Please run './install.sh' first."
        exit 1
    fi
    
    # Build and load the Docker image for the sidecar
    log_info "Building and loading SPIFFE token fetcher image..."
    build_spiffe_token_fetcher
    load_images_to_kind
    echo
    
    # Create and configure the demo pod
    create_nginx_pod
    wait_for_pod
    
    echo
    verify_token_access
    
    echo
    show_pod_info
    
    echo
    log_success "Demo nginx pod is ready and configured for SPIFFE!"
    echo
    echo "The nginx pod now has:"
    echo "- SPIFFE sidecar that fetches JWT from CSI driver"
    echo "- SPIFFE JWT file at /var/run/secrets/nvidia.com/spiffe/token"
    echo "- JWT automatically refreshed at 80% of expiry time with retries"
}

# Handle script interruption
trap 'log_error "Demo deployment interrupted"; exit 1' INT TERM

# Check command line arguments
case "${1:-}" in
    "cleanup")
        cleanup_pod
        exit 0
        ;;
    "")
        main
        ;;
    *)
        echo "Usage: $0 [cleanup]"
        echo "  $0        - Deploy demo nginx pod"
        echo "  $0 cleanup - Remove demo nginx pod"
        exit 1
        ;;
esac
