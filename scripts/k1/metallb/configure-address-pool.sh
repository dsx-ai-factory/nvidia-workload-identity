#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CONTEXT=kind-k1

source "${PROJECT_ROOT}/scripts/common.sh"

main() {
    local manifest subnet_prefix

    validate_context_exists "${CONTEXT}"
    subnet_prefix=$(docker network inspect kind \
        -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' | \
        tr ' ' '\n' | awk -F. '!/:/ {print $1 "." $2; exit}')
    [[ -n "${subnet_prefix}" ]] || {
        log_error "Docker network kind has no IPv4 subnet"
        return 1
    }

    kubectl wait --for=condition=Established --context "${CONTEXT}" --timeout=120s \
        crd/ipaddresspools.metallb.io crd/l2advertisements.metallb.io >/dev/null
    manifest=$(<"${SCRIPT_DIR}/address-pool.yaml.tmpl")
    manifest=${manifest//SUBNET_PREFIX/${subnet_prefix}}
    kubectl apply -f - --context "${CONTEXT}" <<<"${manifest}" >/dev/null
    log_success "MetalLB address pool is ready"
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main
