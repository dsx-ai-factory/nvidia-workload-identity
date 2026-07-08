#!/bin/bash
# create-cluster.sh - Creates the Data Hall Kind cluster
# Uses OVN-Kubernetes CNI for UDN support

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
KIND_CONFIG="${SCRIPT_DIR}/kind-config.yaml"

source "${PROJECT_ROOT}/scripts/common.sh"

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    for cmd in kind kubectl docker helm curl; do
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
    local clusters
    clusters=$(kind get clusters)
    if grep -Fxq "${CLUSTER_NAME}" <<<"${clusters}"; then
        log_info "Cluster '${CLUSTER_NAME}' already exists, skipping creation"
        kind_configure_context "${CLUSTER_NAME}" "${KIND_CONFIG}"
        return 0
    fi
    
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    kind_create_cluster "${CLUSTER_NAME}" "${KIND_CONFIG}"
    kind_configure_context "${CLUSTER_NAME}" "${KIND_CONFIG}"
    
    kubectl wait --for=condition=Ready --timeout=60s -n kube-system pod -l component=kube-apiserver \
        --context "kind-${CLUSTER_NAME}"
    
    log_success "Cluster '${CLUSTER_NAME}' created (CNI installation needed)"
}

tune_node_limits() {
    log_info "Tuning kernel limits for OVN-Kubernetes..."
    for node in $(kind get nodes --name "${CLUSTER_NAME}"); do
        docker exec "$node" sysctl -w fs.inotify.max_user_instances=1024 >/dev/null
        docker exec "$node" sysctl -w fs.inotify.max_user_watches=1048576 >/dev/null
        docker exec "$node" sysctl -w fs.file-max=1048576 >/dev/null
    done
    log_success "Kernel limits tuned"
}

install_ovn() {
    log_info "Installing OVN-Kubernetes CNI..."
    
    local ovn_chart_dir="${PROJECT_ROOT}/third-party/ovn-kubernetes"
    local ovn_values_file="${ovn_chart_dir}/helm/ovn-kubernetes/values-no-ic.yaml"
    local ovn_image_repo="ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-fedora"
    local ovn_image_tag="release-1.2@sha256:a960f93ab6a910ab6ef9ad7ac2afc84ea5399151964c4e85c765554aa3442b0b"
    local ovn_mtu=1300 # Leaves Geneve headroom on Kind's 1400-MTU underlay.
    local multus_version="v4.1.3"
    local multi_network_policy_version="v1.0.1"
    
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
    curl -fsSL "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/${multus_version}/deployments/multus-daemonset.yml" | \
        sed "s|multus-cni:snapshot|multus-cni:${multus_version}|g" | \
        kubectl apply -f - --context "kind-${CLUSTER_NAME}"
    
    # Install MultiNetworkPolicy CRD (idempotent with kubectl apply)
    log_info "Installing MultiNetworkPolicy CRD..."
    kubectl apply \
        -f "https://raw.githubusercontent.com/k8snetworkplumbingwg/multi-networkpolicy/${multi_network_policy_version}/scheme.yml" \
        --context "kind-${CLUSTER_NAME}"
    
    # Get control plane count
    local control_plane_count
    control_plane_count=$(kubectl get node -l node-role.kubernetes.io/control-plane --no-headers \
        --context "kind-${CLUSTER_NAME}" | wc -l)
    
    # Install OVN-Kubernetes
    log_info "Installing OVN-Kubernetes..."
    helm upgrade --install ovn-kubernetes "${ovn_chart_dir}/helm/ovn-kubernetes" \
        --namespace default \
        -f "${ovn_values_file}" \
        --set k8sAPIServer="${k8s_api_server}" \
        --set ovnkube-identity.replicas="${control_plane_count}" \
        --set global.image.repository="${ovn_image_repo}" \
        --set global.image.tag="${ovn_image_tag}" \
        --set mtu="${ovn_mtu}" \
        --set global.enableMultiNetwork=true \
        --set global.enableNetworkSegmentation=true \
        --kube-context "kind-${CLUSTER_NAME}"
    
    # Patch CoreDNS
    log_info "Patching CoreDNS..."
    local corefile
    corefile=$(kubectl get configmap coredns -n kube-system \
        -o jsonpath='{.data.Corefile}' --context "kind-${CLUSTER_NAME}")
    if [[ "${corefile}" == *"kubernetes cluster.local net"* ]]; then
        log_info "CoreDNS is already patched for OVN-Kubernetes"
    else
        (
            kubectl() { command kubectl --context "kind-${CLUSTER_NAME}" "$@"; }
            coredns_patch
        )
    fi
    
    # Update DNS server
    local dns_server
    dns_server=$(awk '$1 == "nameserver" && $2 !~ /^127\./ { print $2; exit }' /etc/resolv.conf)
    if [[ -z "${dns_server}" && -r /run/systemd/resolve/resolv.conf ]]; then
        dns_server=$(awk '$1 == "nameserver" && $2 !~ /^127\./ { print $2; exit }' \
            /run/systemd/resolve/resolv.conf)
    fi
    if [[ -n "${dns_server}" ]]; then
        kubectl get configmap coredns -n kube-system -o yaml --context "kind-${CLUSTER_NAME}" | \
            sed "s|forward \. 8\.8\.8\.8|forward . ${dns_server}|" | \
            kubectl apply -f - --context "kind-${CLUSTER_NAME}"
    fi

    kubectl rollout restart deployment coredns -n kube-system --context "kind-${CLUSTER_NAME}"
    local resource
    # The chart release is in default, but its controllers run in ovn-kubernetes.
    while read -r resource; do
        if ! kubectl rollout status "${resource}" -n ovn-kubernetes --timeout=10m \
                --context "kind-${CLUSTER_NAME}"; then
            kubectl get pods -n ovn-kubernetes -o wide --context "kind-${CLUSTER_NAME}"
            kubectl describe pods -n ovn-kubernetes --context "kind-${CLUSTER_NAME}"
            return 1
        fi
    done < <(kubectl get deployments,statefulsets,daemonsets -n ovn-kubernetes \
        --context "kind-${CLUSTER_NAME}" -o name)
    kubectl rollout status daemonset/kube-multus-ds -n kube-system --timeout=300s \
        --context "kind-${CLUSTER_NAME}"
    kubectl wait --for=condition=Ready nodes --all --timeout=300s \
        --context "kind-${CLUSTER_NAME}"
    kubectl rollout status deployment coredns -n kube-system --timeout=60s --context "kind-${CLUSTER_NAME}"
    kubectl wait --for=condition=Established crd/dpus.provisioning.dpu.nvidia.com \
        --timeout=60s --context "kind-${CLUSTER_NAME}"
    kubectl wait --for=condition=Established crd/launchlayersourcefixtures.e2e.dsx-identity.nvidia.com \
        --timeout=60s --context "kind-${CLUSTER_NAME}"
    
    log_success "OVN-Kubernetes CNI installed"
}

apply_source_crd_fixtures() {
    log_info "Installing the Kind source API fixtures..."
    kubectl apply -f "${SCRIPT_DIR}/dpu-crd-fixture.yaml" --context "kind-${CLUSTER_NAME}"
    kubectl apply -f "${SCRIPT_DIR}/launch-layer-source-fixture-crd.yaml" --context "kind-${CLUSTER_NAME}"
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
    kind delete cluster --name "${CLUSTER_NAME}"
    local clusters
    clusters=$(kind get clusters)
    if grep -Fxq "${CLUSTER_NAME}" <<<"${clusters}"; then
        log_error "Cluster '${CLUSTER_NAME}' still exists after deletion"
        return 1
    fi
    log_success "Cluster '${CLUSTER_NAME}' is absent"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            check_prerequisites
            create_cluster
            tune_node_limits
            apply_source_crd_fixtures
            install_ovn
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
