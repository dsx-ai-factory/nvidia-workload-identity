# DSX identity Makefile

.PHONY: preflight local-registry
.PHONY: help install install-skaffold preload-vm-disk clean
.PHONY: test test-k2-identity test-cross-shard status
.PHONY: k1-cluster k2-cluster
.PHONY: validate test-go test-rust test-shell test-chart test-skaffold chart-dependencies

MISE ?= mise
MISE_EXEC := $(MISE) exec --
SKAFFOLD_RUN := $(MISE_EXEC) skaffold run --iterative-status-check=false
IDENTITY_E2E_VALUES := -f scripts/identity-e2e-values.yaml
KATA_UDS_PROXY := --manifest-path kata-uds-proxy/Cargo.toml

# Default target
help: ## Show this help message
	@echo "DSX identity"
	@echo "========================================"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

preflight:
	@./scripts/preflight.sh

validate: test-go test-rust test-shell test-chart test-skaffold ## Run local static validation

test-go:
	@$(MISE_EXEC) go test -race ./...

test-rust:
	@$(MISE_EXEC) cargo fmt $(KATA_UDS_PROXY) -- --check
	@$(MISE_EXEC) cargo clippy $(KATA_UDS_PROXY) --all-targets --locked -- -D warnings
	@$(MISE_EXEC) cargo test $(KATA_UDS_PROXY) --locked

test-shell:
	@find scripts kata-uds-proxy -type f -name '*.sh' -print0 | xargs -0 -r -n1 bash -n

test-chart: chart-dependencies
	@$(MISE_EXEC) helm lint charts/dsx-identity $(IDENTITY_E2E_VALUES) -f scripts/k1-values.yaml
	@$(MISE_EXEC) helm lint charts/dsx-imds --set metadataService.identityKeysURL=http://identity-keys.example/v1/exchange
	@$(MISE_EXEC) helm lint charts/dsx-spiffe-csi
	@$(MISE_EXEC) helm lint charts/nats-2.14.2.tgz -f scripts/k1/nats/values.yaml
	@# Normal E2E values disable metrics, so lint the optional ServiceMonitor resources separately.
	@$(MISE_EXEC) helm lint charts/dsx-identity $(IDENTITY_E2E_VALUES) -f scripts/k2-values.yaml \
		--set dpuService.enabled=true \
		--set dpuService.identityKeysURL=https://identity-keys.example/v1/exchange \
		--set observability.metrics.enabled=true \
		--set observability.metrics.port=19464 \
		--set observability.metrics.serviceMonitor.enabled=true

test-skaffold:
	@# Diagnose checks the full graph, while this render also resolves the prepared NATS chart path.
	@$(MISE_EXEC) skaffold diagnose --yaml-only >/dev/null
	@$(MISE_EXEC) skaffold render -f skaffold.yaml -m nats-k1 \
		--offline --digest-source=tag >/dev/null

chart-dependencies:
	@$(MISE_EXEC) ./scripts/prepare-chart-dependencies.sh

# ============================================================
# Multi-cluster environment with independent SPIRE and DSX Identity Keys shards
# ============================================================

# The documented parallel make invocation overlaps the VM disk pull with the
# root Skaffold workflow.
install: install-skaffold preload-vm-disk ## Install the complete local environment

install-skaffold: k1-cluster k2-cluster
	@trap '$(MISE_EXEC) ./scripts/cleanup-secret-sources.sh' EXIT INT TERM; \
		$(SKAFFOLD_RUN)

preload-vm-disk: k2-cluster
	@image="$$( $(MISE_EXEC) yq -er \
		'select(.kind == "VirtualMachine") | .spec.template.spec.volumes[] | select(has("containerDisk")) | .containerDisk.image' \
		scripts/k2/vm/vm.yaml )"; \
		started="$$(date +%s)"; \
		echo "[INFO] Ensuring VM container disk is present on k2-worker..."; \
		if ! docker exec k2-worker crictl inspecti "$$image" >/dev/null 2>&1; then \
			docker exec k2-worker crictl pull "$$image" >/dev/null || \
				echo "[WARNING] VM container disk preload failed. The VM will use its normal image pull."; \
		fi; \
		echo "[INFO] Timing: k2 VM container disk preload: $$(($$(date +%s) - started))s"

clean: ## Clean up: Remove both Kind clusters
	@echo "Cleaning up..."
	@$(MISE_EXEC) kind delete cluster --name k1
	@$(MISE_EXEC) kind delete cluster --name k2
	@echo "Cleanup complete"

test: ## Run the complete integration test sequence
	@$(MAKE) --no-print-directory -j2 test-k2-identity test-cross-shard

test-k2-identity:
	@$(MISE_EXEC) ./scripts/k2/verify-identity.sh

test-cross-shard:
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
