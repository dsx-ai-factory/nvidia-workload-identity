#!/bin/bash
# create-cluster.sh - Creates the k2 Kind cluster (secondary cluster)
# Uses OVN-Kubernetes CNI for UDN support

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
KIND_CONFIG="${SCRIPT_DIR}/kind-config.yaml"

source "${PROJECT_ROOT}/scripts/common.sh"

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    for cmd in kind kubectl docker helm; do
        if ! command -v "$cmd" &> /dev/null; then
            log_error "$cmd is not installed"
            exit 1
        fi
    done
    
    if ! docker info &> /dev/null; then
        log_error "Docker is not running"
        exit 1
    fi
    
    # Check OVN submodule
    if [[ ! -d "${PROJECT_ROOT}/third-party/ovn-kubernetes/helm" ]]; then
        log_error "OVN-Kubernetes submodule not found"
        log_error "Run: git submodule update --init third-party/ovn-kubernetes"
        exit 1
    fi
    
    log_success "All prerequisites met"
}

create_cluster() {
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Cluster '${CLUSTER_NAME}' already exists, skipping creation"
        return 0
    fi
    
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    kind create cluster --config="${KIND_CONFIG}" --name="${CLUSTER_NAME}"
    
    kubectl wait --for=condition=Ready --timeout=60s -n kube-system pod -l component=kube-apiserver \
        --context "kind-${CLUSTER_NAME}"
    
    log_success "Cluster '${CLUSTER_NAME}' created (CNI installation needed)"
}

install_ovn() {
    # Check if OVN is already installed
    if helm status ovn-kubernetes --kube-context "kind-${CLUSTER_NAME}" &>/dev/null; then
        log_info "OVN-Kubernetes already installed, skipping"
        kubectl wait --for=condition=Ready nodes --all --timeout=300s --context "kind-${CLUSTER_NAME}"
        return 0
    fi
    
    log_info "Installing OVN-Kubernetes CNI..."
    
    local ovn_chart_dir="${PROJECT_ROOT}/third-party/ovn-kubernetes"
    local ovn_values_file="${ovn_chart_dir}/helm/ovn-kubernetes/values-no-ic.yaml"
    local ovn_image_repo="ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu"
    local ovn_image_tag="release-1.1"
    
    # Get API server endpoint
    local api_server_ip
    api_server_ip=$(kubectl get pods -n kube-system -l component=kube-apiserver \
        -o jsonpath='{.items[0].status.hostIP}' --context "kind-${CLUSTER_NAME}")
    local k8s_api_server="https://${api_server_ip}:6443"
    
    # Configure IPv6 in Kind nodes
    export IP_FAMILY="ipv4"
    export OCI_BIN="docker"
    export KIND_CLUSTER_NAME="${CLUSTER_NAME}"
    source "${ovn_chart_dir}/contrib/kind-common"
    docker_disable_ipv6
    
    # Install multus-cni (idempotent with kubectl apply)
    log_info "Installing multus-cni..."
    kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset.yml \
        --context "kind-${CLUSTER_NAME}"
    kubectl wait --for=condition=Ready pod -l app=multus -n kube-system --timeout=300s \
        --context "kind-${CLUSTER_NAME}"
    
    # Install MultiNetworkPolicy CRD (idempotent with kubectl apply)
    log_info "Installing MultiNetworkPolicy CRD..."
    kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multi-networkpolicy/master/scheme.yml \
        --context "kind-${CLUSTER_NAME}"
    
    # Get control plane count
    local control_plane_count
    control_plane_count=$(kubectl get node -l node-role.kubernetes.io/control-plane --no-headers \
        --context "kind-${CLUSTER_NAME}" | wc -l)
    
    # Install OVN-Kubernetes
    log_info "Installing OVN-Kubernetes..."
    helm upgrade --install ovn-kubernetes "${ovn_chart_dir}/helm/ovn-kubernetes" \
        -f "${ovn_values_file}" \
        --set k8sAPIServer="${k8s_api_server}" \
        --set ovnkube-identity.replicas="${control_plane_count}" \
        --set global.image.repository="${ovn_image_repo}" \
        --set global.image.tag="${ovn_image_tag}" \
        --set global.enableMultiNetwork=true \
        --set global.enableNetworkSegmentation=true \
        --kube-context "kind-${CLUSTER_NAME}"
    
    # Wait for OVN pods
    log_info "Waiting for OVN-Kubernetes pods..."
    kubectl wait --for=condition=Ready pod -l app=ovnkube-node -n ovn-kubernetes --timeout=300s \
        --context "kind-${CLUSTER_NAME}"
    kubectl wait --for=condition=Ready pod -l name=ovnkube-master -n ovn-kubernetes --timeout=300s \
        --context "kind-${CLUSTER_NAME}"
    
    # Wait for nodes
    kubectl wait --for=condition=Ready nodes --all --timeout=300s --context "kind-${CLUSTER_NAME}"
    
    # Patch CoreDNS
    log_info "Patching CoreDNS..."
    coredns_patch
    
    # Update DNS server
    local dns_server
    dns_server=$(resolvectl status 2>/dev/null | grep "DNS Servers" | head -1 | awk '{print $3}')
    if [[ -n "${dns_server}" ]]; then
        kubectl get configmap coredns -n kube-system -o yaml --context "kind-${CLUSTER_NAME}" | \
            sed "s|forward \. 8\.8\.8\.8|forward . ${dns_server}|" | \
            kubectl apply -f - --context "kind-${CLUSTER_NAME}"
    fi
    
    kubectl rollout restart deployment coredns -n kube-system --context "kind-${CLUSTER_NAME}"
    kubectl rollout status deployment coredns -n kube-system --timeout=60s --context "kind-${CLUSTER_NAME}"
    
    log_success "OVN-Kubernetes CNI installed"
}

display_info() {
    log_success "k2 cluster setup complete"
    echo
    echo "Cluster: ${CLUSTER_NAME}"
    echo "Context: kind-${CLUSTER_NAME}"
    echo
    kubectl get nodes --context "kind-${CLUSTER_NAME}"
}

cleanup() {
    log_info "Cleaning up k2 cluster..."
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        kind delete cluster --name "${CLUSTER_NAME}"
        log_success "Cluster '${CLUSTER_NAME}' deleted"
    else
        log_info "Cluster '${CLUSTER_NAME}' not found"
    fi
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            check_prerequisites
            create_cluster
            install_ovn
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
