#!/bin/bash
# verify-nginx.sh - Verifies nginx SPIFFE authentication to OpenBao

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="default"
POD_NAME="demo-nginx"
K1_GATEWAY_HOST="gateway.sjc-1.nvidia.com"

source "${PROJECT_ROOT}/scripts/common.sh"

# Resolve gateway IP once (curl has DNS issues, but nslookup works)
get_gateway_ip() {
    kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        nslookup "${K1_GATEWAY_HOST}" 2>/dev/null | grep -A 1 "Name:" | grep "Address:" | awk '{print $2}' | head -1
}

verify_pod_running() {
    log_test "Checking if demo-nginx pod is running..."
    
    if kubectl get pod "${POD_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        --no-headers 2>/dev/null | grep -q "Running"; then
        log_success "Pod is running"
        return 0
    else
        log_error "Pod is not running"
        return 1
    fi
}

verify_token_exists() {
    log_test "Checking for SPIFFE token..."
    
    if kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        test -f /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null; then
        log_success "SPIFFE token found"
        return 0
    else
        log_error "SPIFFE token not found"
        return 1
    fi
}

verify_token_format() {
    log_test "Verifying token format..."
    
    local token_start
    token_start=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        head -c 10 /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null || echo "")
    
    if [[ "$token_start" =~ ^eyJ ]]; then
        log_success "Token is valid JWT format"
        return 0
    else
        log_error "Token is not valid JWT format"
        return 1
    fi
}

verify_token_expiry() {
    log_test "Verifying token expiry is at most 5 minutes..."
    
    local token
    token=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        cat /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null)
    
    # Extract payload (second part of JWT)
    local payload_b64
    payload_b64=$(echo "$token" | cut -d. -f2)
    
    # Add padding if needed and decode
    local payload
    payload=$(echo "$payload_b64" | tr '_-' '/+' | awk '{
        l = length($0)
        pad = (4 - l % 4) % 4
        for (i = 0; i < pad; i++) $0 = $0 "="
        print
    }' | base64 -d 2>/dev/null)
    
    local exp iat
    exp=$(echo "$payload" | jq -r '.exp' 2>/dev/null)
    iat=$(echo "$payload" | jq -r '.iat' 2>/dev/null)
    
    if [[ -z "$exp" || "$exp" == "null" ]]; then
        log_error "Token missing exp claim"
        return 1
    fi
    
    if [[ -z "$iat" || "$iat" == "null" ]]; then
        log_error "Token missing iat claim"
        return 1
    fi
    
    local ttl=$((exp - iat))
    local max_ttl=300  # 5 minutes in seconds
    
    if [[ $ttl -le $max_ttl ]]; then
        log_success "Token TTL is ${ttl}s (max allowed: ${max_ttl}s)"
        return 0
    else
        log_error "Token TTL is ${ttl}s, exceeds maximum of ${max_ttl}s (5 minutes)"
        return 1
    fi
}

verify_bao_auth() {
    log_test "Testing authentication to OpenBao..."
    
    local gateway_ip
    gateway_ip=$(get_gateway_ip)
    if [[ -z "$gateway_ip" ]]; then
        log_error "Could not resolve ${K1_GATEWAY_HOST}"
        return 1
    fi
    
    local token
    token=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        cat /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null)
    
    local auth_result
    auth_result=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        curl -s -X POST \
        -H "Content-Type: application/json" \
        -d "{\"jwt\":\"${token}\",\"role\":\"basic-spiffe\"}" \
        "http://${gateway_ip}:8200/v1/auth/jwt/cel/login" 2>/dev/null || echo "failed")
    
    if echo "$auth_result" | grep -q '"client_token"'; then
        log_success "OpenBao authentication successful"
        echo
        echo "Auth Response:"
        echo "$auth_result" | jq . 2>/dev/null || echo "$auth_result"
        return 0
    else
        log_error "OpenBao authentication failed"
        echo "Response: $auth_result"
        return 1
    fi
}

verify_secret_access() {
    log_test "Testing secret access..."
    
    local gateway_ip
    gateway_ip=$(get_gateway_ip)
    if [[ -z "$gateway_ip" ]]; then
        log_error "Could not resolve ${K1_GATEWAY_HOST}"
        return 1
    fi
    
    local token
    token=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        cat /var/run/secrets/nvidia.com/spiffe/token 2>/dev/null)
    
    local auth_result
    auth_result=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        curl -s -X POST \
        -H "Content-Type: application/json" \
        -d "{\"jwt\":\"${token}\",\"role\":\"basic-spiffe\"}" \
        "http://${gateway_ip}:8200/v1/auth/jwt/cel/login" 2>/dev/null)
    
    local client_token
    client_token=$(echo "$auth_result" | jq -r '.auth.client_token' 2>/dev/null || echo "")
    
    if [[ -z "$client_token" || "$client_token" == "null" ]]; then
        log_error "Could not get client token"
        return 1
    fi
    
    local secret_result
    secret_result=$(kubectl exec "${POD_NAME}" -c nginx -n "${NAMESPACE}" --context "${CONTEXT}" -- \
        curl -s -H "X-Vault-Token: ${client_token}" \
        "http://${gateway_ip}:8200/v1/secret/data/nginx" 2>/dev/null || echo "failed")
    
    if echo "$secret_result" | grep -q '"username"'; then
        log_success "Secret access successful"
        echo
        echo "Secret Response:"
        echo "$secret_result" | jq . 2>/dev/null || echo "$secret_result"
        return 0
    else
        log_warning "Secret access failed"
        echo "Response: $secret_result"
        return 1
    fi
}

main() {
    log_info "Verifying nginx SPIFFE authentication..."
    echo
    
    local failed=0
    
    verify_pod_running || ((failed++))
    verify_token_exists || ((failed++))
    verify_token_format || ((failed++))
    verify_token_expiry || ((failed++))
    verify_bao_auth || ((failed++))
    verify_secret_access || ((failed++))
    
    echo
    if [[ $failed -eq 0 ]]; then
        log_success "All nginx verification tests passed"
    else
        log_error "${failed} test(s) failed"
        exit 1
    fi
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
