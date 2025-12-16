#!/bin/bash
# setup-cluster.sh - Creates the k1 Kind cluster (primary cluster)
# Uses default kindnet CNI - no OVN overhead needed

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
KIND_CONFIG="${SCRIPT_DIR}/kind-config.yaml"

source "${PROJECT_ROOT}/scripts/common.sh"

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    for cmd in kind kubectl docker helm bao; do
        if ! command -v "$cmd" &> /dev/null; then
            log_error "$cmd is not installed"
            exit 1
        fi
    done
    
    if ! docker info &> /dev/null; then
        log_error "Docker is not running"
        exit 1
    fi
    
    log_success "All prerequisites met"
}

create_cluster() {
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Cluster '${CLUSTER_NAME}' already exists, skipping creation"
        kubectl wait --for=condition=Ready nodes --all --timeout=120s --context "kind-${CLUSTER_NAME}"
        log_success "Cluster '${CLUSTER_NAME}' ready"
        return 0
    fi
    
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    kind create cluster --config="${KIND_CONFIG}" --name="${CLUSTER_NAME}"
    
    kubectl wait --for=condition=Ready --timeout=120s -n kube-system pod -l component=kube-apiserver --context "kind-${CLUSTER_NAME}"
    kubectl wait --for=condition=Ready nodes --all --timeout=120s --context "kind-${CLUSTER_NAME}"
    
    log_success "Cluster '${CLUSTER_NAME}' created and ready"
}

display_info() {
    log_success "k1 cluster setup complete"
    echo
    echo "Cluster: ${CLUSTER_NAME}"
    echo "Context: kind-${CLUSTER_NAME}"
    echo
    kubectl get nodes --context "kind-${CLUSTER_NAME}"
}

cleanup() {
    log_info "Cleaning up k1 cluster..."
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
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

