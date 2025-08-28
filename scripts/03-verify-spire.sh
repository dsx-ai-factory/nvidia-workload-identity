#!/bin/bash

# 03-verify-spire.sh
# Comprehensive verification script for SPIRE installation

set -euo pipefail

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
NAMESPACE="${SPIRE_NAMESPACE:-spire-server}"
SPIRE_SERVER_NAMESPACE="${SPIRE_SERVER_NAMESPACE:-spire-server}"
SPIRE_TRUST_DOMAIN="${SPIRE_TRUST_DOMAIN:-sjc-1.k8s2-control.nvidia.com}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Test results tracking
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# Test execution wrapper
run_test() {
    local test_name="$1"
    local test_command="$2"
    local expected_result="${3:-0}"  # Default expected result is 0 (success)
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    log_test "Running: $test_name"
    
    if eval "$test_command" &>/dev/null; then
        local result=0
    else
        local result=1
    fi
    
    if [[ "$result" == "$expected_result" ]]; then
        log_success "✓ $test_name"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        return 0
    else
        log_error "✗ $test_name"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        return 1
    fi
}

# Check if commands are available
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    run_test "kubectl command available" "command -v kubectl"
    run_test "helm command available" "command -v helm"
    run_test "kind command available" "command -v kind"
    run_test "docker command available" "command -v docker"
    
    echo
}

# Verify cluster status
check_cluster_status() {
    log_info "Checking cluster status..."
    
    run_test "Kubernetes cluster accessible" "kubectl cluster-info"
    run_test "Cluster context contains '$CLUSTER_NAME'" "kubectl config current-context | grep -q '$CLUSTER_NAME'"
    run_test "All nodes are ready" "[ \$(kubectl get nodes --no-headers | awk '{print \$2}' | grep -v Ready | wc -l | tr -d ' ') -eq 0 ]"
    
    # Display cluster info
    echo "Cluster Information:"
    kubectl get nodes -o wide
    echo
}

# Verify namespaces
check_namespaces() {
    log_info "Checking namespaces..."
    
    run_test "SPIRE server namespace exists" "kubectl get namespace '$SPIRE_SERVER_NAMESPACE'"
    run_test "SPIRE agent namespace exists" "kubectl get namespace '$NAMESPACE'"
    
    echo
}

# Verify SPIRE server deployment
check_spire_server() {
    log_info "Checking SPIRE server..."
    
    run_test "SPIRE server statefulset exists" "kubectl get statefulset spire-server -n '$SPIRE_SERVER_NAMESPACE'"
    run_test "SPIRE server statefulset is ready" "kubectl get statefulset spire-server -n '$SPIRE_SERVER_NAMESPACE' -o jsonpath='{.status.readyReplicas}' | grep -q 1"
    run_test "SPIRE server pod is running" "kubectl get pods -n '$SPIRE_SERVER_NAMESPACE' -l app.kubernetes.io/name=server --no-headers | grep -q Running"
    run_test "SPIRE server service exists" "kubectl get service spire-server -n '$SPIRE_SERVER_NAMESPACE'"
    
    # Get server pod for detailed checks
    local server_pod
    server_pod=$(kubectl get pods -n "$SPIRE_SERVER_NAMESPACE" -l app.kubernetes.io/name=server -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [[ -n "$server_pod" ]]; then
        # Wait for server pod to be ready with timeout
        log_info "Waiting for SPIRE server pod to be ready (timeout: 300s)..."
        if kubectl wait --for=condition=ready pod "$server_pod" -n "$SPIRE_SERVER_NAMESPACE" --timeout=300s &>/dev/null; then
            log_success "✓ SPIRE server pod is ready"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ SPIRE server pod failed to become ready within timeout"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        run_test "SPIRE server binary is accessible" "kubectl exec -n '$SPIRE_SERVER_NAMESPACE' '$server_pod' -- /opt/spire/bin/spire-server --version"
        
        # Check server health
        if kubectl exec -n "$SPIRE_SERVER_NAMESPACE" "$server_pod" -- /opt/spire/bin/spire-server healthcheck &>/dev/null; then
            log_success "✓ SPIRE server health check passed"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ SPIRE server health check failed"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
    else
        log_error "Could not find SPIRE server pod for detailed checks"
    fi
    
    echo
}

# Verify SPIRE agent deployment
check_spire_agent() {
    log_info "Checking SPIRE agent..."
    
    run_test "SPIRE agent daemonset exists" "kubectl get daemonset spire-agent -n '$NAMESPACE'"
    # Wait for agent daemonset to be ready with timeout
    log_info "Waiting for SPIRE agent daemonset to be ready (timeout: 300s)..."
    if kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=agent -n "$NAMESPACE" --timeout=300s &>/dev/null; then
        log_success "✓ SPIRE agent daemonset is ready"
        PASSED_TESTS=$((PASSED_TESTS + 1))
    else
        log_error "✗ SPIRE agent daemonset failed to become ready within timeout"
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    
    # Check if agents are running on worker nodes (control-plane nodes may have NoSchedule taints)
    local worker_node_count
    local agent_count
    worker_node_count=$(kubectl get nodes --no-headers | grep -v control-plane | wc -l)
    agent_count=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=agent --no-headers | grep Running | wc -l)
    
    if [[ "$worker_node_count" == "$agent_count" ]]; then
        log_success "✓ SPIRE agent running on all $worker_node_count worker nodes"
        PASSED_TESTS=$((PASSED_TESTS + 1))
    else
        log_error "✗ SPIRE agent count mismatch (worker nodes: $worker_node_count, agents: $agent_count)"
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    
    # Check individual agent pods
    local agent_pods
    agent_pods=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=agent -o jsonpath='{.items[*].metadata.name}')
    
    if [[ -n "$agent_pods" ]]; then
        local first_agent_pod
        first_agent_pod=$(echo "$agent_pods" | awk '{print $1}')
        
        # Wait for agent pod to be ready with timeout
        log_info "Waiting for SPIRE agent pod to be ready (timeout: 300s)..."
        if kubectl wait --for=condition=ready pod "$first_agent_pod" -n "$NAMESPACE" --timeout=300s &>/dev/null; then
            log_success "✓ SPIRE agent pod is ready"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ SPIRE agent pod failed to become ready within timeout"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        run_test "SPIRE agent binary is accessible" "kubectl exec -n '$NAMESPACE' '$first_agent_pod' -- /opt/spire/bin/spire-agent --version"
        
        # Check agent health
        if kubectl exec -n "$NAMESPACE" "$first_agent_pod" -- /opt/spire/bin/spire-agent healthcheck &>/dev/null; then
            log_success "✓ SPIRE agent health check passed"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ SPIRE agent health check failed"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
    fi
    
    echo
}

# Verify RBAC resources
check_rbac() {
    log_info "Checking RBAC resources..."
    
    run_test "SPIRE server service account exists" "kubectl get serviceaccount spire-server -n '$SPIRE_SERVER_NAMESPACE'"
    run_test "SPIRE agent service account exists" "kubectl get serviceaccount spire-agent -n '$NAMESPACE'"
    run_test "SPIRE server cluster role exists" "kubectl get clusterrole spire-server-spire-server"
    run_test "SPIRE agent cluster role exists" "kubectl get clusterrole spire-agent"
    run_test "SPIRE server cluster role binding exists" "kubectl get clusterrolebinding spire-server-spire-server"
    run_test "SPIRE agent cluster role binding exists" "kubectl get clusterrolebinding spire-agent"
    
    echo
}

# Verify agent-server connectivity
check_agent_server_connectivity() {
    log_info "Checking agent-server connectivity..."
    
    local server_pod
    server_pod=$(kubectl get pods -n "$SPIRE_SERVER_NAMESPACE" -l app.kubernetes.io/name=server -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [[ -n "$server_pod" ]]; then
        # List connected agents
        local connected_agents
        connected_agents=$(kubectl exec -n "$SPIRE_SERVER_NAMESPACE" "$server_pod" -- /opt/spire/bin/spire-server agent list 2>/dev/null | grep -c "SPIFFE ID" || echo "0")
        
        if [[ "$connected_agents" -gt 0 ]]; then
            log_success "✓ $connected_agents agent(s) connected to server"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ No agents connected to server"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
        
        # Show detailed agent list
        echo "Connected agents:"
        kubectl exec -n "$SPIRE_SERVER_NAMESPACE" "$server_pod" -- /opt/spire/bin/spire-server agent list || log_warning "Could not list agents"
    else
        log_error "Could not find SPIRE server pod for connectivity check"
    fi
    
    echo
}

# Verify workload API socket
check_csi_driver() {
    log_info "Checking SPIFFE CSI driver..."
    
    # Check if CSI driver pods are running
    run_test "SPIFFE CSI driver pods are running" "kubectl get pods -n '$SPIRE_SERVER_NAMESPACE' -l app.kubernetes.io/name=spiffe-csi-driver --no-headers | grep -q Running"
    
    # Check CSI driver registration
    run_test "CSI driver is registered" "kubectl get csidriver csi.spiffe.io >/dev/null 2>&1"
    
    echo
}

# Verify Helm releases
check_helm_releases() {
    log_info "Checking Helm releases..."
    
    if command -v helm &>/dev/null; then
        run_test "SPIRE Helm release exists" "helm list -n '$SPIRE_SERVER_NAMESPACE' | grep -q spire"
        run_test "SPIRE CRDs Helm release exists" "helm list -n '$SPIRE_SERVER_NAMESPACE' | grep -q spire-crds"
        
        # Check release status
        local spire_status
        spire_status=$(helm list -n "$SPIRE_SERVER_NAMESPACE" -o json | jq -r '.[] | select(.name=="spire") | .status' 2>/dev/null || echo "unknown")
        
        if [[ "$spire_status" == "deployed" ]]; then
            log_success "✓ SPIRE Helm release is deployed"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ SPIRE Helm release status: $spire_status"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))

    else
        log_warning "Helm not available, skipping Helm release checks"
    fi
    
    echo
}

# Check trust domain configuration
check_trust_domain() {
    log_info "Checking trust domain configuration..."
    
    local server_pod
    server_pod=$(kubectl get pods -n "$SPIRE_SERVER_NAMESPACE" -l app.kubernetes.io/name=server -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [[ -n "$server_pod" ]]; then
        # Check server trust domain via agent SPIFFE ID
        local server_trust_domain
        server_trust_domain=$(kubectl exec -n "$SPIRE_SERVER_NAMESPACE" "$server_pod" -- /opt/spire/bin/spire-server agent list 2>/dev/null | grep "SPIFFE ID" | head -1 | grep -o "spiffe://[^/]*" | sed 's/spiffe:\/\///' || echo "")
        
        if [[ "$server_trust_domain" == "$SPIRE_TRUST_DOMAIN" ]]; then
            log_success "✓ Server trust domain correctly configured: $server_trust_domain"
            PASSED_TESTS=$((PASSED_TESTS + 1))
        else
            log_error "✗ Server trust domain mismatch (expected: $SPIRE_TRUST_DOMAIN, got: $server_trust_domain)"
            FAILED_TESTS=$((FAILED_TESTS + 1))
        fi
        TOTAL_TESTS=$((TOTAL_TESTS + 1))
    fi
    
    echo
}

# Display comprehensive status
display_comprehensive_status() {
    log_info "Displaying comprehensive SPIRE status..."
    
    echo "=== SPIRE Server Status ==="
    kubectl get all -n "$SPIRE_SERVER_NAMESPACE" -l app.kubernetes.io/name=server 2>/dev/null || log_warning "No SPIRE server resources found"
    
    echo
    echo "=== SPIRE Agent Status ==="
    kubectl get all -n "$NAMESPACE" -l app.kubernetes.io/name=agent 2>/dev/null || log_warning "No SPIRE agent resources found"
    
    echo
    echo "=== SPIRE Server Logs (last 10 lines) ==="
    kubectl logs -n "$SPIRE_SERVER_NAMESPACE" deployment/spire-server --tail=10 2>/dev/null || log_warning "Could not retrieve server logs"
    
    echo
    echo "=== SPIRE Agent Logs (last 10 lines) ==="
    kubectl logs -n "$NAMESPACE" daemonset/spire-agent --tail=10 2>/dev/null || log_warning "Could not retrieve agent logs"
    
    echo
}

# Display test results summary
display_test_summary() {
    echo
    echo "=============================================="
    echo "           VERIFICATION SUMMARY"
    echo "=============================================="
    echo
    echo "Total Tests: $TOTAL_TESTS"
    echo "Passed: $PASSED_TESTS"
    echo "Failed: $FAILED_TESTS"
    echo
    
    local success_rate
    if [[ "$TOTAL_TESTS" -gt 0 ]]; then
        success_rate=$(( (PASSED_TESTS * 100) / TOTAL_TESTS ))
    else
        success_rate=0
    fi
    
    echo "Success Rate: $success_rate%"
    echo
    
    if [[ "$FAILED_TESTS" -eq 0 ]]; then
        log_success "🎉 All tests passed! SPIRE installation is healthy."
        echo
        echo "Your SPIRE installation is ready for use!"
        echo "Next steps:"
        echo "- Configure your applications to use the SPIFFE workload API"
        echo "- Set up entries for your workloads using spire-server entry create"
        echo "- Configure Bao authentication with SPIFFE JWTs"
    else
        log_error "❌ Some tests failed. Please review the issues above."
        echo
        echo "Common troubleshooting steps:"
        echo "- Check pod logs: kubectl logs -n $SPIRE_SERVER_NAMESPACE deployment/spire-server"
        echo "- Check agent logs: kubectl logs -n $NAMESPACE daemonset/spire-agent"
        echo "- Verify RBAC permissions"
        echo "- Check network connectivity between agents and server"
    fi
    
    echo
    echo "For more information:"
    echo "- SPIRE documentation: https://spiffe.io/docs/latest/"
    echo "- Troubleshooting guide: https://spiffe.io/docs/latest/spire/installing/troubleshooting/"
    
    return $FAILED_TESTS
}

# Main execution
main() {
    log_info "Starting SPIRE verification..."
    echo "Cluster: $CLUSTER_NAME"
    echo "Trust Domain: $SPIRE_TRUST_DOMAIN"
    echo "SPIRE Server Namespace: $SPIRE_SERVER_NAMESPACE"
    echo "SPIRE Agent Namespace: $NAMESPACE"
    echo
    
    check_prerequisites
    check_cluster_status
    check_namespaces
    check_spire_server
    check_spire_agent
    check_rbac
    check_agent_server_connectivity
    check_csi_driver
    check_helm_releases
    check_trust_domain
    display_comprehensive_status
    display_test_summary
}

# Handle script interruption
trap 'log_error "Verification interrupted"; exit 1' INT TERM

# Run main function and exit with appropriate code
main "$@"
exit_code=$?
exit $exit_code
