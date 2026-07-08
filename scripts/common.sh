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
COMMON_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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

DSX_OUTPUT_FOLLOWER_ACTIVE_PID=""

cleanup_command_output_follower() {
    local follower_pid="${DSX_OUTPUT_FOLLOWER_ACTIVE_PID:-${DSX_OUTPUT_FOLLOWER_PID:-}}"
    DSX_OUTPUT_FOLLOWER_ACTIVE_PID=""
    [[ -n "${follower_pid}" ]] || return 0

    kill "${follower_pid}" 2>/dev/null || true
    wait "${follower_pid}" 2>/dev/null || true
}

wait_for_command_output() {
    local deadline="$1"
    local first="$2"
    local second="$3"
    shift 3

    local follower_pid output_fd line matched=false
    coproc DSX_OUTPUT_FOLLOWER { exec timeout "${deadline}" "$@" 2>/dev/null; }
    follower_pid="${DSX_OUTPUT_FOLLOWER_PID:-}"
    DSX_OUTPUT_FOLLOWER_ACTIVE_PID="${follower_pid}"
    output_fd="${DSX_OUTPUT_FOLLOWER[0]}"
    while IFS= read -r -u "${output_fd}" line; do
        if [[ "${line}" == *"${first}"* && "${line}" == *"${second}"* ]]; then
            matched=true
            break
        fi
    done
    exec {output_fd}<&-
    cleanup_command_output_follower
    [[ "${matched}" == "true" ]]
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

# Kind publishes its API on the local host by default. When Docker is a remote
# CI service, publish on all interfaces, trust the service DNS name, and point
# the generated kubeconfig at that name instead.
kind_create_cluster() {
    local cluster_name="$1"
    local config="$2"

    if [[ -z "${KIND_DIND_SERVICE_HOST:-}" ]]; then
        kind create cluster --config="${config}" --name="${cluster_name}"
        return
    fi

    kind create cluster --name="${cluster_name}" --config=<(yq eval '
        .networking.apiServerAddress = "0.0.0.0" |
        (.nodes[] | select(.role == "control-plane").kubeadmConfigPatches) += [
          "kind: ClusterConfiguration\napiServer:\n  certSANs:\n    - " + strenv(KIND_DIND_SERVICE_HOST) + "\n"
        ]
    ' "${config}")
}

kind_configure_context() {
    local cluster_name="$1"
    local config="$2"
    [[ -z "${KIND_DIND_SERVICE_HOST:-}" ]] && return

    local api_port
    api_port=$(yq -er '.networking.apiServerPort' "${config}")
    kubectl config set-cluster "kind-${cluster_name}" \
        --server="https://${KIND_DIND_SERVICE_HOST}:${api_port}" >/dev/null
}

run_bao_cli() {
    local image="$1"
    shift

    if command -v bao >/dev/null 2>&1; then
        env -u BAO_NAMESPACE -u VAULT_NAMESPACE bao "$@"
        return
    fi

    local args=(
        run --rm --interactive --network host
        --entrypoint bao
        --env BAO_ADDR
        --env VAULT_ADDR
    )
    if [[ -n "${BAO_TOKEN:-}" ]]; then
        args+=(--env BAO_TOKEN --env VAULT_TOKEN)
    fi
    docker "${args[@]}" "${image}" "$@"
}

preload_manifest_images() {
    local cluster_name="$1"
    local images image

    images=$(awk '$1 == "image:" {gsub(/"/, "", $2); print $2}' | sort -u)
    if [[ -z "${images}" ]]; then
        log_error "No container images found in rendered manifests"
        return 1
    fi

    while IFS= read -r image; do
        [[ -z "${image}" ]] && continue
        "${COMMON_SCRIPT_DIR}/kind-load-image.sh" "${image}" "${cluster_name}"
    done <<<"${images}"
}

# Cluster availability checks
is_k1_ready() {
    kubectl cluster-info --context "${K1_CONTEXT}" &>/dev/null
}

is_k2_ready() {
    kubectl cluster-info --context "${K2_CONTEXT}" &>/dev/null
}
