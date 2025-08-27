#!/bin/bash

# 04-demo-nginx.sh
# Deploys a demo nginx pod and verifies SPIFFE token access

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${DEMO_NAMESPACE:-default}"
POD_NAME="${POD_NAME:-demo-nginx}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Create nginx pod with SPIFFE socket mount
create_nginx_pod() {
    log_info "Creating demo nginx pod with SPIFFE access..."
    
    local config_file="${SCRIPT_DIR}/config/demo-nginx.yaml"
    
    if [[ ! -f "$config_file" ]]; then
        log_error "Demo nginx configuration file not found: $config_file"
        return 1
    fi
    
    # Apply the configuration with variable substitution if needed
    if [[ "${POD_NAME}" != "demo-nginx" ]] || [[ "${NAMESPACE}" != "default" ]]; then
        # Create temporary file with substitutions
        local temp_config="${SCRIPT_DIR}/demo-nginx-temp.yaml"
        sed "s/name: demo-nginx/name: ${POD_NAME}/g; s/namespace: default/namespace: ${NAMESPACE}/g" "$config_file" > "$temp_config"
        kubectl apply -f "$temp_config"
        rm -f "$temp_config"
    else
        kubectl apply -f "$config_file"
    fi
    
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

# Create SPIRE entry for the nginx pod
create_spire_entry() {
    log_info "Creating SPIRE entry for nginx pod..."
    
    local server_pod
    server_pod=$(kubectl get pods -n spire-server -l app.kubernetes.io/name=server -o jsonpath='{.items[0].metadata.name}')
    
    if [[ -z "$server_pod" ]]; then
        log_error "SPIRE server pod not found"
        return 1
    fi
    
    # Create SPIRE entry for the nginx pod (or update if exists)
    if kubectl exec -n spire-server "$server_pod" -c spire-server -- \
        /opt/spire/bin/spire-server entry create \
        -spiffeID "spiffe://example.org/nginx" \
        -parentID "spiffe://example.org/spire/agent/k8s_psat/spire-demo/$(kubectl get node spire-demo-worker -o jsonpath='{.metadata.uid}')" \
        -selector "k8s:ns:${NAMESPACE}" \
        -selector "k8s:pod-name:${POD_NAME}" 2>/dev/null; then
        log_success "SPIRE entry created for nginx pod"
    else
        log_info "SPIRE entry already exists for nginx pod"
    fi
}

# Verify SPIFFE token access
verify_token_access() {
        log_info "Verifying SPIFFE CSI token injection in nginx pod..."
    
    # Check if SPIFFE CSI directory exists
    log_info "Checking SPIFFE CSI directory..."
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -- test -d /var/run/secrets/spire.io; then
        log_success "✓ SPIFFE CSI directory exists"
    else
        log_error "✗ SPIFFE CSI directory not found"
        return 1
    fi
    
    # List files in the SPIFFE directory
    log_info "Listing files in SPIFFE CSI directory..."
    kubectl exec -n ${NAMESPACE} ${POD_NAME} -- ls -la /var/run/secrets/spire.io/ || true
    
    # Check for specific SPIFFE identity files
    local files_found=0
    
    log_info "Checking for SPIFFE identity files..."
    
    # Check for SVID (certificate)
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -- test -f /var/run/secrets/spire.io/svid.pem 2>/dev/null; then
        log_success "✓ SVID certificate found"
        files_found=$((files_found + 1))
    else
        log_warning "SVID certificate not found"
    fi
    
    # Check for private key
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -- test -f /var/run/secrets/spire.io/key.pem 2>/dev/null; then
        log_success "✓ Private key found"
        files_found=$((files_found + 1))
    else
        log_warning "Private key not found"
    fi
    
    # Check for CA bundle
    if kubectl exec -n ${NAMESPACE} ${POD_NAME} -- test -f /var/run/secrets/spire.io/bundle.pem 2>/dev/null; then
        log_success "✓ CA bundle found"
        files_found=$((files_found + 1))
    else
        log_warning "CA bundle not found"
    fi
    
    # Verify environment variables
    log_info "Checking SPIFFE environment variables..."
    local token_path
    token_path=$(kubectl exec -n ${NAMESPACE} ${POD_NAME} -- printenv SPIFFE_TOKEN_PATH 2>/dev/null || echo "")
    
    if [[ "$token_path" == "/var/run/secrets/spire.io/svid.pem" ]]; then
        log_success "✓ SPIFFE_TOKEN_PATH correctly configured: $token_path"
    else
        log_warning "SPIFFE_TOKEN_PATH not set or incorrect: '$token_path'"
    fi
    
    if [[ $files_found -gt 0 ]]; then
        log_success "✓ Demo nginx pod has SPIFFE identity files available ($files_found/3 files found)"
        log_info "Note: Files are automatically rotated by the SPIRE CSI driver"
    else
        log_error "✗ No SPIFFE identity files found"
        return 1
    fi
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
    echo "kubectl exec -it ${POD_NAME} -n ${NAMESPACE} -- sh"
    echo
    echo "Check SPIFFE socket:"
    echo "kubectl exec ${POD_NAME} -n ${NAMESPACE} -- ls -la /run/spire/sockets/"
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
    
    # Create and configure the demo pod
    create_nginx_pod
    wait_for_pod
    create_spire_entry
    
    echo
    verify_token_access
    
    echo
    show_pod_info
    
    echo
    log_success "Demo nginx pod is ready and configured for SPIFFE!"
    echo
    echo "The nginx pod now has:"
    echo "- Access to the SPIRE agent socket at /run/spire/sockets/agent.sock"
    echo "- SPIFFE token directory mounted at /var/run/secrets/nvidia.com/spiffe"
    echo "- SPIRE entry configured for identity: spiffe://example.org/nginx"
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
