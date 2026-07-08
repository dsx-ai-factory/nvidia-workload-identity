#!/bin/bash
# deploy-identity.sh - Deploy the DSX identity umbrella
#
# Usage:
#   ./scripts/deploy-identity.sh
#   ./scripts/deploy-identity.sh -c kind-k1 -f scripts/k1-values.yaml
#   ./scripts/deploy-identity.sh cleanup
#   ./scripts/deploy-identity.sh cleanup -c kind-k2

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CHART_PATH="${PROJECT_ROOT}/charts/dsx-identity"
HELM_RELEASE="dsx-identity"
IDENTITY_NAMESPACE="dsx-identity"
POD_FIXTURE="${PROJECT_ROOT}/scripts/k2/nginx/e2e-nginx.yaml"
POD_NAME="e2e-nginx"
POD_IMAGE="nginx:1.27.4-alpine"
POD_NAMESPACE="dsx-identity-e2e"

# No explicit cluster arguments selects the k1 smoke path.
CONTEXT="kind-k1"
VALUES_FILE="${SCRIPT_DIR}/local-values.yaml"
SMOKE_MODE=true
HELM_ARGS=()

source "${PROJECT_ROOT}/scripts/common.sh"

check_prerequisites() {
    local cmd commands=(go helm jq kubectl yq)
    if [[ "${SMOKE_MODE}" == "true" ]]; then
        commands+=(docker make openssl)
    fi
    for cmd in "${commands[@]}"; do
        if ! command -v "${cmd}" >/dev/null 2>&1; then
            log_error "${cmd} is not installed"
            return 1
        fi
    done
}

parse_args() {
    local cmd="${1:-}"
    if [[ "$cmd" == "cleanup" ]]; then
        shift
    fi

    while [[ $# -gt 0 ]]; do
        case "$1" in
            -c|--context|-f|--values|-n|--namespace|--set)
                if [[ $# -lt 2 ]]; then
                    log_error "Missing value for $1"
                    return 1
                fi
                case "$1" in
                    -c|--context) CONTEXT="$2"; SMOKE_MODE=false ;;
                    -f|--values) VALUES_FILE="$2" ;;
                    -n|--namespace) IDENTITY_NAMESPACE="$2" ;;
                    --set) HELM_ARGS+=(--set "$2") ;;
                esac
                shift 2
                ;;
            *)
                log_error "Unknown argument: $1"
                return 1
                ;;
        esac
    done
}

build_chart_deps() {
    log_info "Building chart dependencies..."
    helm dependency build --skip-refresh "${PROJECT_ROOT}/third-party/dsx-exchange/deploy/nats-event-bus"
    helm dependency build --skip-refresh "${CHART_PATH}"
}

build_and_load_images() {
    local cluster_name="${CONTEXT#kind-}"

    log_info "Building Docker images..."
    docker build -t spiffe-exchange-service:0.1.0 \
        -f "${PROJECT_ROOT}/Dockerfile.spiffe-exchange-service" "${PROJECT_ROOT}"
    docker build -t dsx-token-fetcher:0.1.0 \
        -f "${PROJECT_ROOT}/Dockerfile.dsx-token-fetcher" "${PROJECT_ROOT}"

    log_info "Loading images into Kind cluster ${cluster_name}..."
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" spiffe-exchange-service:0.1.0 "${cluster_name}"
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" dsx-token-fetcher:0.1.0 "${cluster_name}"
    "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${POD_IMAGE}" "${cluster_name}"

    log_success "Images built and loaded"
}

prepare_event_bus() {
    local expose_gateway=true
    if [[ "${SMOKE_MODE}" == "true" ]]; then
        expose_gateway=false
    fi
    log_info "Preparing the DSX Event Bus and OAuth provider..."
    make -s -C "${PROJECT_ROOT}" event-bus-tools
    PATH="${PROJECT_ROOT}/bin:${PATH}" CONTEXT="${CONTEXT}" EXPOSE_GATEWAY="${expose_gateway}" \
        "${PROJECT_ROOT}/scripts/k1/event-bus/prepare-event-bus.sh"
}

create_signing_keyset() {
    local secret_name generated jwks key_id
    secret_name=$(yq -r '.exchange.signingKeysetSecret // "spiffe-exchange-signing-keyset"' \
        "${VALUES_FILE}")

    kubectl create namespace "${IDENTITY_NAMESPACE}" --context "${CONTEXT}" --dry-run=client -o yaml | \
        kubectl apply -f - --context "${CONTEXT}"

    if kubectl get secret "${secret_name}" -n "${IDENTITY_NAMESPACE}" --context "${CONTEXT}" &>/dev/null; then
        log_info "Exchange signing keyset Secret already exists in ${CONTEXT}"
        return 0
    fi

    log_info "Creating static Exchange signing keyset Secret in ${CONTEXT}..."
    generated=$(cd "${PROJECT_ROOT}" && go run ./cmd/generate-signing-keyset)
    jwks=$(jq -c '.jwks' <<<"${generated}")
    key_id=$(jq -r '.active_kid' <<<"${generated}")
    kubectl create secret generic "${secret_name}" \
        -n "${IDENTITY_NAMESPACE}" \
        --context "${CONTEXT}" \
        --from-literal="signing-jwks.json=${jwks}" \
        --from-literal="active-kid=${key_id}" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}"

    log_success "Exchange signing keyset Secret created in ${CONTEXT}"
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

deploy_identity() {
    log_info "Deploying DSX identity (values: $(basename "${VALUES_FILE}"))..."

    helm upgrade --install "${HELM_RELEASE}" "${CHART_PATH}" \
        --namespace "${IDENTITY_NAMESPACE}" \
        --create-namespace \
        --values "${VALUES_FILE}" \
        --kube-context "${CONTEXT}" \
        --timeout 15m \
        "${HELM_ARGS[@]}"

    log_success "DSX identity release submitted"
}

deploy_smoke_pod() {
    kubectl apply -f "${POD_FIXTURE}" --context "${CONTEXT}"
}

wait_for_identity() {
    local kind name namespace resource
    # The release owns controllers in dsx-identity and exchange-proxy-system.
    while IFS=$'\t' read -r kind namespace name; do
        resource="${kind,,}/${name}"
        kubectl rollout status "${resource}" -n "${namespace}" \
            --context "${CONTEXT}" --timeout=15m
    done < <(helm get manifest "${HELM_RELEASE}" -n "${IDENTITY_NAMESPACE}" --kube-context "${CONTEXT}" | \
        IDENTITY_NAMESPACE="${IDENTITY_NAMESPACE}" yq eval-all -o=json -I=0 \
            'select(.kind == "Deployment" or .kind == "StatefulSet" or .kind == "DaemonSet") |
             {"kind": .kind, "namespace": (.metadata.namespace // strenv(IDENTITY_NAMESPACE)), "name": .metadata.name}' - | \
        jq -r '[.kind, .namespace, .name] | @tsv')

    if [[ "$SMOKE_MODE" == "true" ]]; then
        kubectl wait --for=condition=Ready "pod/${POD_NAME}" -n "${POD_NAMESPACE}" \
            --context "${CONTEXT}" --timeout=180s
    fi
    log_success "DSX identity controllers and fixtures are ready"
}

delete_exchange_workload() {
    log_info "Deleting Exchange while the SPIRE CSI driver is still running..."
    kubectl delete deployment spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
        --context "${CONTEXT}" --cascade=foreground --ignore-not-found \
        --wait=true --timeout=120s
}

display_info() {
    echo
    log_success "DSX identity deployed (context: ${CONTEXT})"
    echo
    echo "Helm releases:"
    helm list -n "${IDENTITY_NAMESPACE}" --kube-context "${CONTEXT}"
    echo
    kubectl get pods -n "${IDENTITY_NAMESPACE}" --context "${CONTEXT}"
}

cleanup() {
    log_info "Removing DSX identity (context: ${CONTEXT})..."
    local identity_releases crd_releases
    if [[ "$SMOKE_MODE" == "true" ]]; then
        kubectl delete namespace "${POD_NAMESPACE}" --context "${CONTEXT}" \
            --ignore-not-found --wait=true --timeout=60s >/dev/null
    fi
    identity_releases=$(helm list --all --short -n "${IDENTITY_NAMESPACE}" \
        --kube-context "${CONTEXT}") || return 1
    if grep -Fxq "${HELM_RELEASE}" <<<"${identity_releases}"; then
        delete_exchange_workload
        helm uninstall "${HELM_RELEASE}" -n "${IDENTITY_NAMESPACE}" --kube-context "${CONTEXT}"
        log_info "Deleting SPIRE server data..."
        kubectl delete pvc -n "${IDENTITY_NAMESPACE}" \
            --context "${CONTEXT}" \
            --selector="app.kubernetes.io/name=server,app.kubernetes.io/instance=${HELM_RELEASE},app.kubernetes.io/component=server" \
            --ignore-not-found --wait=true --timeout=120s
    fi

    crd_releases=$(helm list --all --short -n spire-system \
        --kube-context "${CONTEXT}") || return 1
    if grep -Fxq spire-crds <<<"${crd_releases}"; then
        helm uninstall spire-crds -n spire-system --kube-context "${CONTEXT}"
    fi

    if [[ "$SMOKE_MODE" == "true" ]]; then
        "${PROJECT_ROOT}/scripts/install-kyverno.sh" "${CONTEXT#kind-}" cleanup
    fi

    log_success "DSX identity removed"
}

main() {
    local cmd="${1:-deploy}"
    parse_args "$@"

    case "$cmd" in
        cleanup)
            cleanup
            ;;
        *)
            check_prerequisites
            validate_context_exists "${CONTEXT}"

            if [[ "$SMOKE_MODE" == "true" ]]; then
                "${PROJECT_ROOT}/scripts/setup-helm-repos.sh"
                build_chart_deps
                build_and_load_images
                "${PROJECT_ROOT}/scripts/install-kyverno.sh" "${CONTEXT#kind-}"
                prepare_event_bus
            fi

            create_signing_keyset
            install_spire_crds
            deploy_identity
            if [[ "$SMOKE_MODE" == "true" ]]; then
                deploy_smoke_pod
            fi
            wait_for_identity
            display_info
            ;;
    esac
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
