#!/bin/bash
# install-kyverno.sh - Installs Kyverno in a Kind cluster.
# Usage: install-kyverno.sh <cluster-name> [cleanup]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="${1:-}"
ACTION="${2:-install}"
KYVERNO_VERSION="${KYVERNO_VERSION:-3.6.1}"

if [[ -z "${CLUSTER_NAME}" || ("${ACTION}" != "install" && "${ACTION}" != "cleanup") ]]; then
    echo "Usage: $0 <cluster-name> [cleanup]" >&2
    exit 1
fi

CONTEXT="kind-${CLUSTER_NAME}"
source "${SCRIPT_DIR}/common.sh"

preload_images() {
    local rendered
    rendered=$(helm template kyverno/kyverno \
        --version "${KYVERNO_VERSION}" \
        --set admissionController.replicas=1 \
        --set backgroundController.replicas=1 \
        --set cleanupController.replicas=1 \
        --set reportsController.replicas=1 \
        --namespace kyverno)
    preload_manifest_images "${CLUSTER_NAME}" <<<"${rendered}"
}

install_kyverno() {
    preload_images
    helm upgrade --install kyverno kyverno/kyverno \
        --namespace kyverno \
        --create-namespace \
        --version "${KYVERNO_VERSION}" \
        --set admissionController.replicas=1 \
        --set backgroundController.replicas=1 \
        --set cleanupController.replicas=1 \
        --set reportsController.replicas=1 \
        --kube-context "${CONTEXT}" \
        --wait \
        --timeout 5m
    log_success "Kyverno ${KYVERNO_VERSION} installed in ${CLUSTER_NAME}"
}

cleanup() {
    helm uninstall kyverno --namespace kyverno --kube-context "${CONTEXT}" \
        --ignore-not-found
    kubectl delete namespace kyverno --context "${CONTEXT}" \
        --ignore-not-found --wait=true --timeout=180s >/dev/null
}

case "${ACTION}" in
    cleanup)
        cleanup
        ;;
    install)
        validate_context_exists "${CONTEXT}"
        install_kyverno
        ;;
esac
