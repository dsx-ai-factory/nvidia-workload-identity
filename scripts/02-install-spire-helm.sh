#!/bin/bash

# install-spire-helm.sh
# Installs SPIRE server and agent using Helm charts

set -euo pipefail

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
NAMESPACE="${SPIRE_NAMESPACE:-spire-system}"
SPIRE_SERVER_NAMESPACE="${SPIRE_SERVER_NAMESPACE:-spire-server}"
HELM_RELEASE_NAME="${HELM_RELEASE_NAME:-spire}"
SPIRE_TRUST_DOMAIN="${SPIRE_TRUST_DOMAIN:-example.org}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed. Please install helm first."
        log_info "Installation: https://helm.sh/docs/intro/install/"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    # Check if cluster is running
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Kubernetes cluster is not accessible. Please ensure your Kind cluster is running."
        log_info "Run './setup-kind-cluster.sh' first to create the cluster."
        exit 1
    fi
    
    # Check if it's the right cluster
    current_context=$(kubectl config current-context)
    if [[ ! "$current_context" == *"$CLUSTER_NAME"* ]]; then
        log_warning "Current kubectl context is '$current_context', expected context containing '$CLUSTER_NAME'"
        read -p "Continue anyway? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
    
    log_success "All prerequisites are met"
}

# Add SPIRE Helm repository
add_helm_repo() {
    log_info "Adding SPIRE Helm repository..."
    
    helm repo add spiffe https://spiffe.github.io/helm-charts-hardened/ || {
        log_warning "Failed to add SPIRE repo, it might already exist"
    }
    
    helm repo update
    
    log_success "SPIRE Helm repository added and updated"
}

# Check SPIRE server configuration
check_spire_server_config() {
    log_info "Checking SPIRE server configuration..."
    
    local config_file="${SCRIPT_DIR}/config/spire-server-values.yaml"
    
    if [[ ! -f "$config_file" ]]; then
        log_error "SPIRE server configuration file not found: $config_file"
        exit 1
    fi
    
    log_success "SPIRE server configuration ready at ${config_file}"
}



# Install SPIRE using Helm
install_spire() {
    log_info "Installing SPIRE using Helm (latest method)..."
    
    # Step 1: Install SPIRE CRDs first
    log_info "Installing SPIRE CRDs..."
    helm upgrade --install spire-crds spiffe/spire-crds \
        --namespace "${SPIRE_SERVER_NAMESPACE}" \
        --create-namespace \
        --timeout 5m \
        --wait
    
    log_success "SPIRE CRDs installed successfully"
    
    # Step 2: Install SPIRE server and agent together (latest chart approach)
    log_info "Installing SPIRE server and agent..."
    helm upgrade --install spire spiffe/spire \
        --namespace "${SPIRE_SERVER_NAMESPACE}" \
        --create-namespace \
        --values "${SCRIPT_DIR}/config/spire-server-values.yaml" \
        --timeout 10m \
        --wait
    
    log_success "SPIRE installed successfully"
    
    # Wait for server to be ready
    log_info "Waiting for SPIRE server to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=server \
        --namespace="${SPIRE_SERVER_NAMESPACE}" \
        --timeout=300s
}

# Verify installation
verify_installation() {
    log_info "Verifying SPIRE installation..."
    
    # Check server pods
    echo "SPIRE Server Pods:"
    kubectl get pods -n "${SPIRE_SERVER_NAMESPACE}" -l app.kubernetes.io/name=server
    
    # Check agent pods
    echo "SPIRE Agent Pods:"
    kubectl get pods -n "${SPIRE_SERVER_NAMESPACE}" -l app.kubernetes.io/name=agent
    
    # Check services
    echo "SPIRE Services:"
    kubectl get svc -n "${SPIRE_SERVER_NAMESPACE}"
    
    # Check if agents are connected to server
    log_info "Checking SPIRE agent connectivity..."
    server_pod=$(kubectl get pods -n "${SPIRE_SERVER_NAMESPACE}" -l app.kubernetes.io/name=server -o jsonpath='{.items[0].metadata.name}')
    
    if [[ -n "$server_pod" ]]; then
        echo "Connected agents:"
        kubectl exec -n "${SPIRE_SERVER_NAMESPACE}" "$server_pod" -- \
            /opt/spire/bin/spire-server agent list || log_warning "Could not list agents yet, they may still be connecting"
    fi
    
    log_success "SPIRE installation verification complete"
}

# Display post-installation information
display_info() {
    log_success "SPIRE installation complete!"
    echo
    echo "Installation Summary:"
    echo "===================="
    echo "Trust Domain: ${SPIRE_TRUST_DOMAIN}"
    echo "SPIRE Server Namespace: ${SPIRE_SERVER_NAMESPACE}"
    echo "SPIRE Agent Namespace: ${NAMESPACE}"
    echo "Cluster Name: ${CLUSTER_NAME}"
    echo
    echo "SPIRE Server Service:"
    kubectl get svc -n "${SPIRE_SERVER_NAMESPACE}" spire-server
    echo
    echo "Access SPIRE Server:"
    echo "kubectl port-forward -n ${SPIRE_SERVER_NAMESPACE} svc/spire-server 8081:8081"
    echo
    echo "Useful Commands:"
    echo "==============="
    echo "# List server agents:"
    echo "kubectl exec -n ${SPIRE_SERVER_NAMESPACE} deployment/spire-server -- /opt/spire/bin/spire-server agent list"
    echo
    echo "# Check agent logs:"
    echo "kubectl logs -n ${NAMESPACE} daemonset/spire-agent"
    echo
    echo "# Check server logs:"
    echo "kubectl logs -n ${SPIRE_SERVER_NAMESPACE} deployment/spire-server"
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Run './verify-spire.sh' to perform comprehensive verification"
    echo "2. Configure your applications to use SPIFFE workload API"
    echo "3. Check the SPIFFE token location: /var/run/secrets/nvidia.com/spiffe/token"
}

# Main execution
main() {
    log_info "Starting SPIRE installation with Helm..."
    
    check_prerequisites
    add_helm_repo
    check_spire_server_config
    install_spire
    verify_installation
    display_info
}

# Handle script interruption
trap 'log_error "Script interrupted"; exit 1' INT TERM

# Run main function
main "$@"
