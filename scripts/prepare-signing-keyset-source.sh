#!/bin/bash

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTEXT="${1:-}"
VALUES_FILE="${2:-}"
OUTPUT_DIR="${3:-}"
NAMESPACE=dsx-identity
TEMP_DIR=""

source "${SCRIPT_DIR}/common.sh"

cleanup() {
    [[ -z "${TEMP_DIR}" || ! -d "${TEMP_DIR}" ]] || rm -rf "${TEMP_DIR}"
}

validate_sources() {
    local kid
    if [[ ! -s "${TEMP_DIR}/signing-jwks.json" || ! -s "${TEMP_DIR}/active-kid" ]]; then
        log_error "Exchange signing keyset source is incomplete"
        return 1
    fi
    kid=$(<"${TEMP_DIR}/active-kid")
    if ! jq -e --arg kid "${kid}" '.keys | any(.kid == $kid)' \
        "${TEMP_DIR}/signing-jwks.json" >/dev/null; then
        log_error "Exchange signing keyset does not contain its active kid"
        return 1
    fi
}

write_existing() {
    local secret_name="$1" json
    json=$(kubectl get secret "${secret_name}" -n "${NAMESPACE}" --context "${CONTEXT}" -o json)
    if ! jq -er '.data["signing-jwks.json"]' <<<"${json}" | base64 -d >"${TEMP_DIR}/signing-jwks.json" ||
        ! jq -er '.data["active-kid"]' <<<"${json}" | base64 -d >"${TEMP_DIR}/active-kid"; then
        log_error "Exchange signing keyset Secret is missing a required key. Refusing to rotate it"
        return 1
    fi
    log_info "Reusing the existing Exchange signing keyset in ${CONTEXT}"
}

generate_sources() {
    local generated
    generated=$(cd "${SCRIPT_DIR}/.." && go run ./cmd/generate-signing-keyset)
    jq -jc '.jwks' <<<"${generated}" >"${TEMP_DIR}/signing-jwks.json"
    jq -jr '.active_kid' <<<"${generated}" >"${TEMP_DIR}/active-kid"
    log_info "Generated an Exchange signing keyset source for ${CONTEXT}"
}

publish_sources() {
    local previous="${OUTPUT_DIR}.previous"
    rm -rf "${previous}"
    if [[ -d "${OUTPUT_DIR}" ]]; then
        mv "${OUTPUT_DIR}" "${previous}"
    fi
    mv "${TEMP_DIR}" "${OUTPUT_DIR}"
    TEMP_DIR=""
    rm -rf "${previous}"
}

main() {
    local secret_name
    if [[ -z "${CONTEXT}" || -z "${VALUES_FILE}" || -z "${OUTPUT_DIR}" ]]; then
        log_error "Usage: $0 <kube-context> <values-file> <output-directory>"
        return 1
    fi

    validate_context_exists "${CONTEXT}"
    secret_name=$(yq -r '.exchange.signingKeysetSecret // "spiffe-exchange-signing-keyset"' \
        "${VALUES_FILE}")
    mkdir -p "$(dirname "${OUTPUT_DIR}")"
    TEMP_DIR=$(mktemp -d "$(dirname "${OUTPUT_DIR}")/.generated.XXXXXX")

    if kubectl get secret "${secret_name}" -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
        write_existing "${secret_name}"
    else
        if kubectl get deployment spiffe-exchange -n "${NAMESPACE}" --context "${CONTEXT}" >/dev/null 2>&1; then
            log_error "Exchange exists but its signing keyset Secret is missing. Refusing to rotate it"
            return 1
        fi
        generate_sources
    fi
    validate_sources
    publish_sources
    log_success "Exchange signing keyset source is ready for ${CONTEXT}"
}

trap cleanup EXIT
trap 'exit 1' INT TERM
main "$@"
