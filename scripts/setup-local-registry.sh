#!/bin/bash

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REGISTRY_NAME=dsx-identity-registry
REGISTRY_PORT=5002
REGISTRY_CONTAINER_PORT=5000
REGISTRY_HOST=127.0.0.1
REGISTRY_READY_HOST=127.0.0.1
REGISTRY_VOLUME=dsx-identity-registry-zot-data
CONFIG_FILE="${PROJECT_ROOT}/scripts/local-registry/zot-config.json"
runtime_config="$(mktemp)"
existing_config="$(mktemp)"
trap 'rm -f "${runtime_config}" "${existing_config}"' EXIT

source "${PROJECT_ROOT}/scripts/common.sh"

if [[ -n "${DOCKERHUB_MIRROR:-}" ]]; then
    jq --arg mirror "${DOCKERHUB_MIRROR}" \
        '(.extensions.sync.registries[] | select(.urls[0] == "https://registry-1.docker.io").urls) = [$mirror]' \
        "${CONFIG_FILE}" > "${runtime_config}"
else
    cp "${CONFIG_FILE}" "${runtime_config}"
fi

if [[ -n "${KIND_DIND_SERVICE_HOST:-}" ]]; then
    REGISTRY_HOST=0.0.0.0
    REGISTRY_READY_HOST="${KIND_DIND_SERVICE_HOST}"
fi

registry_image() {
    case "$(docker info --format '{{.Architecture}}')" in
        amd64|x86_64) printf '%s\n' 'ghcr.io/project-zot/zot-linux-amd64:v2.1.10' ;;
        arm64|aarch64) printf '%s\n' 'ghcr.io/project-zot/zot-linux-arm64:v2.1.10' ;;
        *)
            log_error "Unsupported Docker architecture: $(docker info --format '{{.Architecture}}')"
            return 1
            ;;
    esac
}

docker network inspect kind >/dev/null 2>&1 || docker network create kind >/dev/null

if docker container inspect "${REGISTRY_NAME}" >/dev/null 2>&1 && \
    { ! docker cp "${REGISTRY_NAME}:/etc/zot/config.json" "${existing_config}" >/dev/null 2>&1 || \
      ! cmp -s "${runtime_config}" "${existing_config}"; }; then
    # Retain the cache volume while replacing a container with stale proxy settings.
    docker rm -f "${REGISTRY_NAME}" >/dev/null
fi

if ! docker container inspect "${REGISTRY_NAME}" >/dev/null 2>&1; then
    docker create --restart=always \
        --name "${REGISTRY_NAME}" \
        --network kind \
        -p "${REGISTRY_HOST}:${REGISTRY_PORT}:${REGISTRY_CONTAINER_PORT}" \
        -v "${REGISTRY_VOLUME}:/var/lib/registry" \
        "$(registry_image)" >/dev/null
    docker cp "${runtime_config}" "${REGISTRY_NAME}:/etc/zot/config.json"
    docker start "${REGISTRY_NAME}" >/dev/null
elif [[ "$(docker inspect --format '{{.State.Running}}' "${REGISTRY_NAME}")" != "true" ]]; then
    docker start "${REGISTRY_NAME}" >/dev/null
fi

curl --fail --silent --show-error \
    --retry 30 --retry-all-errors --retry-connrefused --retry-delay 1 \
    --connect-timeout 1 --max-time 5 \
    "http://${REGISTRY_READY_HOST}:${REGISTRY_PORT}/v2/" >/dev/null

log_success "Persistent upstream image cache is ready"
