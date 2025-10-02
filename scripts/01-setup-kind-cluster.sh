#!/bin/bash

# setup-kind-cluster.sh
# Creates a Kind cluster configured for SPIRE deployment

set -euo pipefail

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-spire-demo}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
KIND_CONFIG_FILE="${KIND_CONFIG_FILE:-${PROJECT_ROOT}/config/kind-config.yaml}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kind &> /dev/null; then
        log_error "kind is not installed. Please install kind first."
        log_info "Installation: https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    if ! command -v docker &> /dev/null; then
        log_error "docker is not installed. Please install docker first."
        exit 1
    fi
    
    if ! command -v curl &> /dev/null; then
        log_error "curl is not installed. Please install curl first."
        exit 1
    fi
    
    if ! command -v bao &> /dev/null; then
        log_error "bao CLI is not installed. Please install bao CLI first."
        log_info "Installation: https://openbao.org/docs/install/"
        exit 1
    fi
    
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed. Please install helm first."
        log_info "Installation: https://helm.sh/docs/intro/install/"
        exit 1
    fi
    
    # Check if docker is running
    if ! docker info &> /dev/null; then
        log_error "Docker is not running. Please start Docker first."
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Check Kind configuration
check_kind_config() {
    log_info "Checking Kind cluster configuration..."
    
    if [[ ! -f "${KIND_CONFIG_FILE}" ]]; then
        log_error "Kind configuration file not found: ${KIND_CONFIG_FILE}"
        exit 1
    fi
    
    log_success "Kind configuration validated at ${KIND_CONFIG_FILE}"
}

# Create the Kind cluster
create_cluster() {
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    
    # Check if cluster already exists and delete it
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Deleting existing cluster '${CLUSTER_NAME}'..."
        kind delete cluster --name "${CLUSTER_NAME}"
    fi
    
    # Create the cluster
    log_info "Creating new cluster..."
    kind create cluster --config="${KIND_CONFIG_FILE}" --name="${CLUSTER_NAME}"
    
    # Wait for API server to be available
    log_info "Waiting for API server to be available..."
    kubectl wait --for=condition=Ready --timeout=60s -n kube-system pod -l component=kube-apiserver
    
    log_success "Cluster '${CLUSTER_NAME}' created (CNI installation needed for nodes to be ready)"
}

# Install OVN-Kubernetes CNI
install_ovn() {
    log_info "Installing OVN-Kubernetes CNI..."
    
    local ovn_namespace="ovn-kubernetes"
    local ovn_git_tag="v1.1.0"
    local ovn_image_tag="release-1.1"
    local ovn_chart_dir="${PROJECT_ROOT}/third-party/ovn-kubernetes"
    local ovn_values_file="${ovn_chart_dir}/helm/ovn-kubernetes/values-no-ic.yaml"
    
    # Check if OVN-Kubernetes submodule exists
    if [[ ! -d "${ovn_chart_dir}/helm" ]]; then
        log_error "OVN-Kubernetes submodule not found at ${ovn_chart_dir}"
        log_error "Please run: git submodule update --init third-party/ovn-kubernetes"
        exit 1
    fi
    log_info "Using OVN-Kubernetes Helm chart from submodule"
    
    # Get Kubernetes API server endpoint
    log_info "Determining Kubernetes API server endpoint..."
    local api_server_ip
    api_server_ip=$(kubectl get pods -n kube-system -l component=kube-apiserver -o jsonpath='{.items[0].status.hostIP}' 2>/dev/null || echo "")
    if [[ -z "${api_server_ip}" ]]; then
        log_error "Could not determine Kubernetes API server IP"
        exit 1
    fi
    local k8s_api_server="https://${api_server_ip}:6443"
    log_info "Kubernetes API server: ${k8s_api_server}"
    
    # Enable IPv6 in Kind nodes for OVN compatibility BEFORE installing OVN
    # This is required even for IPv4-only deployments
    # Must be done before OVN installation per kind-helm.sh
    log_info "Configuring IPv6 in Kind nodes for OVN compatibility..."
    export IP_FAMILY="ipv4"
    export OCI_BIN="docker"
    export KIND_CLUSTER_NAME="${CLUSTER_NAME}"
    source "${PROJECT_ROOT}/third-party/ovn-kubernetes/contrib/kind-common"
    docker_disable_ipv6
    
    # Get number of control plane nodes for ovnkube-identity replicas
    local control_plane_count
    control_plane_count=$(kubectl get node -l node-role.kubernetes.io/control-plane --no-headers | wc -l)
    
    # Install OVN-Kubernetes via Helm from submodule chart (per README.md)
    log_info "Installing OVN-Kubernetes from Helm chart..."
    helm install ovn-kubernetes "${ovn_chart_dir}/helm/ovn-kubernetes" \
        -f "${ovn_values_file}" \
        --set k8sAPIServer="${k8s_api_server}" \
        --set ovnkube-identity.replicas="${control_plane_count}" \
        --set global.image.repository="ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu" \
        --set global.image.tag="${ovn_image_tag}"
    
    # Wait for OVN-Kubernetes pods to be ready
    log_info "Waiting for OVN-Kubernetes pods to be ready..."
    kubectl wait --for=condition=Ready pod -l app=ovnkube-node -n ovn-kubernetes --timeout=300s
    kubectl wait --for=condition=Ready pod -l name=ovnkube-master -n ovn-kubernetes --timeout=300s
    kubectl wait --for=condition=Ready pod -l name=ovnkube-db -n ovn-kubernetes --timeout=300s
    
    # Wait for nodes to be ready with network
    log_info "Waiting for nodes to be ready with OVN networking..."
    kubectl wait --for=condition=Ready nodes --all --timeout=300s
    
    # Patch CoreDNS configuration now that OVN is installed and nodes are ready
    log_info "Patching CoreDNS configuration for OVN-Kubernetes..."
    
    # Apply standard coredns_patch from OVN (adds "net" domain, replaces forward with 8.8.8.8)
    coredns_patch
    
    # Replace 8.8.8.8 with actual upstream DNS server (routable from pods)
    # Get the first DNS server from systemd-resolved
    local dns_server
    dns_server=$(resolvectl status 2>/dev/null | grep "DNS Servers" | head -1 | awk '{print $3}')
    if [[ -z "${dns_server}" ]]; then
        log_error "Could not determine upstream DNS server"
        exit 1
    fi
    log_info "Updating CoreDNS to use upstream DNS server: ${dns_server}"
    kubectl get configmap coredns -n kube-system -o yaml | \
        sed "s|forward \. 8\.8\.8\.8|forward . ${dns_server}|" | \
        kubectl apply -f -
    
    # Restart CoreDNS to apply the new configuration
    log_info "Restarting CoreDNS to apply configuration..."
    kubectl rollout restart deployment coredns -n kube-system
    kubectl rollout status deployment coredns -n kube-system --timeout=60s
    
    # Validate DNS resolution is working
    log_info "Validating DNS resolution..."
    local dns_test_output
    if dns_test_output=$(kubectl run dns-test --image=busybox:1.36 --rm -i --restart=Never --timeout=30s -- nslookup google.com 2>&1); then
        log_success "DNS resolution validated successfully"
    else
        log_error "DNS resolution validation failed"
        echo "${dns_test_output}"
        exit 1
    fi
    
    log_success "OVN-Kubernetes CNI installed successfully"
}

# Display cluster information
display_cluster_info() {
    log_success "Kind cluster setup complete!"
    echo
    echo "Cluster Information:"
    echo "==================="
    echo "Cluster Name: ${CLUSTER_NAME}"
    echo "Kubernetes Version: $(kubectl version --short --client 2>/dev/null | grep 'Client Version' || echo 'N/A')"
    echo "Nodes:"
    kubectl get nodes -o wide
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Run 'make install' to complete the full setup"
    echo "2. Run 'make test' to verify the installation"
    echo "3. Use 'make clean' to clean up when done"
}

# Cleanup function
cleanup_cluster() {
    log_info "Cleaning up Kind cluster..."
    
    # Check if cluster exists
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Deleting Kind cluster '${CLUSTER_NAME}'..."
        kind delete cluster --name "${CLUSTER_NAME}"
        log_success "Kind cluster '${CLUSTER_NAME}' deleted"
    else
        log_info "Kind cluster '${CLUSTER_NAME}' not found"
    fi
    
    log_success "Cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Creates or manages a Kind cluster configured for SPIRE deployment"
    echo ""
    echo "Options:"
    echo "  cleanup    Delete the Kind cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  CLUSTER_NAME      Name of the Kind cluster (default: spire-demo)"
    echo "  KIND_CONFIG_FILE  Path to Kind configuration file"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_cluster
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting Kind cluster setup with OVN-Kubernetes CNI..."
            check_prerequisites
            check_kind_config
            create_cluster
            install_ovn
            display_cluster_info
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
