#!/bin/bash
# install-cert-manager.sh - Installs cert-manager for a Kind cluster using Helm
# Usage: install-cert-manager.sh <cluster-name>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CERT_MANAGER_VERSION="v1.19.2"

CLUSTER_NAME="${1:-}"

if [[ -z "$CLUSTER_NAME" ]]; then
    echo "Usage: $0 <cluster-name>" >&2
    exit 1
fi

CONTEXT="kind-${CLUSTER_NAME}"

source "${PROJECT_ROOT}/scripts/common.sh"

CHART_CACHE="/tmp/cert-manager-${CERT_MANAGER_VERSION}.tgz"
MANIFEST_CACHE="/tmp/cert-manager-${CERT_MANAGER_VERSION}-manifest.yaml"

get_chart() {
    if [[ ! -f "$CHART_CACHE" ]]; then
        log_info "Caching cert-manager Helm chart..." >&2
        helm pull oci://quay.io/jetstack/charts/cert-manager \
            --version "${CERT_MANAGER_VERSION}" \
            --destination "$(dirname "$CHART_CACHE")" >/dev/null 2>&1
        if [[ ! -f "$CHART_CACHE" ]]; then
            log_error "Failed to download Helm chart" >&2
            return 1
        fi
    fi
    echo "$CHART_CACHE"
}

get_manifest() {
    if [[ ! -f "$MANIFEST_CACHE" ]]; then
        log_info "Caching cert-manager manifest..." >&2
        local chart_path
        chart_path=$(get_chart)
        helm template cert-manager "$chart_path" \
            --namespace cert-manager \
            --set crds.enabled=true > "$MANIFEST_CACHE" 2>/dev/null
    fi
    cat "$MANIFEST_CACHE"
}

preload_images() {
    log_info "Preloading cert-manager images into cluster..."
    
    local manifest_output
    manifest_output=$(get_manifest)
    
    # Extract unique image references
    local images
    images=$(echo "$manifest_output" | grep -E '^\s+image:\s+' | \
        awk '{print $2}' | tr -d '"' | sort -u)
    
    if [[ -z "$images" ]]; then
        log_info "No images found in Helm chart, skipping preload"
        return 0
    fi
    
    while IFS= read -r img; do
        [[ -z "$img" ]] && continue
        if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "$img" "${CLUSTER_NAME}"; then
            log_error "Failed to load image: $img"
            return 1
        fi
    done <<< "$images"
    
    log_success "Images preloaded"
}

install_cert_manager() {
    log_info "Installing cert-manager ${CERT_MANAGER_VERSION}..."
    
    # Check if already installed
    if kubectl get deployment cert-manager -n cert-manager --context "${CONTEXT}" &>/dev/null; then
        log_info "cert-manager already installed"
        return 0
    fi
    
    local chart_path
    chart_path=$(get_chart)
    
    helm upgrade --install cert-manager "$chart_path" \
        --namespace cert-manager \
        --create-namespace \
        --set crds.enabled=true \
        --kube-context "${CONTEXT}" \
        --wait \
        --timeout 5m
    
    log_success "cert-manager installed"
}

create_ca_issuer() {
    log_info "Creating CA issuer chain..."
    
    # Check if already exists
    if kubectl get clusterissuer ca-issuer --context "${CONTEXT}" &>/dev/null; then
        log_info "CA issuer already exists"
        return 0
    fi
    
    # Create self-signed issuer (to bootstrap the CA)
    kubectl apply --context "${CONTEXT}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned-issuer
spec:
  selfSigned: {}
---
# CA Certificate (signed by self-signed issuer)
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${CLUSTER_NAME}-ca
  namespace: cert-manager
spec:
  isCA: true
  commonName: "${CLUSTER_NAME}-ca"
  secretName: ${CLUSTER_NAME}-ca-secret
  duration: 87600h # 10 years
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: selfsigned-issuer
    kind: ClusterIssuer
---
# ClusterIssuer using the CA
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ca-issuer
spec:
  ca:
    secretName: ${CLUSTER_NAME}-ca-secret
EOF
    
    # Wait for CA certificate to be ready
    log_info "Waiting for CA certificate..."
    kubectl wait --for=condition=Ready certificate/${CLUSTER_NAME}-ca \
        -n cert-manager --timeout=60s --context "${CONTEXT}"
    
    log_success "CA issuer created"
}

main() {
    validate_context_exists "${CONTEXT}"
    preload_images
    install_cert_manager
    create_ca_issuer
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
