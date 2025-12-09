#!/bin/bash
# 06-deploy-exchange-service.sh
# Deploys SPIFFE exchange service for JWT transformation

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
EXCHANGE_SERVICE_FILE="${PROJECT_ROOT}/config/spiffe-exchange-service.yaml"
EXCHANGE_IMAGE_NAME="spiffe-exchange-service"
IMAGE_TAG="latest"
NAMESPACE="${NAMESPACE:-default}"
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot access Kubernetes cluster. Please ensure cluster is running."
        exit 1
    fi
    
    if [[ ! -f "${EXCHANGE_SERVICE_FILE}" ]]; then
        log_error "Exchange service config file not found: ${EXCHANGE_SERVICE_FILE}"
        exit 1
    fi
    
    if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_error "Kind cluster '${CLUSTER_NAME}' not found. Please run 'make cluster' first."
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Build Docker image
build_image() {
    log_info "Building SPIFFE exchange service Docker image..."
    
    cd "${PROJECT_ROOT}"
    if docker build -t ${EXCHANGE_IMAGE_NAME}:${IMAGE_TAG} -f Dockerfile.spiffe-exchange-service .; then
        log_success "Exchange service Docker image built successfully"
    else
        log_error "Failed to build exchange service Docker image"
        exit 1
    fi
}

# Load image into Kind cluster
load_image() {
    log_info "Loading exchange service image into Kind cluster..."
    
    if kind load docker-image ${EXCHANGE_IMAGE_NAME}:${IMAGE_TAG} --name "${CLUSTER_NAME}"; then
        log_success "Exchange service image loaded into Kind cluster"
    else
        log_error "Failed to load exchange service image into Kind cluster"
        exit 1
    fi
}

# Deploy exchange service
deploy_exchange_service() {
    log_info "Deploying SPIFFE exchange service..."
    
    kubectl apply -f "${EXCHANGE_SERVICE_FILE}" -n ${NAMESPACE}
    
    log_success "Exchange service deployed"
}

# Wait for exchange service
wait_for_exchange_service() {
    log_info "Waiting for exchange service to be ready..."
    
    if kubectl wait --for=condition=ready pod -l app=spiffe-exchange-service -n ${NAMESPACE} --timeout=60s; then
        log_success "Exchange service is ready"
    else
        log_warning "Exchange service not ready within 60 seconds"
    fi
}

# Display status
display_status() {
    log_success "Exchange service deployment complete!"
    echo
    echo "Exchange Service Status:"
    echo "========================"
    
    echo "Deployment:"
    kubectl get deployment spiffe-exchange-service -n ${NAMESPACE} 2>/dev/null || echo "  Deployment not found"
    
    echo
    echo "Service:"
    kubectl get service spiffe-exchange-service -n ${NAMESPACE} 2>/dev/null || echo "  Service not found"
    
    echo
    echo "Pods:"
    kubectl get pods -n ${NAMESPACE} -l app=spiffe-exchange-service 2>/dev/null || echo "  No pods found"
    
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Check exchange service logs: kubectl logs -l app=spiffe-exchange-service -n ${NAMESPACE}"
    echo "2. Deploy metadata service with 'make metadata-service' or 'scripts/09-deploy-metadata-service.sh'"
}

# Cleanup function
cleanup_exchange_service() {
    log_info "Cleaning up exchange service..."
    
    if kubectl get deployment spiffe-exchange-service -n ${NAMESPACE} &> /dev/null; then
        log_info "Deleting exchange service..."
        kubectl delete -f "${EXCHANGE_SERVICE_FILE}" -n ${NAMESPACE} --ignore-not-found=true
    fi
    
    log_success "Exchange service cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Deploys SPIFFE exchange service for JWT transformation"
    echo ""
    echo "Options:"
    echo "  cleanup    Remove the exchange service from the cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  NAMESPACE      Namespace for deployment (default: default)"
    echo "  CLUSTER_NAME   Kind cluster name (default: spire-demo)"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_exchange_service
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting exchange service deployment..."
            check_prerequisites
            build_image
            load_image
            deploy_exchange_service
            wait_for_exchange_service
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

