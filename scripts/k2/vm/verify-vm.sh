#!/bin/bash
# verify-vm.sh - Verifies VM with MDS sidecar

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
VM_NAME="${VM_NAME:-demo-ubuntu-vm}"
VM_TENANT="${VM_TENANT:-tenant1}"
NAMESPACE="tenant-${VM_TENANT}"
TEST_AUDIENCE="${TEST_AUDIENCE:-spiffe://tenant1.sjc-1.vmaas.nvidia.com}"
MDS_IP="169.254.169.254"

source "${PROJECT_ROOT}/scripts/common.sh"

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
    ) | timeout "${timeout_secs}"s virtctl console "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" 2>&1
    
    return $?
}

# Parse and display JWT response
parse_jwt_response() {
    local response="$1"
    
    # Extract JSON from response (handle curl verbose output)
    local json_response
    json_response=$(echo "$response" | grep -o '{.*}' | tail -1)
    
    if [[ -z "$json_response" ]]; then
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
    
    if [[ -z "$token" ]]; then
        log_error "Token not found in response"
        return 1
    fi
    
    log_success "Successfully retrieved JWT response"
    echo
    echo "Response:"
    echo "$json_response" | jq . 2>/dev/null || echo "$json_response"
    
    return 0
}

# Test metadata service JWT endpoint
test_jwt_endpoint() {
    log_test "Testing JWT endpoint at ${MDS_IP}:80/v1/metadata/identity..."
    
    local curl_cmd="curl -v -X POST http://${MDS_IP}/v1/metadata/identity -H 'Content-Type: application/json' -d '{\"audience\": \"${TEST_AUDIENCE}\"}' --max-time 10"
    
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

verify_vm_running() {
    log_test "Checking if VM is running..."
    
    if kubectl wait --for=jsonpath='{.status.phase}'=Running "vmi/${VM_NAME}" \
        -n "${NAMESPACE}" --timeout=60s --context "${CONTEXT}" > /dev/null 2>&1; then
        log_success "VM is running"
        return 0
    else
        log_error "VM is not running"
        return 1
    fi
}

verify_mds_sidecar() {
    log_test "Checking MDS sidecar..."
    
    local pod_name
    pod_name=$(kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" \
        -o jsonpath='{.items[0].metadata.name}' --context "${CONTEXT}" 2>/dev/null || echo "")
    
    if [[ -z "$pod_name" ]]; then
        log_error "Could not find VM launcher pod"
        return 1
    fi
    
    if kubectl get pod "${pod_name}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        -o jsonpath='{.spec.containers[*].name}' 2>/dev/null | grep -q "metadata-service"; then
        log_success "MDS sidecar present"
        return 0
    else
        log_error "MDS sidecar not found"
        return 1
    fi
}

verify_mds_network_setup() {
    log_test "Checking MDS network setup DaemonSet..."
    
    if kubectl get daemonset mds-network-setup -n mds-system --context "${CONTEXT}" \
        --no-headers 2>/dev/null | grep -q .; then
        log_success "MDS network setup DaemonSet exists"
        return 0
    else
        log_error "MDS network setup DaemonSet not found"
        return 1
    fi
}

verify_node_labels() {
    log_test "Checking node labels..."
    
    local tenant="${VM_TENANT:-default}"
    
    local nodes_with_label
    nodes_with_label=$(kubectl get nodes -l "vm-tenant=${tenant}" --context "${CONTEXT}" \
        --no-headers 2>/dev/null | wc -l)
    
    if [[ $nodes_with_label -gt 0 ]]; then
        log_success "Node(s) labeled for VM tenant"
        return 0
    else
        log_warning "No nodes labeled for VM tenant"
        return 1
    fi
}

verify_metadata_access() {
    log_test "Testing metadata service from VM..."
    
    # Check prerequisites
    if ! command -v virtctl &> /dev/null; then
        log_warning "virtctl is not installed. Skipping automated VM test."
        log_info "To test metadata access from inside the VM:"
        echo
        echo "  virtctl console ${VM_NAME} -n ${NAMESPACE} --context ${CONTEXT}"
        echo "  # Then run inside VM:"
        echo "  curl -X POST http://${MDS_IP}/v1/metadata/identity \\"
        echo "    -H 'Content-Type: application/json' \\"
        echo "    -d '{\"audience\": \"${TEST_AUDIENCE}\"}'"
        echo
        return 0
    fi
    
    # Test JWT endpoint
    if test_jwt_endpoint; then
        return 0
    else
        return 1
    fi
}

display_vm_info() {
    echo
    echo "VM Information:"
    echo "==============="
    
    echo "VMI:"
    kubectl get vmi "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" 2>/dev/null || echo "  Not found"
    
    echo
    echo "Launcher Pod:"
    kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" --context "${CONTEXT}" 2>/dev/null || echo "  Not found"
    
    echo
    echo "Launcher Pod Containers:"
    local pod_name
    pod_name=$(kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" \
        -o jsonpath='{.items[0].metadata.name}' --context "${CONTEXT}" 2>/dev/null || echo "")
    if [[ -n "$pod_name" ]]; then
        kubectl get pod "${pod_name}" -n "${NAMESPACE}" --context "${CONTEXT}" \
            -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' 2>/dev/null
    fi
    
    echo
    echo "Access:"
    echo "  Console: virtctl console ${VM_NAME} -n ${NAMESPACE} --context ${CONTEXT}"
    echo "  Credentials: ubuntu / ubuntu"
    echo
    echo "Test MDS from inside VM:"
    echo "  curl -X POST http://${MDS_IP}/v1/metadata/identity \\"
    echo "    -H 'Content-Type: application/json' \\"
    echo "    -d '{\"audience\": \"${TEST_AUDIENCE}\"}'"
}

main() {
    log_info "Verifying VM with MDS sidecar..."
    echo
    
    local failed=0
    
    verify_vm_running || ((failed++))
    verify_mds_sidecar || ((failed++))
    verify_mds_network_setup || ((failed++))
    verify_node_labels || ((failed++))
    verify_metadata_access || ((failed++))
    
    display_vm_info
    
    echo
    if [[ $failed -eq 0 ]]; then
        log_success "All VM verification tests passed"
    else
        log_error "${failed} test(s) failed"
        exit 1
    fi
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
