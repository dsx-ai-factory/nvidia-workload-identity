#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# create-cluster.sh - Creates the no-CNI Data Hall Kind cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
KIND_CONFIG="${SCRIPT_DIR}/kind-config.yaml"

source "${PROJECT_ROOT}/local-e2e-tests/common.sh"

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
    
    log_success "Cluster '${CLUSTER_NAME}' API server is ready"
}

prepare_nodes() {
    log_info "Preparing Kind nodes for OVN-Kubernetes..."
    for node in $(kind get nodes --name "${CLUSTER_NAME}"); do
        docker exec "$node" sysctl -w fs.inotify.max_user_instances=1024 >/dev/null
        docker exec "$node" sysctl -w fs.inotify.max_user_watches=1048576 >/dev/null
        docker exec "$node" sysctl -w fs.file-max=1048576 >/dev/null
        docker exec "$node" sysctl --ignore net.ipv6.conf.all.disable_ipv6=0 >/dev/null
        docker exec "$node" sysctl --ignore net.ipv6.conf.all.forwarding=1 >/dev/null
    done
    log_success "Kind nodes prepared"
}

configure_coredns() {
    local corefile dns_server new_corefile
    corefile=$(kubectl get configmap coredns -n kube-system \
        -o jsonpath='{.data.Corefile}' --context "kind-${CLUSTER_NAME}")
    dns_server=$(awk '$1 == "nameserver" && $2 !~ /^127\./ { print $2; exit }' /etc/resolv.conf)
    if [[ -z "${dns_server}" && -r /run/systemd/resolve/resolv.conf ]]; then
        dns_server=$(awk '$1 == "nameserver" && $2 !~ /^127\./ { print $2; exit }' \
            /run/systemd/resolve/resolv.conf)
    fi
    dns_server="${dns_server:-8.8.8.8}"

    new_corefile=$(sed \
        -e "s|forward \\. /etc/resolv.conf|forward . ${dns_server}|" \
        -e "s|forward \\. 8\\.8\\.8\\.8|forward . ${dns_server}|" <<<"${corefile}")
    if [[ "${new_corefile}" == "${corefile}" ]]; then
        log_info "CoreDNS bootstrap configuration is unchanged"
        return 0
    fi

    kubectl create configmap coredns -n kube-system \
        --from-literal="Corefile=${new_corefile}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "kind-${CLUSTER_NAME}" >/dev/null
    log_success "CoreDNS bootstrap configuration is ready"
}

display_info() {
    log_success "k2 cluster setup complete"
    echo
    echo "Cluster: ${CLUSTER_NAME}"
    echo "Context: kind-${CLUSTER_NAME}"
    echo
    kubectl get nodes --context "kind-${CLUSTER_NAME}"
}

main() {
    create_cluster
    kind_configure_registry "${CLUSTER_NAME}"
    prepare_nodes
    configure_coredns
    display_info
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main
