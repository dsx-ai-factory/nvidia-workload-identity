#!/bin/bash

# test-integration.sh
# Test SPIFFE to Bao authentication integration

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/scripts/common.sh"

# Test SPIFFE to Bao authentication
test_spiffe_auth() {
    log_info "Testing SPIFFE JWT authentication to OpenBao..."
    
    # Check if all components are running
    if ! kubectl get pods -n spire-server -l app.kubernetes.io/name=server | grep -q Running; then
        log_error "SPIRE server not running"
        return 1
    fi
    
    if ! kubectl get pods -n bao -l app.kubernetes.io/name=openbao | grep -q Running; then
        log_error "Bao not running"
        return 1
    fi
    
    if ! kubectl get pods demo-nginx | grep -q Running; then
        log_error "Demo nginx pod not running"
        return 1
    fi
    
    # Wait for SPIFFE token to be available
    log_info "Waiting for SPIFFE JWT token..."
    local timeout=60
    local count=0
    while [[ $count -lt $timeout ]]; do
        if kubectl exec demo-nginx -c nginx -- test -f /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null; then
            break
        fi
        sleep 1
        ((count++))
    done
    
    if [[ $count -eq $timeout ]]; then
        log_error "SPIFFE token not found after ${timeout}s"
        return 1
    fi
    
    # Test authentication to Bao
    log_info "Testing authentication to Bao using SPIFFE JWT..."
    
    local auth_result
    auth_result=$(kubectl exec demo-nginx -c nginx -- \
        curl -s -X POST \
        -H "Content-Type: application/json" \
        -d '{"jwt":"'$(kubectl exec demo-nginx -c nginx -- cat /var/run/secrets/nvidia.com/spiffe/token)'","role":"basic-spiffe"}' \
        http://openbao.bao:8200/v1/auth/jwt/cel/login 2>/dev/null || echo "failed")
    
    if [[ "$auth_result" == "failed" ]] || [[ -z "$auth_result" ]]; then
        log_error "Authentication to Bao failed"
        return 1
    fi
    
    # Check if we got a client token back
    if echo "$auth_result" | grep -q '"client_token"'; then
        log_success "✓ SPIFFE JWT authentication to Bao successful!"
        log_info "Authentication Response:"
        echo "$auth_result" | jq . 2>/dev/null || echo "$auth_result"
    else
        log_error "Authentication response doesn't contain client_token"
        log_info "Response: $auth_result"
        return 1
    fi
    
    # Test accessing a secret with the obtained token
    log_info "Testing secret access..."
    local client_token
    client_token=$(echo "$auth_result" | grep -o '"client_token":"[^"]*"' | cut -d'"' -f4)
    
    if [[ -n "$client_token" ]]; then
        local secret_result
        secret_result=$(kubectl exec demo-nginx -c nginx -- \
            curl -s -H "X-Vault-Token: $client_token" \
            http://openbao.bao:8200/v1/secret/data/nginx 2>/dev/null || echo "failed")
        
        if echo "$secret_result" | grep -q '"username"'; then
            log_success "✓ Secret access successful!"
            log_info "Secret Response:"
            echo "$secret_result" | jq . 2>/dev/null || echo "$secret_result"
        else
            log_warning "Secret access failed or no data returned"
            log_info "Response: $secret_result"
        fi
    fi
}

# Main function
main() {
    log_info "Starting SPIFFE to Bao integration test..."
    echo
    
    test_spiffe_auth
    
    echo
    log_success "Integration test completed!"
}

# Run main function
main "$@"
