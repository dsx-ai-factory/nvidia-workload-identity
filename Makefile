# DSX identity Makefile

.PHONY: preflight local-registry
.PHONY: help install clean test status
.PHONY: k1-cluster k2-cluster
.PHONY: validate test-go test-shell test-helm

MISE ?= mise
MISE_EXEC := $(MISE) exec --
SKAFFOLD_RUN := $(MISE_EXEC) skaffold run --iterative-status-check=false
IDENTITY_E2E_VALUES := -f scripts/identity-e2e-values.yaml

# Default target
help: ## Show this help message
	@echo "DSX identity"
	@echo "========================================"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

preflight:
	@./scripts/preflight.sh

validate: test-go test-shell test-helm ## Run local static validation

test-go:
	@$(MISE_EXEC) go test -race ./...

test-shell:
	@find scripts -type f -name '*.sh' -print0 | xargs -0 -r -n1 bash -n

test-helm:
	@$(MISE_EXEC) ./scripts/prepare-chart-dependencies.sh
	@$(MISE_EXEC) helm lint charts/dsx-identity $(IDENTITY_E2E_VALUES) -f scripts/k1-values.yaml
	@$(MISE_EXEC) helm lint charts/dsx-identity $(IDENTITY_E2E_VALUES) -f scripts/k2-values.yaml
	@$(MISE_EXEC) helm template dsx-identity charts/dsx-identity --namespace dsx-identity $(IDENTITY_E2E_VALUES) -f scripts/k1-values.yaml >/dev/null
	@$(MISE_EXEC) helm template dsx-identity charts/dsx-identity --namespace dsx-identity $(IDENTITY_E2E_VALUES) -f scripts/k2-values.yaml >/dev/null
	@$(MISE_EXEC) helm template dsx-identity charts/dsx-identity --namespace dsx-identity \
		$(IDENTITY_E2E_VALUES) -f scripts/k2-values.yaml \
		--set observability.metrics.enabled=true \
		--set observability.metrics.port=19464 \
		--set observability.metrics.serviceMonitor.enabled=true >/dev/null
	@$(MISE_EXEC) helm template dsx-imds-host-agent charts/dsx-identity/charts/spire-0.27.1.tgz \
		--namespace dsx-identity -f scripts/dsx-imds-host-agent-values.yaml \
		-f scripts/k1/dsx-imds-host-agent-values.yaml >/dev/null
	@$(MISE_EXEC) helm template dsx-imds-host-agent charts/dsx-identity/charts/spire-0.27.1.tgz \
		--namespace dsx-identity -f scripts/dsx-imds-host-agent-values.yaml \
		-f scripts/k2/dsx-imds-host-agent-values.yaml >/dev/null
	@$(MISE_EXEC) helm lint third-party/dsx-exchange/deploy/nats-event-bus -f scripts/k1/event-bus/values.yaml
	@$(MISE_EXEC) helm template event-bus third-party/dsx-exchange/deploy/nats-event-bus -f scripts/k1/event-bus/values.yaml >/dev/null
	@$(MISE_EXEC) skaffold diagnose --yaml-only >/dev/null

# ============================================================
# Multi-cluster environment with independent SPIRE and Exchange shards
# ============================================================

install: k1-cluster k2-cluster ## Install the complete local environment
	@trap '$(MISE_EXEC) ./scripts/cleanup-secret-sources.sh' EXIT INT TERM; \
		$(SKAFFOLD_RUN)

clean: ## Clean up: Remove both Kind clusters
	@echo "Cleaning up..."
	@$(MISE_EXEC) kind delete cluster --name k1
	@$(MISE_EXEC) kind delete cluster --name k2
	@echo "Cleanup complete"

test: ## Run the complete integration test sequence
	@$(MISE_EXEC) ./scripts/k2/verify-identity.sh
	@$(MISE_EXEC) ./scripts/verify-e2e.sh

status: ## Show status of all components
	@echo "Multi-Cluster Status"
	@echo "===================="
	@echo ""
	@echo "=== k1 Cluster ==="
	@$(MISE_EXEC) kubectl get nodes --context kind-k1 2>/dev/null || echo "k1 cluster not found"
	@echo ""
	@$(MISE_EXEC) kubectl get pods -n dsx-identity --context kind-k1 2>/dev/null || echo "No identity pods"
	@echo ""
	@$(MISE_EXEC) kubectl get pods -n openbao -l app.kubernetes.io/name=openbao --context kind-k1 2>/dev/null || echo "No OpenBao"
	@echo ""
	@echo "=== k2 Cluster ==="
	@$(MISE_EXEC) kubectl get nodes --context kind-k2 2>/dev/null || echo "k2 cluster not found"
	@echo ""
	@$(MISE_EXEC) kubectl get pods -n dsx-identity --context kind-k2 2>/dev/null || echo "No identity pods"

# ============================================================
# k1 targets
# ============================================================

local-registry: preflight
	@$(MISE_EXEC) ./scripts/setup-local-registry.sh

k1-cluster: local-registry
	@$(MISE_EXEC) ./scripts/k1/cluster/setup-cluster.sh

# ============================================================
# k2 targets
# ============================================================

k2-cluster: local-registry
	@$(MISE_EXEC) ./scripts/k2/cluster/create-cluster.sh
