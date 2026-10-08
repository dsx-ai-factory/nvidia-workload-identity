#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

helm repo add spiffe https://spiffe.github.io/helm-charts-hardened/ --force-update

# Keep the pinned archive between Skaffold runs so only a clean workspace queries its repository.
nats_chart=charts/nats-2.14.2.tgz
if [[ ! -f "${nats_chart}" ]]; then
  helm repo add nats https://nats-io.github.io/k8s/helm/charts/ --force-update
  helm pull nats/nats --version 2.14.2 --destination charts
fi

helm dependency build --skip-refresh charts/dsx-identity
