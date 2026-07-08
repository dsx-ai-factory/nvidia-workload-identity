#!/bin/bash
# setup-helm-repos.sh - Adds and updates all required Helm repositories
# This script is called once before parallel helm installs to avoid race conditions

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

REPOS=(
    "spiffe|https://spiffe.github.io/helm-charts-hardened/"
    "kyverno|https://kyverno.github.io/kyverno/"
    "openbao|https://openbao.github.io/openbao-helm"
    "nats|https://nats-io.github.io/k8s/helm/charts/"
)

setup_repos() {
    local needs_update=false
    
    for repo_entry in "${REPOS[@]}"; do
        local name="${repo_entry%%|*}"
        local url="${repo_entry##*|}"
        local cache_file="${HOME}/.cache/helm/repository/${name}-index.yaml"
        
        if ! helm repo list 2>/dev/null | grep -q "^${name}"; then
            log_info "Adding Helm repo: ${name}"
            helm repo add "${name}" "${url}"
            needs_update=true
        elif [[ ! -f "$cache_file" ]]; then
            log_info "Helm repo ${name} missing cache, will update"
            needs_update=true
        fi
    done
    
    if [[ "$needs_update" == "true" ]]; then
        log_info "Updating Helm repositories..."
        helm repo update >/dev/null
        log_success "Helm repositories updated"
    fi
}

setup_repos
