#!/bin/bash
# 08-deploy-metadata-service.sh
# Deploys SPIFFE metadata service DaemonSet for tenant isolation

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
METADATA_DAEMONSET_FILE="${PROJECT_ROOT}/config/metadata-service-daemonset.yaml.tmpl"
METADATA_SERVICE_FILE="${PROJECT_ROOT}/config/metadata-service.yaml.tmpl"
METADATA_TENANT="${METADATA_TENANT:-default}"
METADATA_NAMESPACE="${METADATA_NAMESPACE:-default}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    # Check if cluster is accessible
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot access Kubernetes cluster. Please ensure cluster is running."
        exit 1
    fi
    
    # Check if metadata service template files exist
    if [[ ! -f "${METADATA_DAEMONSET_FILE}" ]]; then
        log_error "Metadata service DaemonSet file not found: ${METADATA_DAEMONSET_FILE}"
        exit 1
    fi
    
    if [[ ! -f "${METADATA_SERVICE_FILE}" ]]; then
        log_error "Metadata service Service file not found: ${METADATA_SERVICE_FILE}"
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Create metadata service DaemonSet configuration from template
create_metadata_daemonset_config() {
    sed -e "s/TENANT_PLACEHOLDER/${METADATA_TENANT}/g" \
        "${METADATA_DAEMONSET_FILE}"
}

# Create metadata service Service configuration from template
create_metadata_service_config() {
    sed -e "s/TENANT_PLACEHOLDER/${METADATA_TENANT}/g" \
        "${METADATA_SERVICE_FILE}"
}

# Deploy metadata service
deploy_metadata_service() {
    log_info "Deploying metadata service for tenant '${METADATA_TENANT}'..."
    
    # Check if DaemonSet already exists
    if kubectl get daemonset metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE} &> /dev/null; then
        log_info "Metadata service DaemonSet already exists, updating..."
        create_metadata_daemonset_config | kubectl apply -n ${METADATA_NAMESPACE} -f -
    else
        log_info "Creating metadata service DaemonSet..."
        create_metadata_daemonset_config | kubectl apply -n ${METADATA_NAMESPACE} -f -
    fi
    
    # Check if Service already exists
    if kubectl get service metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE} &> /dev/null; then
        log_info "Metadata service Service already exists, updating..."
        create_metadata_service_config | kubectl apply -n ${METADATA_NAMESPACE} -f -
    else
        log_info "Creating metadata service Service..."
        create_metadata_service_config | kubectl apply -n ${METADATA_NAMESPACE} -f -
    fi
    
    log_success "Metadata service deployed"
}

# Wait for metadata service pods
wait_for_metadata_service() {
    log_info "Waiting for metadata service pods to be ready..."
    
    local timeout=60
    local elapsed=0
    
    while [ $elapsed -lt $timeout ]; do
        local ready_pods=$(kubectl get pods -n ${METADATA_NAMESPACE} -l app=metadata-service,tenant=${METADATA_TENANT} --field-selector=status.phase=Running --no-headers 2>/dev/null | wc -l)
        
        if [ "$ready_pods" -gt 0 ]; then
            log_success "Metadata service pods are ready!"
            return 0
        fi
        
        log_info "Waiting for pods... (${elapsed}s/${timeout}s)"
        sleep 5
        elapsed=$((elapsed + 5))
    done
    
    log_warning "Metadata service pods not ready within ${timeout} seconds"
    return 1
}

# Display status
display_status() {
    log_success "Metadata service deployment complete!"
    echo
    echo "Metadata Service Status:"
    echo "========================"
    
    # Show DaemonSet status
    echo "DaemonSet:"
    kubectl get daemonset metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE} 2>/dev/null || echo "  DaemonSet not found"
    
    # Show Service status
    echo
    echo "Service:"
    kubectl get service metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE} 2>/dev/null || echo "  Service not found"
    
    # Show Pods
    echo
    echo "Pods:"
    kubectl get pods -n ${METADATA_NAMESPACE} -l app=metadata-service,tenant=${METADATA_TENANT} 2>/dev/null || echo "  No pods found"
    
    # Show which nodes have the DaemonSet pods
    echo
    echo "Node Distribution:"
    echo "=================="
    kubectl get pods -n ${METADATA_NAMESPACE} -l app=metadata-service,tenant=${METADATA_TENANT} -o wide 2>/dev/null | awk 'NR>1 {print "  Node:", $7}' || echo "  No pods found"
    
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Check pod logs: kubectl logs -l app=metadata-service,tenant=${METADATA_TENANT} -n ${METADATA_NAMESPACE}"
    echo "2. Test service: kubectl run -it --rm test-pod --image=curlimages/curl --restart=Never -- curl http://metadata-service-${METADATA_TENANT}"
    echo "3. Deploy VM with 'make vm' or 'scripts/09-deploy-vm.sh'"
}

# Cleanup function
cleanup_metadata_service() {
    log_info "Cleaning up metadata service for tenant '${METADATA_TENANT}'..."
    
    # Delete DaemonSet
    if kubectl get daemonset metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE} &> /dev/null; then
        log_info "Deleting DaemonSet..."
        kubectl delete daemonset metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE}
    fi
    
    # Delete Service
    if kubectl get service metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE} &> /dev/null; then
        log_info "Deleting Service..."
        kubectl delete service metadata-service-${METADATA_TENANT} -n ${METADATA_NAMESPACE}
    fi
    
    log_success "Metadata service cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Deploys SPIFFE metadata service DaemonSet with tenant isolation"
    echo ""
    echo "Options:"
    echo "  cleanup    Remove the metadata service from the cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  METADATA_TENANT      Tenant identifier (default: default)"
    echo "  METADATA_NAMESPACE   Namespace for deployment (default: default)"
    echo ""
    echo "Prerequisites:"
    echo "  - Kubernetes cluster must be running and accessible"
    echo "  - kubectl must be configured to access the cluster"
    echo "  - Nodes must have 'vm-tenant' label matching METADATA_TENANT"
    echo ""
    echo "Files:"
    echo "  - DaemonSet template: ${METADATA_DAEMONSET_FILE}"
    echo "  - Service template: ${METADATA_SERVICE_FILE}"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_metadata_service
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting metadata service deployment..."
            check_prerequisites
            deploy_metadata_service
            wait_for_metadata_service
            display_status
            ;;
        *)
            echo "Error: Unknown option '$1'"
            echo ""
            usage
            exit 1
            ;;
    esac
}

# Handle script interruption
trap 'log_error "Script interrupted"; exit 1' INT TERM

# Run main function
main "$@"

