#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CONTEXT="${CONTEXT:-kind-k2}"
NAMESPACE="${NAMESPACE:-dsx-identity-e2e}"
RELEASE="e2e-invalid-health-port"

source "${PROJECT_ROOT}/scripts/common.sh"

cleanup() {
    helm uninstall "${RELEASE}" --namespace "${NAMESPACE}" \
        --kube-context "${CONTEXT}" --ignore-not-found \
        --wait --timeout 30s >/dev/null
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for port in 0 -1 65536; do
    log_test "Checking admission rejects token-fetcher health port ${port}..."
    if response=$(helm install "${RELEASE}" "${SCRIPT_DIR}/token-fetcher-admission" \
        --namespace "${NAMESPACE}" --kube-context "${CONTEXT}" \
        --set-string "healthPort=${port}" --timeout 30s 2>&1); then
        log_error "Admission accepted invalid token-fetcher health port ${port}"
        exit 1
    fi
    if [[ "${response}" != *"inject-dsx-token-fetcher"* ||
          "${response}" != *"healthPort"* ||
          "${response}" != *"type conversion error"* ]]; then
        log_error "Admission did not reject health port ${port} through healthPort validation"
        echo "${response}" >&2
        exit 1
    fi
    cleanup
done

log_success "Admission rejected invalid token-fetcher health ports"
