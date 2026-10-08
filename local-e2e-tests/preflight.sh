#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${PROJECT_ROOT}"

if ! command -v mise >/dev/null 2>&1; then
    echo "mise is required. See https://mise.jdx.dev/getting-started.html" >&2
    exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required" >&2
    exit 1
fi
if ! command -v ssh-keygen >/dev/null 2>&1; then
    echo "OpenSSH client tools are required" >&2
    exit 1
fi
if ! docker buildx version >/dev/null 2>&1; then
    echo "Docker Buildx is required" >&2
    exit 1
fi
if ! docker info >/dev/null 2>&1; then
    echo "Docker is not running" >&2
    exit 1
fi

mise install

mise exec -- go version
mise exec -- kind version
mise exec -- kubectl version --client=true
mise exec -- helm version --short
mise exec -- skaffold version
docker version --format 'Docker client {{.Client.Version}}, server {{.Server.Version}}'

echo "Preflight complete"
