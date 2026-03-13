#!/bin/bash
# deploy-spiffe.sh - Deploy SPIFFE stack via umbrella chart
#
# Usage:
#   ./scripts/deploy-spiffe.sh                                    # Standalone: full setup (Kind + CNPG + Kyverno + chart)
#   ./scripts/deploy-spiffe.sh -c kind-k1 -f scripts/k1-values.yaml  # Multi-cluster: chart only (infra from Makefile)
#   ./scripts/deploy-spiffe.sh cleanup                            # Remove standalone stack
#   ./scripts/deploy-spiffe.sh cleanup -c kind-k2                 # Remove chart from specific cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CHART_PATH="${PROJECT_ROOT}/charts/spiffe"
SPIFFE_NAMESPACE="dsx-spiffe"

# Defaults (standalone mode)
CONTEXT="kind-k1"
VALUES_FILE="${SCRIPT_DIR}/local-values.yaml"
STANDALONE=true

source "${PROJECT_ROOT}/scripts/common.sh"

parse_args() {
    local cmd="${1:-}"
    if [[ "$cmd" == "cleanup" ]]; then
        shift
    fi

    while [[ $# -gt 0 ]]; do
        case "$1" in
            -c|--context) CONTEXT="$2"; STANDALONE=false; shift 2 ;;
            -f|--values)  VALUES_FILE="$2"; shift 2 ;;
            -n|--namespace) SPIFFE_NAMESPACE="$2"; shift 2 ;;
            *) shift ;;
        esac
    done
}

build_chart_deps() {
    if [ -d "${PROJECT_ROOT}/charts/spiffe-spire/charts" ] && \
       [ -d "${PROJECT_ROOT}/charts/spiffe/charts" ]; then
        return 0
    fi
    log_info "Building chart dependencies..."
    cd "${PROJECT_ROOT}/charts/spiffe-spire" && helm dep build
    cd "${PROJECT_ROOT}/charts/spiffe" && helm dep build
    cd "${PROJECT_ROOT}"
}

build_and_load_images() {
    local cluster_name="${CONTEXT#kind-}"

    log_info "Building Docker images..."
    docker build -t spiffe-exchange-service:latest \
        -f "${PROJECT_ROOT}/Dockerfile.spiffe-exchange-service" "${PROJECT_ROOT}"
    docker build -t spiffe-token-fetcher:latest \
        -f "${PROJECT_ROOT}/Dockerfile.spiffe-token-fetcher" "${PROJECT_ROOT}"

    log_info "Loading images into Kind cluster ${cluster_name}..."
    kind load docker-image spiffe-exchange-service:latest --name "${cluster_name}"
    kind load docker-image spiffe-token-fetcher:latest --name "${cluster_name}"

    log_success "Images built and loaded"
}

install_kyverno() {
    if helm status kyverno -n kyverno --kube-context "${CONTEXT}" &>/dev/null; then
        log_info "Kyverno already installed"
        return 0
    fi

    log_info "Installing Kyverno..."
    helm repo add kyverno https://kyverno.github.io/kyverno/ 2>/dev/null || true
    helm upgrade --install kyverno kyverno/kyverno \
        --namespace kyverno --create-namespace \
        --set admissionController.replicas=1 \
        --set backgroundController.replicas=1 \
        --set cleanupController.replicas=1 \
        --set reportsController.replicas=1 \
        --kube-context "${CONTEXT}" \
        --wait --timeout 5m

    log_success "Kyverno installed"
}

deploy_cnpg() {
    log_info "Deploying shared CNPG cluster..."
    "${PROJECT_ROOT}/scripts/k1/postgres/deploy-postgres.sh"
}

copy_pg_secret() {
    if ! kubectl get secret shared-pg-cluster-app -n cnpg-shared --context "${CONTEXT}" &>/dev/null; then
        log_info "No CNPG secret to copy (exchange may not be enabled)"
        return 0
    fi

    log_info "Copying PostgreSQL credentials to ${SPIFFE_NAMESPACE}..."

    kubectl create namespace "${SPIFFE_NAMESPACE}" --context "${CONTEXT}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"

    kubectl get secret shared-pg-cluster-app -n cnpg-shared --context "${CONTEXT}" -o json | \
        python3 -c "
import sys, json
s = json.load(sys.stdin)
for k in ['namespace','resourceVersion','uid','creationTimestamp','ownerReferences']:
    s['metadata'].pop(k, None)
json.dump(s, sys.stdout)
" | kubectl apply -n "${SPIFFE_NAMESPACE}" --context "${CONTEXT}" -f -

    log_success "PostgreSQL credentials copied"
}

create_encryption_keyset() {
    kubectl create namespace "${SPIFFE_NAMESPACE}" --context "${CONTEXT}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"

    if kubectl get secret exchange-encryption-keyset -n "${SPIFFE_NAMESPACE}" --context "${CONTEXT}" &>/dev/null; then
        log_info "Encryption keyset secret already exists"
        return 0
    fi

    log_info "Creating encryption keyset secret..."
    local key_id="mk-$(date +%Y-%m)"
    local key_bytes
    key_bytes=$(openssl rand 32)
    local key_b64url
    key_b64url=$(echo -n "$key_bytes" | base64 | tr '+/' '-_' | tr -d '=')

    local jwks
    jwks=$(cat <<EOF
{"keys":[{"kty":"oct","kid":"${key_id}","k":"${key_b64url}","alg":"A256GCM","use":"enc"}]}
EOF
)

    kubectl create secret generic exchange-encryption-keyset \
        -n "${SPIFFE_NAMESPACE}" \
        --context "${CONTEXT}" \
        --from-literal=EXCHANGE_ENCRYPTION_KEYSET="${jwks}" \
        --from-literal=EXCHANGE_ACTIVE_KEY_ID="${key_id}"

    log_success "Encryption keyset secret created"
}

install_spire_crds() {
    if helm status spire-crds -n spire-system --kube-context "${CONTEXT}" &>/dev/null; then
        log_info "SPIRE CRDs already installed"
        return 0
    fi

    log_info "Installing SPIRE CRDs..."
    helm upgrade --install spire-crds spiffe/spire-crds \
        --namespace spire-system --create-namespace \
        --version 0.5.0 \
        --kube-context "${CONTEXT}" \
        --wait --timeout 2m

    log_success "SPIRE CRDs installed"
}

deploy_spiffe() {
    log_info "Deploying SPIFFE umbrella chart (values: $(basename "${VALUES_FILE}"))..."

    helm upgrade --install spiffe "${CHART_PATH}" \
        --namespace "${SPIFFE_NAMESPACE}" \
        --create-namespace \
        --values "${VALUES_FILE}" \
        --kube-context "${CONTEXT}" \
        --timeout 15m \
        --wait

    log_success "SPIFFE umbrella chart deployed"
}

display_info() {
    echo
    log_success "SPIFFE stack deployed (context: ${CONTEXT})"
    echo
    echo "Helm releases:"
    helm list -n "${SPIFFE_NAMESPACE}" --kube-context "${CONTEXT}"
    echo
    kubectl get pods -n "${SPIFFE_NAMESPACE}" --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing SPIFFE stack (context: ${CONTEXT})..."
    helm uninstall spiffe -n "${SPIFFE_NAMESPACE}" --kube-context "${CONTEXT}" 2>/dev/null || true
    helm uninstall spire-crds -n spire-system --kube-context "${CONTEXT}" 2>/dev/null || true

    if [[ "$STANDALONE" == "true" ]]; then
        "${PROJECT_ROOT}/scripts/k1/postgres/deploy-postgres.sh" cleanup
        helm uninstall kyverno -n kyverno --kube-context "${CONTEXT}" 2>/dev/null || true
        kubectl delete namespace kyverno --context "${CONTEXT}" --ignore-not-found=true
    fi

    kubectl delete namespace "${SPIFFE_NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true
    log_success "SPIFFE stack removed"
}

main() {
    local cmd="${1:-deploy}"
    parse_args "$@"

    case "$cmd" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            build_chart_deps

            if [[ "$STANDALONE" == "true" ]]; then
                build_and_load_images
                install_kyverno
                deploy_cnpg
            fi

            copy_pg_secret
            create_encryption_keyset
            install_spire_crds
            deploy_spiffe
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
