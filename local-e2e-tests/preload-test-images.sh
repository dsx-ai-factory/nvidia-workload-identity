#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

if ! command -v timeout >/dev/null 2>&1; then
    log_error "GNU coreutils timeout is required for image preloading"
    exit 1
fi

preload_fixture() {
    local cluster="$1" fixture="$2" container="$3" image repository cached_image
    image=$(CONTAINER_NAME="${container}" yq -er \
        'select(.kind == "Pod") | .spec.containers[] | select(.name == strenv(CONTAINER_NAME)) | .image' \
        "${SCRIPT_DIR}/${fixture}")
    repository="${image#docker.io/}"
    if [[ "${repository}" != */* ]]; then
        repository="library/${repository}"
    fi
    cached_image="localhost:5002/${repository}"

    # Pull through Zot before kubelet can time out and fall back to Docker Hub.
    log_info "Preloading ${image} into ${cluster} through Zot"
    timeout --kill-after=30s 10m docker pull "${cached_image}"
    docker tag "${cached_image}" "${image}"
    timeout --kill-after=30s 5m kind load docker-image --name "${cluster}" "${image}"
}

preload_fixture k1 k1/nginx/e2e-nginx.yaml nginx
preload_fixture k2 k2/nginx/e2e-nginx.yaml nginx
preload_fixture k2 k2/vm/external-client.yaml external-client
