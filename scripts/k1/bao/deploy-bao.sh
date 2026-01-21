#!/bin/bash
# deploy-bao.sh - Deploys OpenBao to k1 cluster
# Note: Helm repo setup is in scripts/setup-helm-repos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="bao"
BAO_CHART_VERSION="0.16.2"

source "${PROJECT_ROOT}/scripts/common.sh"

# Track background port-forward PID for cleanup on error/exit
PF_PID=""

cleanup_port_forward() {
    if [[ -n "$PF_PID" ]]; then
        kill "$PF_PID" 2>/dev/null || true
        PF_PID=""
    fi
}

preload_images() {
    log_info "Extracting images from OpenBao Helm chart..."
    
    # Template the chart to extract image references
    local template_output
    template_output=$(helm template openbao/openbao \
        --values "${SCRIPT_DIR}/bao-values.yaml" \
        --version "${BAO_CHART_VERSION}" \
        --namespace "${NAMESPACE}" 2>/dev/null)
    
    # Extract unique image references (image: field in YAML)
    local images
    images=$(echo "$template_output" | grep -E '^\s+image:\s+' | \
        awk '{print $2}' | tr -d '"' | sort -u)
    
    if [[ -z "$images" ]]; then
        log_info "No images found in Helm chart, skipping preload"
        return 0
    fi
    
    log_info "Preloading OpenBao images into cluster..."
    while IFS= read -r img; do
        [[ -z "$img" ]] && continue
        if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "$img" "${CLUSTER_NAME}"; then
            log_error "Failed to load image: $img"
            return 1
        fi
    done <<< "$images"
    
    log_success "Images preloaded"
}

deploy_bao() {
    log_info "Deploying OpenBao..."
    
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"
    
    helm upgrade --install openbao openbao/openbao \
        --namespace "${NAMESPACE}" \
        --values "${SCRIPT_DIR}/bao-values.yaml" \
        --version "${BAO_CHART_VERSION}" \
        --kube-context "${CONTEXT}"
    
    log_info "Waiting for OpenBao pod..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=openbao \
        -n "${NAMESPACE}" --timeout=120s --context "${CONTEXT}"
    
    log_success "OpenBao deployed"
}

initialize_bao() {
    log_info "Initializing OpenBao..."
    
    # Port forward in background (PF_PID tracked for cleanup on error)
    kubectl port-forward -n "${NAMESPACE}" svc/openbao 8200:8200 --context "${CONTEXT}" &
    PF_PID=$!
    sleep 3
    
    # Check if already initialized
    if curl -s http://127.0.0.1:8200/v1/sys/health | grep -q '"initialized":true'; then
        log_info "OpenBao already initialized"
        cleanup_port_forward
        return 0
    fi
    
    # Initialize
    local init_output
    init_output=$(curl -s -X PUT http://127.0.0.1:8200/v1/sys/init \
        -H "Content-Type: application/json" \
        -d '{"secret_shares": 1, "secret_threshold": 1}')
    
    local unseal_key root_token
    unseal_key=$(echo "$init_output" | jq -r '.keys[0]')
    root_token=$(echo "$init_output" | jq -r '.root_token')
    
    # Save keys
    kubectl create configmap bao-init-keys -n "${NAMESPACE}" \
        --from-literal=unseal_key="$unseal_key" \
        --from-literal=root_token="$root_token" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}"
    
    # Unseal
    curl -s -X PUT http://127.0.0.1:8200/v1/sys/unseal \
        -H "Content-Type: application/json" \
        -d "{\"key\": \"$unseal_key\"}" >/dev/null
    
    cleanup_port_forward
    
    log_success "OpenBao initialized and unsealed"
}

configure_spiffe_auth() {
    log_info "Configuring SPIFFE JWT authentication..."
    
    local root_token
    root_token=$(kubectl get configmap bao-init-keys -n "${NAMESPACE}" \
        -o jsonpath='{.data.root_token}' --context "${CONTEXT}")
    
    # Port forward in background (PF_PID tracked for cleanup on error)
    kubectl port-forward -n "${NAMESPACE}" svc/openbao 8200:8200 --context "${CONTEXT}" &
    PF_PID=$!
    sleep 3
    
    export VAULT_ADDR=http://127.0.0.1:8200
    export VAULT_TOKEN="$root_token"
    
    # Enable JWT auth
    bao auth list | grep -q "jwt/" || bao auth enable jwt >/dev/null
    
    # Configure JWT auth with exchange service
    local tenant_domain="tenant1.sjc-1.internal.nvidia.com"
    bao write auth/jwt/config \
        jwks_url="http://${tenant_domain}:8080/.well-known/jwks.json" \
        bound_issuer="spiffe://${tenant_domain}" >/dev/null
    
    # Create policies
    for policy_file in "${SCRIPT_DIR}"/policies/*.hcl; do
        local policy_name
        policy_name=$(basename "$policy_file" .hcl | sed 's/-policy$//')
        bao policy write "$policy_name" "$policy_file" >/dev/null
    done
    
    # Create CEL role
    local cel_role
    cel_role=$(jq --arg aud "spiffe://${tenant_domain}" \
        '.bound_audiences = [$aud]' "${SCRIPT_DIR}/cel-role.json")
    echo "$cel_role" | bao write auth/jwt/cel/role/basic-spiffe - >/dev/null
    
    # Enable KV secrets
    bao secrets list | grep -q "^secret/" || bao secrets enable -path=secret kv-v2 >/dev/null
    
    # Create test secret
    bao kv put secret/nginx username=admin password=secret123 >/dev/null
    
    cleanup_port_forward
    
    log_success "SPIFFE JWT authentication configured"
}

apply_route() {
    log_info "Applying OpenBao gateway route..."
    kubectl apply -f "${SCRIPT_DIR}/bao-route.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for HTTPRoute to be accepted..."
    kubectl wait --for=jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}'=True \
        httproute/openbao-route -n "${NAMESPACE}" --timeout=60s --context "${CONTEXT}"
    
    log_success "OpenBao route accepted"
}

display_info() {
    log_success "OpenBao deployment complete"
    echo
    echo "Pod:"
    kubectl get pod -l app.kubernetes.io/name=openbao -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Root token:"
    kubectl get configmap bao-init-keys -n "${NAMESPACE}" \
        -o jsonpath='{.data.root_token}' --context "${CONTEXT}" 2>/dev/null || echo "Not available"
    echo
}

cleanup() {
    log_info "Removing OpenBao..."
    helm uninstall openbao --namespace "${NAMESPACE}" --kube-context "${CONTEXT}" || true
    kubectl delete namespace "${NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true
    log_success "OpenBao removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            deploy_bao
            initialize_bao
            configure_spiffe_auth
            apply_route
            display_info
            ;;
    esac
}

trap 'cleanup_port_forward' EXIT
trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
