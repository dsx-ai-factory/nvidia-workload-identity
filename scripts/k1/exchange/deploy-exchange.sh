#!/bin/bash
# deploy-exchange.sh - Deploys SPIFFE exchange service to k1 cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="spiffe-exchange"
IMAGE_NAME="spiffe-exchange-service"
IMAGE_TAG="latest"

source "${PROJECT_ROOT}/scripts/common.sh"

build_image() {
    log_info "Building SPIFFE exchange service image..."
    
    docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" \
        -f "${PROJECT_ROOT}/Dockerfile.spiffe-exchange-service" \
        "${PROJECT_ROOT}"
    
    log_success "Image built"
}

load_image() {
    log_info "Loading image into Kind cluster..."
    
    kind load docker-image "${IMAGE_NAME}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
    
    log_success "Image loaded"
}

ensure_namespace() {
    kubectl create namespace "${NAMESPACE}" --context "${CONTEXT}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"
}

copy_postgres_secret() {
    log_info "Copying PostgreSQL credentials to exchange namespace..."
    
    ensure_namespace
    
    # Copy CloudNativePG app credentials secret from postgres-system to spiffe-exchange namespace
    kubectl get secret postgres-app -n postgres-system --context "${CONTEXT}" -o json | \
        jq 'del(.metadata.namespace, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.ownerReferences)' | \
        kubectl apply -n "${NAMESPACE}" --context "${CONTEXT}" -f -
    
    log_success "PostgreSQL credentials copied"
}

create_encryption_key_secret() {
    log_info "Ensuring encryption key secret exists..."
    
    ensure_namespace
    
    # Check if secret already exists
    if kubectl get secret exchange-encryption-key -n "${NAMESPACE}" --context "${CONTEXT}" &>/dev/null; then
        log_info "Encryption key secret already exists"
        return 0
    fi
    
    # Generate a random 32-byte key and base64 encode it
    local key_b64
    key_b64=$(openssl rand -base64 32)
    
    kubectl create secret generic exchange-encryption-key \
        -n "${NAMESPACE}" \
        --context "${CONTEXT}" \
        --from-literal=EXCHANGE_KEY_ENCRYPTION_KEY_B64="${key_b64}"
    
    log_success "Encryption key secret created"
}

deploy_exchange() {
    log_info "Deploying SPIFFE exchange service..."
    
    # Ensure secrets exist before deploying
    copy_postgres_secret
    create_encryption_key_secret
    
    kubectl apply -f "${SCRIPT_DIR}/exchange-service.yaml" \
        --context "${CONTEXT}"
    
    log_info "Waiting for exchange service..."
    kubectl rollout status deployment/spiffe-exchange-service \
        -n "${NAMESPACE}" --timeout=120s --context "${CONTEXT}"
    
    log_success "Exchange service deployed"
}

configure_tenant_dns() {
    log_info "Configuring CoreDNS wildcard rewrite for tenant domains..."
    
    local region="sjc-1"
    local base_domain="nvidia.com"
    local service_fqdn="spiffe-exchange-service.${NAMESPACE}.svc.cluster.local"
    
    # Products that need DNS entries (wildcard on tenant)
    local services=("internal" "vmaas")
    
    local corefile
    corefile=$(kubectl get configmap coredns -n kube-system -o jsonpath='{.data.Corefile}' --context "${CONTEXT}")
    
    local changes_needed=false
    local rules=""
    
    for service in "${services[@]}"; do
        local service_domain="${region}.${service}.${base_domain}"
        local escaped_domain="${service_domain//./\\.}"
        local rewrite_rule="rewrite name regex .*\\.${escaped_domain} ${service_fqdn}"
        
        if ! echo "$corefile" | grep -qF "rewrite name regex .*\\.${escaped_domain}"; then
            log_info "Adding wildcard DNS rewrite: *.${service_domain} -> ${service_fqdn}"
            rules="${rules}    ${rewrite_rule}\n"
            changes_needed=true
        else
            log_info "Wildcard DNS rewrite already configured for *.${service_domain}"
        fi
    done
    
    if [[ "$changes_needed" != "true" ]]; then
        log_info "All service DNS rewrites already configured"
        return 0
    fi
    
    # Insert rewrite rules before "kubernetes cluster.local"
    local new_corefile
    new_corefile=$(echo "$corefile" | awk -v rules="$rules" '/kubernetes cluster.local/{printf "%s", rules}1')
    
    kubectl create configmap coredns -n kube-system \
        --from-literal=Corefile="$new_corefile" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}"
    
    log_info "Reloading CoreDNS..."
    kubectl rollout restart deployment coredns -n kube-system --context "${CONTEXT}"
    kubectl rollout status deployment coredns -n kube-system --timeout=60s --context "${CONTEXT}"
    
    log_success "CoreDNS configured for service domains"
}

apply_route() {
    log_info "Applying exchange service gateway route..."
    kubectl apply -f "${SCRIPT_DIR}/exchange-route.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for HTTPRoute to be accepted..."
    kubectl wait --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True \
        httproute/exchange-route -n "${NAMESPACE}" --timeout=60s --context "${CONTEXT}"
    
    log_success "Exchange route accepted"
}

display_info() {
    log_success "Exchange service deployment complete"
    echo
    echo "Deployment:"
    kubectl get deployment spiffe-exchange-service -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Service:"
    kubectl get service spiffe-exchange-service -n "${NAMESPACE}" --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing exchange service..."
    kubectl delete -f "${SCRIPT_DIR}/exchange-service.yaml" \
        --context "${CONTEXT}" --ignore-not-found=true
    log_success "Exchange service removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            build_image
            load_image
            deploy_exchange
            configure_tenant_dns
            apply_route
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
