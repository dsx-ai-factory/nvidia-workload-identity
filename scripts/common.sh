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

# Cluster availability checks
is_k1_ready() {
    kubectl cluster-info --context "${K1_CONTEXT}" &>/dev/null
}

is_k2_ready() {
    kubectl cluster-info --context "${K2_CONTEXT}" &>/dev/null
}

require_k1() {
    if ! is_k1_ready; then
        log_error "k1 cluster is not available. Run k1 setup first."
        exit 1
    fi
}

require_k2() {
    if ! is_k2_ready; then
        log_error "k2 cluster is not available. Run k2 setup first."
        exit 1
    fi
}
