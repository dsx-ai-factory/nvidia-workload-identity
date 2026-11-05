#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../../common.sh"

# The upstream DaemonSet has no readiness probe. Containerd must select
# Multus before KubeVirt creates guest NICs on the tenant network.
main() {
    local nodes node remaining host_timeout node_timeout
    local deadline=$((SECONDS + 600))

    nodes=$(timeout --kill-after=1s 15s kind get nodes --name "${K2_CLUSTER}")
    if [[ -z "${nodes}" ]]; then
        log_error "No Kind nodes found for ${K2_CLUSTER}"
        return 1
    fi

    remaining=$((deadline - SECONDS))
    timeout --kill-after=1s "${remaining}s" \
        kubectl rollout status daemonset/kube-multus-ds --namespace kube-system \
            --context "${K2_CONTEXT}" --timeout="${remaining}s"
    for node in ${nodes}; do
        log_info "Waiting for Multus to be the default CNI on ${node}"
        while true; do
            remaining=$((deadline - SECONDS))
            if ((remaining <= 0)); then
                log_error "Multus is not the default CNI on ${node}"
                return 1
            fi
            host_timeout=$((remaining < 15 ? remaining : 15))
            node_timeout=$((host_timeout < 10 ? host_timeout : 10))
            # The command inside the node survives a Docker client disconnect.
            # Bound both processes so a stalled daemon cannot leave a probe running.
            if timeout --kill-after=1s "${host_timeout}s" docker exec "${node}" \
                timeout --kill-after=1s "${node_timeout}s" crictl --timeout="${node_timeout}s" info | jq -e '
                  any(.cniconfig.Networks[];
                    .IFName == "eth0" and .Config.Plugins[0].Network.type == "multus")
                ' >/dev/null; then
                break
            fi
            if ((SECONDS < deadline)); then
                sleep 1
            fi
        done
    done
    log_success "Multus is the default CNI on every k2 node"
}

main "$@"
