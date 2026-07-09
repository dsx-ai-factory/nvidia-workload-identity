#!/bin/bash

set -euo pipefail

helm repo add nats https://nats-io.github.io/k8s/helm/charts/ --force-update
helm repo add spiffe https://spiffe.github.io/helm-charts-hardened/ --force-update
helm dependency build --skip-refresh third-party/dsx-exchange/deploy/nats-event-bus
helm dependency build --skip-refresh charts/dsx-identity
