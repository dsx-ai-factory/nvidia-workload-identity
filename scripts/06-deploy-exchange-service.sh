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
TENANT_PREFIX="${TENANT_PREFIX:-tenant1}"
BASE_DOMAIN="${BASE_DOMAIN:-sjc-1.k8s2-control.nvidia.com}"

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

# Configure CoreDNS for wildcard tenant domain routing
configure_tenant_dns() {
    log_info "Configuring CoreDNS for wildcard tenant domain..."
    
    local escaped_domain="${BASE_DOMAIN//./\\.}"
    local service_fqdn="spiffe-exchange-service.${NAMESPACE}.svc.cluster.local"
    
    # Check if already configured (idempotent)
    if kubectl get configmap coredns -n kube-system -o jsonpath='{.data.Corefile}' | grep -qF "rewrite name regex .*\.${escaped_domain}"; then
        log_info "CoreDNS already configured for *.${BASE_DOMAIN}"
        return 0
    fi
    
    log_info "Adding wildcard DNS rewrite: *.${BASE_DOMAIN} -> ${service_fqdn}"
    
    # Get current Corefile
    local corefile
    corefile=$(kubectl get configmap coredns -n kube-system -o jsonpath='{.data.Corefile}')
    
    # Build rewrite rule - rewrites *.BASE_DOMAIN to exchange service FQDN
    local rewrite_rule="rewrite name regex .*\\.${escaped_domain} ${service_fqdn}"
    
    # Insert rewrite rule before "kubernetes cluster.local"
    local new_corefile
    new_corefile=$(echo "$corefile" | awk -v rule="    $rewrite_rule" '/kubernetes cluster.local/{print rule}1')
    
    # Create new configmap YAML and apply
    kubectl create configmap coredns -n kube-system --from-literal=Corefile="$new_corefile" --dry-run=client -o yaml | kubectl apply -f -
    
    # Restart CoreDNS to pick up changes
    kubectl rollout restart deployment coredns -n kube-system
    kubectl rollout status deployment coredns -n kube-system --timeout=60s
    
    log_success "CoreDNS configured for *.${BASE_DOMAIN}"
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
    
    kubectl delete -f "${EXCHANGE_SERVICE_FILE}" -n ${NAMESPACE} --ignore-not-found=true
    
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
            configure_tenant_dns
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

