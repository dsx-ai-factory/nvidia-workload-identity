#!/bin/bash

# common.sh
# Shared functions and variables for SPIRE multi-cluster scripts

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
NC='\033[0m' # No Color

# Cluster names
K1_CLUSTER="k1"
K2_CLUSTER="k2"
K1_CONTEXT="kind-${K1_CLUSTER}"
K2_CONTEXT="kind-${K2_CLUSTER}"

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_test() {
    echo -e "${PURPLE}[TEST]${NC} $1"
}

run_timed() {
    local name="$1" started=${SECONDS} result
    shift

    if "$@"; then
        result=0
    else
        result=$?
    fi
    log_info "Timing: ${name}: $((SECONDS - started))s"
    return "${result}"
}

log_timing() {
    local name="$1" started="$2"
    log_info "Timing: ${name}: $((SECONDS - started))s"
}

# Context validation helpers
# NOTE: Do NOT use kubectl config use-context in scripts!
# With parallel execution (make -j), scripts share kubeconfig and race.
# Always pass --context to every kubectl/helm command instead.

validate_context_exists() {
    local context="$1"
    if ! kubectl config get-contexts "$context" &>/dev/null; then
        log_error "Context '$context' does not exist. Is the cluster created?"
        return 1
    fi
}

get_gateway_address() {
    local context="$1"
    local namespace="$2"
    local name="$3"

    kubectl wait --for=condition=Programmed "gateway/${name}" \
        --namespace "${namespace}" --context "${context}" --timeout=120s >/dev/null
    kubectl get "gateway/${name}" --namespace "${namespace}" \
        --context "${context}" -o json | jq -er '.status.addresses[0].value'
}

# Kind publishes its API on the local host by default. When Docker is a remote
# CI service, publish on all interfaces, trust the service DNS name, and point
# the generated kubeconfig at that name instead.
kind_create_cluster() {
    local cluster_name="$1"
    local config="$2"

    if [[ -z "${KIND_DIND_SERVICE_HOST:-}" ]]; then
        kind create cluster --config="${config}" --name="${cluster_name}"
    else
        local kubeconfig
        kubeconfig=$(kind_kubeconfig_path "${cluster_name}")
        mkdir -p "$(dirname "${kubeconfig}")"
        kind create cluster --name="${cluster_name}" --kubeconfig="${kubeconfig}" --config=<(yq eval '
            .networking.apiServerAddress = "0.0.0.0" |
            (.nodes[] | select(.role == "control-plane").kubeadmConfigPatches) += [
              "kind: ClusterConfiguration\napiServer:\n  certSANs:\n    - " + strenv(KIND_DIND_SERVICE_HOST) + "\n"
            ]
        ' "${config}")
    fi
}

kind_kubeconfig_path() {
    local cluster_name="$1"
    printf '%s/.kube/kind-%s' "${PROJECT_ROOT}" "${cluster_name}"
}

kind_configure_registry() {
    local cluster_name="$1"
    local node registry_dir=/etc/containerd/certs.d/_default
    while IFS= read -r node; do
        docker exec "${node}" mkdir -p "${registry_dir}"
        docker cp "${PROJECT_ROOT}/scripts/local-registry/_default/hosts.toml" \
            "${node}:${registry_dir}/hosts.toml"
    done < <(kind get nodes --name "${cluster_name}")
}

kind_configure_context() {
    local cluster_name="$1"
    local config="$2"
    [[ -z "${KIND_DIND_SERVICE_HOST:-}" ]] && return

    local api_port kubeconfig
    kubeconfig=$(kind_kubeconfig_path "${cluster_name}")
    kind export kubeconfig --name "${cluster_name}" --kubeconfig="${kubeconfig}"
    api_port=$(yq -er '.networking.apiServerPort' "${config}")
    CLUSTER_NAME_VALUE=${cluster_name} API_PORT_VALUE=${api_port} yq -i '
        (.clusters[] | select(.name == "kind-" + strenv(CLUSTER_NAME_VALUE)).cluster.server) =
        "https://" + strenv(KIND_DIND_SERVICE_HOST) + ":" + strenv(API_PORT_VALUE)' "${kubeconfig}"
}

# Cluster availability checks
is_k1_ready() {
    kubectl cluster-info --context "${K1_CONTEXT}" &>/dev/null
}

is_k2_ready() {
    kubectl cluster-info --context "${K2_CONTEXT}" &>/dev/null
}
