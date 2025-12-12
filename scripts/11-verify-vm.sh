#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

# Configuration
VM_NAME="${VM_NAME:-demo-ubuntu-vm}"
VM_NAMESPACE="${VM_NAMESPACE:-default}"
METADATA_SERVICE_TENANT="${METADATA_SERVICE_TENANT:-default}"
TEST_AUDIENCE="${TEST_AUDIENCE:-spiffe://tenant1.sjc-1.k8s2-control.nvidia.com}"

# Get metadata service UDN IP
get_metadata_service_udn_ip() {
    # TODO eventually girish will give us the static 169.254.169.254 IP https://github.com/ovn-kubernetes/ovn-kubernetes/pull/5595
    local pod_name=$(kubectl get pod -n ${VM_NAMESPACE} -l app=metadata-service,tenant=${METADATA_SERVICE_TENANT} -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    if [ -z "$pod_name" ]; then
        return 1
    fi
    
    local udn_ip=$(kubectl get pod -n ${VM_NAMESPACE} ${pod_name} -o jsonpath='{.metadata.annotations.k8s\.ovn\.org/pod-networks}' 2>/dev/null | jq -r '.["'${VM_NAMESPACE}'/tenant-'${METADATA_SERVICE_TENANT}'-network"].ip_addresses[0]' 2>/dev/null | cut -d'/' -f1)
    
    if [ -z "$udn_ip" ] || [ "$udn_ip" = "null" ]; then
        return 1
    fi
    
    echo "$udn_ip"
}

# Execute command in VM via virtctl console
exec_in_vm() {
    local cmd="$1"
    local timeout_secs="${2:-30}"
    
    # Use virtctl console with piped input and sleep delays
    (
        sleep 2
        echo "ubuntu"
        sleep 2
        echo "ubuntu"
        sleep 3
        echo "$cmd"
        sleep 3
        echo "exit"
        sleep 1
    ) | timeout ${timeout_secs}s virtctl console ${VM_NAME} -n ${VM_NAMESPACE} 2>&1
    
    return $?
}

# Decode JWT payload (base64url decode)
decode_jwt_payload() {
    local jwt="$1"
    
    # Extract payload (second part between dots)
    local payload=$(echo "$jwt" | cut -d'.' -f2)
    
    if [ -z "$payload" ]; then
        return 1
    fi
    
    # Base64url decode: replace - with +, _ with /, add padding
    local base64_payload=$(echo "$payload" | tr '_-' '/+')
    
    # Add padding if needed
    case $((${#base64_payload} % 4)) in
        2) base64_payload="${base64_payload}==" ;;
        3) base64_payload="${base64_payload}=" ;;
    esac
    
    # Decode and pretty print JSON
    echo "$base64_payload" | base64 -d 2>/dev/null | jq . 2>/dev/null || {
        # Fallback if jq not available
        echo "$base64_payload" | base64 -d 2>/dev/null
    }
}

# Parse and display JWT response
parse_jwt_response() {
    local response="$1"
    
    # Extract JSON from response (handle curl verbose output)
    local json_response
    json_response=$(echo "$response" | grep -o '{.*}' | tail -1)
    
    if [ -z "$json_response" ]; then
        log_error "Could not extract JSON from response"
        return 1
    fi
    
    # Parse JSON fields
    local token
    local expires_at
    local spiffe_id
    
    token=$(echo "$json_response" | jq -r '.token // empty' 2>/dev/null)
    expires_at=$(echo "$json_response" | jq -r '.expires_at // empty' 2>/dev/null)
    spiffe_id=$(echo "$json_response" | jq -r '.spiffe_id // empty' 2>/dev/null)
    
    if [ -z "$token" ]; then
        log_error "Token not found in response"
        return 1
    fi
    
    # Display response fields
    log_success "Successfully retrieved JWT response"
    echo ""
    log_info "Response Fields:"
    echo "  Token: ${token}"
    if [ -n "$expires_at" ]; then
        echo "  Expires At: ${expires_at}"
    fi
    if [ -n "$spiffe_id" ]; then
        echo "  SPIFFE ID: ${spiffe_id}"
    fi
    echo ""
    
    # Decode and display JWT payload
    log_info "JWT Payload (decoded):"
    if ! decode_jwt_payload "$token"; then
        log_warning "Failed to decode JWT payload"
        return 1
    fi
    
    return 0
}

# Test metadata service JWT endpoint
test_jwt_endpoint() {
    local metadata_ip="$1"
    
    log_info "Testing JWT endpoint at ${metadata_ip}:8080/v1/metadata/identity..."
    
    local curl_cmd="curl -v -X POST http://${metadata_ip}:8080/v1/metadata/identity -H 'Content-Type: application/json' -d '{\"audience\": \"${TEST_AUDIENCE}\"}' --max-time 10"
    
    local response
    if response=$(exec_in_vm "$curl_cmd" 60); then
        # Check if response contains a token
        if echo "$response" | grep -q '"token"'; then
            if parse_jwt_response "$response"; then
                return 0
            else
                log_error "Failed to parse JWT response"
                echo "$response"
                return 1
            fi
        else
            log_error "Response did not contain a token"
            echo "$response"
            return 1
        fi
    else
        log_error "Failed to retrieve JWT from metadata service"
        echo "$response"
        return 1
    fi
}

# Display VM status
display_vm_status() {
    log_info "VM Status:"
    kubectl get vmi ${VM_NAME} -n ${VM_NAMESPACE} -o wide 2>/dev/null || log_warning "VM not found"
    
    echo ""
    log_info "Metadata Service Status:"
    kubectl get pod -n ${VM_NAMESPACE} -l app=metadata-service,tenant=${METADATA_SERVICE_TENANT} -o wide 2>/dev/null || log_warning "Metadata service not found"
}

# Main verification flow
main() {
    log_info "Starting VM metadata service verification..."
    
    # Check prerequisites
    if ! command -v virtctl &> /dev/null; then
        log_error "virtctl is not installed. Please install it first."
        exit 1
    fi
    
    # Display status
    display_vm_status
    echo ""
    
    # Get metadata service IP
    log_info "Retrieving metadata service UDN IP address..."
    local metadata_ip
    if ! metadata_ip=$(get_metadata_service_udn_ip); then
        log_error "Failed to get metadata service IP"
        exit 1
    fi
    
    log_success "Metadata service UDN IP: ${metadata_ip}"
    echo ""
    
    # Wait for VM to be ready
    log_info "Waiting for VM to be ready..."
    if ! kubectl wait --for=condition=Ready vmi/${VM_NAME} -n ${VM_NAMESPACE} --timeout=60s > /dev/null 2>&1; then
        log_error "VM did not become ready in time"
        exit 1
    fi
    log_success "VM is ready"
    echo ""
    
    # Test JWT endpoint
    if test_jwt_endpoint "${metadata_ip}"; then
        echo ""
        log_success "VM metadata service verification completed successfully!"
        return 0
    else
        echo ""
        log_error "VM metadata service verification failed"
        return 1
    fi
}

# Run main function
main "$@"

