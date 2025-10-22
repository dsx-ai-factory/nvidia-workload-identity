#!/bin/bash

# 06-deploy-bao.sh
# Deploy OpenBao to the Kind cluster

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
NAMESPACE="${BAO_NAMESPACE:-bao}"
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
BAO_CHART_VERSION="${BAO_CHART_VERSION:-0.16.2}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check if Kind cluster exists
check_cluster() {
    log_info "Checking Kind cluster..."
    
    # Check if Kind cluster exists
    if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_error "Kind cluster '${CLUSTER_NAME}' not found. Please run install.sh first."
        exit 1
    fi
    
    log_success "Kind cluster '${CLUSTER_NAME}' found"
}

# Create Bao namespace
create_namespace() {
    log_info "Creating Bao namespace..."
    
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
    
    log_success "Bao namespace ready"
}

# Add Bao Helm repository
add_bao_helm_repo() {
    log_info "Adding OpenBao Helm repository..."
    
    helm repo add openbao https://openbao.github.io/openbao-helm
    helm repo update
    
    log_success "OpenBao Helm repository added and updated"
}

# Deploy Bao using Helm chart
deploy_bao() {
    log_info "Deploying OpenBao using Helm chart..."
    
    local values_file="${PROJECT_ROOT}/config/bao-values.yaml"
    
    if [[ ! -f "$values_file" ]]; then
        log_error "Bao Helm values file not found: $values_file"
        return 1
    fi
    
    # Install OpenBao using Helm
    helm upgrade --install openbao openbao/openbao \
        --namespace "${NAMESPACE}" \
        --create-namespace \
        --values "$values_file" \
        --version "${BAO_CHART_VERSION}"
    
    log_success "OpenBao deployed with Helm"
}

# Wait for Bao to be ready
wait_for_bao() {
    log_info "Waiting for Bao pod to be ready (timeout: 120s)..."
    
    if kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=openbao -n "${NAMESPACE}" --timeout=120s; then
        log_success "Bao pod is ready"
    else
        log_error "Bao pod failed to become ready"
        return 1
    fi
}

# Initialize Bao
initialize_bao() {
    log_info "Initializing OpenBao..."
    
    # Start port forwarding in the background
    log_info "Starting port forwarding to OpenBao..."
    kubectl port-forward -n "${NAMESPACE}" svc/openbao 8200:8200 >/dev/null 2>&1 &
    local port_forward_pid=$!
    
    # Function to cleanup port forwarding
    cleanup_port_forward() {
        if kill -0 "$port_forward_pid" 2>/dev/null; then
            kill "$port_forward_pid" 2>/dev/null || true
            wait "$port_forward_pid" 2>/dev/null || true
        fi
    }
    
    # Set trap to cleanup on exit
    trap cleanup_port_forward EXIT
    
    # Wait for port forwarding to be ready
    sleep 3
    
    # Check if already initialized
    if curl -s http://127.0.0.1:8200/v1/sys/health | jq -e '.initialized == true' >/dev/null 2>&1; then
        log_info "Bao is already initialized"
        cleanup_port_forward
        trap - EXIT
        return 0
    fi
    
    # Initialize Bao
    log_info "Initializing Bao..."
    local init_output
    init_output=$(curl -s -X PUT http://127.0.0.1:8200/v1/sys/init \
        -H "Content-Type: application/json" \
        -d '{"secret_shares": 1, "secret_threshold": 1}')
    
    if [[ -z "$init_output" ]]; then
        log_error "Failed to initialize OpenBao"
        cleanup_port_forward
        trap - EXIT
        return 1
    fi
    
    # Extract unseal key and root token
    local unseal_key root_token
    unseal_key=$(echo "$init_output" | jq -r '.keys[0]')
    root_token=$(echo "$init_output" | jq -r '.root_token')
    
    # Save to config map for demo purposes (not for production!)
    kubectl create configmap bao-init-keys -n "${NAMESPACE}" \
        --from-literal=unseal_key="$unseal_key" \
        --from-literal=root_token="$root_token" \
        --dry-run=client -o yaml | kubectl apply -f -
    
    # Unseal Bao
    log_info "Unsealing Bao..."
    curl -s -X PUT http://127.0.0.1:8200/v1/sys/unseal \
        -H "Content-Type: application/json" \
        -d "{\"key\": \"$unseal_key\"}" >/dev/null
    
    # Verify status
    curl -s http://127.0.0.1:8200/v1/sys/health | jq .
    
    cleanup_port_forward
    trap - EXIT
    
    log_success "Bao initialized and unsealed"
    log_info "Root token and unseal key saved to configmap 'bao-init-keys' in namespace '${NAMESPACE}'"
}

# Configure SPIFFE authentication
configure_spiffe_auth() {
    log_info "Configuring SPIFFE JWT authentication in Bao..."
    
    # Get root token
    local root_token
    root_token=$(kubectl get configmap bao-init-keys -n "${NAMESPACE}" -o jsonpath='{.data.root_token}')
    
    if [[ -z "$root_token" ]]; then
        log_error "Could not get root token from configmap"
        return 1
    fi
    
    # Start port forwarding in the background
    log_info "Starting port forwarding to OpenBao..."
    kubectl port-forward -n "${NAMESPACE}" svc/openbao 8200:8200 >/dev/null 2>&1 &
    local port_forward_pid=$!
    
    # Function to cleanup port forwarding
    cleanup_port_forward() {
        if kill -0 "$port_forward_pid" 2>/dev/null; then
            kill "$port_forward_pid" 2>/dev/null || true
            wait "$port_forward_pid" 2>/dev/null || true
        fi
    }
    
    # Set trap to cleanup on exit
    trap cleanup_port_forward EXIT
    
    # Wait for port forwarding to be ready
    sleep 3
    
    # Enable JWT auth method
    export VAULT_ADDR=http://127.0.0.1:8200
    export VAULT_TOKEN="$root_token"
    
    # Check if JWT auth is already enabled
    if ! bao auth list | grep -q "jwt/"; then
        bao auth enable jwt >/dev/null
    fi
    
    # Extract the TLS certificate from the OIDC discovery provider
    log_info "Extracting TLS certificate from OIDC discovery provider..."
    local oidc_ca_cert=""
    
    # Extract certificate using a temporary pod with openssl
    # Use full FQDN for cross-namespace DNS resolution
    log_info "Extracting certificate from OIDC discovery provider..."
    oidc_ca_cert=$(kubectl run cert-extractor --image=alpine:latest --rm -i --restart=Never -- sh -c "
        apk add --no-cache openssl > /dev/null 2>&1
        echo | openssl s_client -connect spire-spiffe-oidc-discovery-provider.spire-server.svc.cluster.local:443 -showcerts 2>/dev/null | sed -n '/-----BEGIN CERTIFICATE-----/,/-----END CERTIFICATE-----/p'
    " 2>&1 | grep -E '^-----BEGIN CERTIFICATE-----$|^[A-Za-z0-9+/=]+$|^-----END CERTIFICATE-----$')
    
    if [[ -n "$oidc_ca_cert" ]] && echo "$oidc_ca_cert" | grep -q "BEGIN CERTIFICATE"; then
        log_success "Successfully extracted OIDC discovery provider certificate"
    else
        log_error "Failed to extract OIDC discovery provider certificate"
        cleanup_port_forward
        trap - EXIT
        return 1
    fi
    
    # Configure JWT auth with OIDC Discovery URL using bao CLI
    log_info "Configuring JWT authentication with OIDC Discovery URL..."
    
    # Configure JWT auth with certificate via stdin
    bao write auth/jwt/config \
        oidc_discovery_url="https://spire-spiffe-oidc-discovery-provider.spire-server" \
        oidc_discovery_ca_pem="$oidc_ca_cert" >/dev/null
    
    # Create policies for SPIFFE authenticated users  
    local policy_file="$PROJECT_ROOT/config/policies/spiffe-policy.hcl"
    
    if [[ ! -f "$policy_file" ]]; then
        log_error "SPIFFE policy file not found: $policy_file"
        cleanup_port_forward
        trap - EXIT
        return 1
    fi
    
    # Create the main spiffe-authenticated policy
    bao policy write spiffe-authenticated "$policy_file" >/dev/null
    
    # Create additional CEL-based policies for different components
    local policies_dir="$PROJECT_ROOT/config/policies"
    
    bao policy write region-sjc-1 "$policies_dir/region-sjc-1-policy.hcl" >/dev/null
    bao policy write tenant-default "$policies_dir/tenant-default-policy.hcl" >/dev/null
    bao policy write product-k8s2-control "$policies_dir/product-k8s2-control-policy.hcl" >/dev/null
    bao policy write product-default "$policies_dir/product-default-policy.hcl" >/dev/null
    bao policy write resource-type-pod "$policies_dir/resource-type-pod-policy.hcl" >/dev/null
    bao policy write resource-id-demo-nginx "$policies_dir/resource-id-demo-nginx-policy.hcl" >/dev/null
    
    # Create CEL role using the cel-role.json file but updated for our trust domain
    local cel_role_file="$PROJECT_ROOT/config/cel-role.json"
    if [[ ! -f "$cel_role_file" ]]; then
        log_error "CEL role file not found: $cel_role_file"
        cleanup_port_forward
        trap - EXIT
        return 1
    fi
    
    # Update the cel-role.json bound_audiences for our trust domain and create the role
    local updated_cel_role
    updated_cel_role=$(jq '.bound_audiences = ["spiffe://sjc-1.k8s2-control.nvidia.com"]' "$cel_role_file")
    echo "$updated_cel_role" | bao write auth/jwt/cel/role/basic-spiffe - >/dev/null
    
    log_success "SPIFFE JWT authentication configured"
    
    # Enable KV secrets engine using bao CLI
    bao secrets enable -path=secret kv-v2 >/dev/null
    
    # Store a test secret using bao CLI
    bao kv put secret/nginx username=admin password=secret123 >/dev/null
    
    cleanup_port_forward
    trap - EXIT
    
    log_success "SPIFFE JWT authentication configured"
}

# Display Bao information
show_bao_info() {
    log_info "OpenBao deployment information:"
    echo
    echo "Pod Status:"
    kubectl get pod -l app.kubernetes.io/name=openbao -n "${NAMESPACE}" -o wide
    echo
    echo "Service Status:"
    kubectl get svc -l app.kubernetes.io/name=openbao -n "${NAMESPACE}"
    echo
    echo "Access Bao API:"
    echo "kubectl port-forward -n ${NAMESPACE} svc/openbao 8200:8200"
    echo "Then visit: http://localhost:8200"
    echo
    echo "Root token (for demo only):"
    kubectl get configmap bao-init-keys -n "${NAMESPACE}" -o jsonpath='{.data.root_token}' 2>/dev/null || echo "Not available"
    echo
    echo
    echo "Test SPIFFE CEL authentication:"
    echo "jwt=\\\$(kubectl exec demo-nginx -c nginx -- cat /var/run/secrets/nvidia.com/spiffe/token)"
    echo "kubectl exec demo-nginx -c nginx -- curl -s -X POST -H \\\"Content-Type: application/json\\\" -d \\\"{\\\\\\\"jwt\\\\\\\":\\\\\\\"\\\$jwt\\\\\\\",\\\\\\\"role\\\\\\\":\\\\\\\"basic-spiffe\\\\\\\"}\\\" http://openbao.${NAMESPACE}:8200/v1/auth/jwt/cel/login" 
}

# Cleanup function
cleanup_bao() {
    log_info "Cleaning up OpenBao deployment..."
    kubectl delete namespace "${NAMESPACE}" --ignore-not-found=true
    log_success "OpenBao deployment cleaned up"
}

# Main function
main() {
    log_info "Starting OpenBao deployment to Kind cluster..."
    echo
    
    # Check if SPIRE is running
    if ! kubectl get pods -n spire-server -l app.kubernetes.io/name=server | grep -q Running; then
        log_error "SPIRE server not found or not running. Please run './install.sh' first."
        exit 1
    fi
    
    # Deploy Bao
    check_cluster
    add_bao_helm_repo
    deploy_bao
    wait_for_bao
    initialize_bao
    configure_spiffe_auth
    
    echo
    show_bao_info
    
    echo
    log_success "OpenBao deployment completed!"
    echo
    echo "OpenBao is now running with SPIFFE JWT authentication configured."
    echo "The demo nginx pod can authenticate to Bao using its SPIFFE identity."
}

# Handle script interruption
trap 'log_error "Bao deployment interrupted"; exit 1' INT TERM

# Check command line arguments
case "${1:-}" in
    "cleanup")
        cleanup_bao
        exit 0
        ;;
    "")
        main
        ;;
    *)
        echo "Usage: $0 [cleanup]"
        echo "  $0        - Deploy OpenBao"
        echo "  $0 cleanup - Remove OpenBao deployment"
        exit 1
        ;;
esac
