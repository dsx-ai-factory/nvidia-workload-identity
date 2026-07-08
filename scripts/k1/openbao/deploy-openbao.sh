#!/bin/bash
# deploy-openbao.sh - Deploys the external OpenBao test instance in k1
# Note: Helm repo setup is in scripts/setup-helm-repos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k1"
CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="openbao"
BAO_CHART_VERSION="0.16.2"
BAO_IMAGE="quay.io/openbao/openbao:2.4.0"
POD_IMAGE="nginx:1.27.4-alpine"
POD_NAME="e2e-nginx"
POD_NAMESPACE="dsx-identity-e2e"
BAO_ADDR="http://127.0.0.1:8200"
VAULT_ADDR="${BAO_ADDR}"
BAO_TOKEN=""
export BAO_ADDR VAULT_ADDR

source "${PROJECT_ROOT}/scripts/common.sh"

# Track background port-forward PID for cleanup on error/exit
PF_PID=""

cleanup_port_forward() {
    if [[ -n "$PF_PID" ]]; then
        kill "$PF_PID" 2>/dev/null || true
        wait "$PF_PID" 2>/dev/null || true
        PF_PID=""
    fi
}

bao_cli() {
    run_bao_cli "${BAO_IMAGE}" "$@"
}

start_port_forward() {
    cleanup_port_forward
    kubectl port-forward -n "${NAMESPACE}" svc/openbao 8200:8200 --context "${CONTEXT}" \
        >/dev/null 2>&1 &
    PF_PID=$!

    if curl -sS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 --connect-timeout 1 --max-time 5 \
        http://127.0.0.1:8200/v1/sys/health >/dev/null 2>&1; then
        return 0
    fi
    if ! kill -0 "${PF_PID}" 2>/dev/null; then
        log_error "OpenBao port-forward exited before becoming ready"
        return 1
    fi
    log_error "Timed out waiting for OpenBao through the port-forward"
    return 1
}

preload_images() {
    log_info "Extracting images from OpenBao Helm chart..."
    local template_output
    template_output=$(helm template openbao/openbao \
        --values "${SCRIPT_DIR}/openbao-values.yaml" \
        --version "${BAO_CHART_VERSION}" \
        --namespace "${NAMESPACE}" 2>/dev/null)
    preload_manifest_images "${CLUSTER_NAME}" <<<"${template_output}"
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${POD_IMAGE}" "${CLUSTER_NAME}"
    log_success "Images preloaded"
}

deploy_openbao() {
    log_info "Deploying OpenBao..."
    
    kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"
    
    helm upgrade --install openbao openbao/openbao \
        --namespace "${NAMESPACE}" \
        --values "${SCRIPT_DIR}/openbao-values.yaml" \
        --version "${BAO_CHART_VERSION}" \
        --kube-context "${CONTEXT}"

    kubectl apply -f "${SCRIPT_DIR}/e2e-nginx-pod.yaml" --context "${CONTEXT}"
    kubectl apply -f "${SCRIPT_DIR}/openbao-route.yaml" --context "${CONTEXT}"
    
    log_info "Waiting for OpenBao StatefulSet..."
    kubectl wait --for=jsonpath='{.status.readyReplicas}'=1 statefulset/openbao \
        -n "${NAMESPACE}" --timeout=120s --context "${CONTEXT}"
    kubectl wait --for=condition=Ready "pod/${POD_NAME}" \
        --namespace "${POD_NAMESPACE}" --timeout=180s --context "${CONTEXT}"
    
    log_success "OpenBao deployed"
}

initialize_openbao() {
    log_info "Initializing OpenBao..."

    start_port_forward

    local status init_output unseal_key root_token
    status=$(bao_cli status -format=json 2>/dev/null || true)
    if [[ "$(jq -r '.initialized' <<<"${status}")" != "true" ]]; then
        init_output=$(bao_cli operator init -key-shares=1 -key-threshold=1 -format=json)
        unseal_key=$(jq -er '.unseal_keys_b64[0]' <<<"${init_output}")
        root_token=$(jq -er '.root_token' <<<"${init_output}")
        kubectl create secret generic openbao-init-keys -n "${NAMESPACE}" \
            --from-literal="unseal_key=${unseal_key}" \
            --from-literal="root_token=${root_token}" \
            --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}" >/dev/null
        status=$(bao_cli status -format=json 2>/dev/null || true)
    elif ! kubectl get secret openbao-init-keys -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
        log_error "OpenBao is initialized but Secret openbao-init-keys is missing. Refusing to reinitialize"
        return 1
    fi

    if [[ "$(jq -r '.sealed' <<<"${status}")" == "true" ]]; then
        unseal_key=$(kubectl get secret openbao-init-keys -n "${NAMESPACE}" \
            -o jsonpath='{.data.unseal_key}' --context "${CONTEXT}" | base64 -d)
        bao_cli operator unseal "${unseal_key}" >/dev/null
    fi

    cleanup_port_forward
    log_success "OpenBao initialized and unsealed"
}

configure_dsx_jwt_auth() {
    log_info "Configuring DSX JWT CEL authentication..."

    BAO_TOKEN=$(kubectl get secret openbao-init-keys -n "${NAMESPACE}" \
        -o jsonpath='{.data.root_token}' --context "${CONTEXT}" | base64 -d)
    export BAO_ADDR BAO_TOKEN
    export VAULT_ADDR="${BAO_ADDR}" VAULT_TOKEN="${BAO_TOKEN}"
    start_port_forward

    if ! bao_cli auth list -format=json | jq -e 'has("jwt/")' >/dev/null; then
        bao_cli auth enable -path=jwt jwt >/dev/null
    fi

    bao_cli write auth/jwt/config \
        jwks_url="http://spiffe-exchange.dsx-identity.svc.cluster.local:8080/.well-known/jwks.json" \
        bound_issuer="spiffe://ds9.dsx.nvid.id" \
        jwt_supported_algs=ES256 >/dev/null

    for policy_file in "${SCRIPT_DIR}"/policies/*.hcl; do
        local policy_name
        policy_name=$(basename "$policy_file" .hcl | sed 's/-policy$//')
        bao_cli policy write "$policy_name" - < "${policy_file}" >/dev/null
    done

    bao_cli write auth/jwt/cel/role/basic-spiffe - < "${SCRIPT_DIR}/cel-role.json" >/dev/null

    if ! bao_cli secrets list -format=json | jq -e 'has("secret/")' >/dev/null; then
        bao_cli secrets enable -path=secret kv-v2 >/dev/null
    fi

    bao_cli kv put secret/region/ds9/e2e value=region-ds9 >/dev/null
    bao_cli kv put secret/service/dsx/e2e value=service-dsx >/dev/null
    bao_cli kv put secret/tenant/nca-0000000000000001/e2e value=tenant-1 >/dev/null
    bao_cli kv put secret/resource-type/dpu/e2e value=resource-dpu >/dev/null
    bao_cli kv put secret/resource-type/host/e2e value=resource-host >/dev/null
    bao_cli kv put secret/resource-type/vm/e2e value=resource-vm >/dev/null
    bao_cli kv put secret/forbidden/admin/e2e value=forbidden >/dev/null

    cleanup_port_forward
    log_success "DSX JWT CEL authentication configured"
}

wait_for_route() {
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
    echo "Initialization credentials are stored in Secret ${NAMESPACE}/openbao-init-keys"
}

cleanup() {
    log_info "Removing OpenBao..."
    kubectl delete namespace "${POD_NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found --wait=false >/dev/null
    helm uninstall openbao --namespace "${NAMESPACE}" --kube-context "${CONTEXT}" \
        --ignore-not-found
    kubectl delete namespace "${NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found --wait=true --timeout=180s >/dev/null
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
            deploy_openbao
            initialize_openbao
            configure_dsx_jwt_auth
            wait_for_route
            display_info
            ;;
    esac
}

trap 'cleanup_port_forward' EXIT
trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
