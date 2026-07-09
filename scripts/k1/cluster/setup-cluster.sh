#!/bin/bash
# setup-cluster.sh - Creates the k1 Kind cluster
# Uses default kindnet CNI - no OVN overhead needed

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
KIND_CONFIG="${SCRIPT_DIR}/kind-config.yaml"

source "${PROJECT_ROOT}/scripts/common.sh"

create_cluster() {
    local clusters
    clusters=$(kind get clusters)
    if grep -Fxq "${CLUSTER_NAME}" <<<"${clusters}"; then
        log_info "Cluster '${CLUSTER_NAME}' already exists, skipping creation"
        kind_configure_context "${CLUSTER_NAME}" "${KIND_CONFIG}"
        kubectl wait --for=condition=Ready nodes --all --timeout=120s --context "kind-${CLUSTER_NAME}"
        log_success "Cluster '${CLUSTER_NAME}' ready"
        return 0
    fi
    
    log_info "Creating Kind cluster '${CLUSTER_NAME}'..."
    kind_create_cluster "${CLUSTER_NAME}" "${KIND_CONFIG}"
    kind_configure_context "${CLUSTER_NAME}" "${KIND_CONFIG}"
    kubectl wait --for=condition=Ready nodes --all --timeout=120s \
        --context "kind-${CLUSTER_NAME}" >/dev/null

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

main() {
    create_cluster
    kind_configure_registry "${CLUSTER_NAME}"
    display_info
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main
