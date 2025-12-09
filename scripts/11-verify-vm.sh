#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

# Configuration
VM_NAME="${VM_NAME:-demo-ubuntu-vm}"
VM_NAMESPACE="${VM_NAMESPACE:-default}"
METADATA_SERVICE_TENANT="${METADATA_SERVICE_TENANT:-default}"
TEST_AUDIENCE="${TEST_AUDIENCE:-spiffe://sjc-1.k8s2-control.nvidia.com}"

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

# Test metadata service JWT endpoint
test_jwt_endpoint() {
    local metadata_ip="$1"
    
    log_info "Testing JWT endpoint at ${metadata_ip}:8080/v1/metadata/identity..."
    
    local curl_cmd="curl -v -X POST http://${metadata_ip}:8080/v1/metadata/identity -H 'Content-Type: application/json' -d '{\"audience\": \"${TEST_AUDIENCE}\"}' --max-time 10"
    
    local response
    if response=$(exec_in_vm "$curl_cmd" 60); then
        # Check if response contains a token
        if echo "$response" | grep -q '"token"'; then
            log_success "Successfully retrieved JWT response"
            echo "$response" | grep -A1 '"token"' | head -20
            
            # Extract and display SPIFFE ID if present
            if echo "$response" | grep -q '"spiffe_id"'; then
                local spiffe_id=$(echo "$response" | grep -o '"spiffe_id":"[^"]*"' | cut -d'"' -f4)
                log_info "SPIFFE ID: ${spiffe_id}"
            fi
            return 0
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

