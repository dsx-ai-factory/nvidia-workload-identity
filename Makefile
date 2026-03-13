# SPIFFE + OpenBao CEL Multi-Cluster Demo Makefile

.PHONY: help install clean test verify-clusters verify-nginx verify-vm status
.PHONY: cluster gateway postgres spiffe bao configure-dns sync-federation-ca cert-manager-k2 helm-repos
.PHONY: k2-cluster k2-gateway kubevirt kyverno spiffe-k2 nginx metadata-service vm
.PHONY: build-images build-images-all chart-deps spiffe-deploy spiffe-verify spiffe-template spiffe-clean

# Default target
help: ## Show this help message
	@echo "SPIFFE + OpenBao CEL Multi-Cluster Demo"
	@echo "========================================"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ============================================================
# Quick start (production-like, single cluster)
# ============================================================

spiffe-deploy: cluster helm-repos chart-deps build-images ## Deploy full SPIFFE stack via umbrella chart
	@./scripts/deploy-spiffe.sh

spiffe-verify: ## Smoke test: verify SPIFFE stack is healthy
	@./scripts/verify-spiffe.sh

spiffe-template: chart-deps ## Validate SPIFFE umbrella chart templates
	@helm template spiffe charts/spiffe/ -f scripts/local-values.yaml --debug

spiffe-clean: ## Uninstall SPIFFE charts
	@./scripts/deploy-spiffe.sh cleanup

# ============================================================
# Multi-cluster demo (k1 = root, k2 = child)
# ============================================================

install: k1 k2 ## Complete setup: k1 + k2 + SPIRE federation
	@echo ""
	@echo "Installation complete"
	@echo ""
	@echo "Multi-cluster setup ready:"
	@echo "  k1: Root SPIRE (sjc-1.nvidia.com), OpenBao, Exchange Service"
	@echo "  k2: Child SPIRE (nested under k1), KubeVirt, workloads"
	@echo ""
	@echo "Next steps:"
	@echo "  make test          # Run all integration tests"
	@echo "  make status        # Check component status"
	@echo "  make clean         # Clean up when done"

k1: cluster gateway spiffe bao ## Setup k1 with SPIRE + Exchange + OpenBao
	@echo ""
	@echo "k1 cluster ready"

k2: k2-cluster kubevirt kyverno spiffe-k2 nginx metadata-service vm ## Setup k2 with workloads
	@echo ""
	@echo "k2 cluster ready"

clean: ## Clean up: Remove both Kind clusters
	@echo "Cleaning up..."
	@./scripts/k1/cluster/setup-cluster.sh cleanup 2>/dev/null || true
	@./scripts/k2/cluster/create-cluster.sh cleanup 2>/dev/null || true
	@echo "Cleanup complete"

test: verify-clusters verify-nginx verify-vm ## Run integration tests
	@echo "Tests complete"

verify-clusters: ## Verify cluster components
	@./scripts/verify.sh

verify-nginx: ## Verify nginx SPIFFE authentication
	@./scripts/k2/nginx/verify-nginx.sh

verify-vm: ## Verify VM metadata service
	@./scripts/k2/vm/verify-vm.sh

status: ## Show status of all components
	@echo "Multi-Cluster Status"
	@echo "===================="
	@echo ""
	@echo "=== k1 Cluster ==="
	@kubectl get nodes --context kind-k1 2>/dev/null || echo "k1 cluster not found"
	@echo ""
	@kubectl get pods -n dsx-spiffe --context kind-k1 2>/dev/null || echo "No SPIFFE pods"
	@echo ""
	@kubectl get pods -n bao -l app.kubernetes.io/name=openbao --context kind-k1 2>/dev/null || echo "No OpenBao"
	@echo ""
	@echo "=== k2 Cluster ==="
	@kubectl get nodes --context kind-k2 2>/dev/null || echo "k2 cluster not found"
	@echo ""
	@kubectl get pods -n dsx-spiffe --context kind-k2 2>/dev/null || echo "No SPIFFE pods"

# ============================================================
# Build targets
# ============================================================

build-images: ## Build core SPIFFE Docker images (exchange + token-fetcher)
	@docker build -t spiffe-exchange-service:latest -f Dockerfile.spiffe-exchange-service .
	@docker build -t spiffe-token-fetcher:latest -f Dockerfile.spiffe-token-fetcher .

build-images-all: build-images ## Build all images including VM support (requires BuildKit)
	@DOCKER_BUILDKIT=1 docker build -t metadata-service:latest -f Dockerfile.metadata-service .
	@DOCKER_BUILDKIT=1 docker build -t mds-network-setup:latest -f Dockerfile.mds-network-setup .
	@DOCKER_BUILDKIT=1 docker build -t exchange-proxy-csi:latest -f Dockerfile.exchange-proxy-csi .

chart-deps: ## Build Helm chart dependencies
	@cd charts/spiffe-spire && helm dep build 2>/dev/null
	@cd charts/spiffe && helm dep build 2>/dev/null

helm-repos:
	@./scripts/setup-helm-repos.sh

configure-dns: gateway k2-gateway ## Configure cross-cluster DNS
	@./scripts/configure-dns.sh

# ============================================================
# k1 targets
# ============================================================

cluster: ## Create k1 Kind cluster
	@./scripts/k1/cluster/setup-cluster.sh

gateway: cluster ## Install MetalLB + Envoy Gateway on k1
	@./scripts/install-metallb.sh k1
	@./scripts/install-envoy-gw.sh k1
	@./scripts/k1/gateway/apply-gateway.sh

postgres: cluster helm-repos ## Deploy shared CNPG cluster on k1
	@./scripts/k1/postgres/deploy-postgres.sh

sync-federation-ca: cert-manager-k2 cluster ## Sync k2 CA to k1
	@./scripts/sync-federation-ca.sh

spiffe: gateway sync-federation-ca postgres helm-repos chart-deps build-images ## Deploy SPIFFE umbrella chart on k1
	@echo "Deploying SPIFFE on k1 via umbrella chart..."
	@kind load docker-image spiffe-exchange-service:latest --name k1
	@kind load docker-image spiffe-token-fetcher:latest --name k1
	@./scripts/deploy-spiffe.sh -c kind-k1 -f scripts/k1-values.yaml
	@./scripts/k1/exchange/configure-tenant-dns.sh

bao: spiffe helm-repos ## Deploy OpenBao on k1
	@./scripts/k1/bao/deploy-bao.sh

# ============================================================
# k2 targets
# ============================================================

k2-cluster: ## Create k2 Kind cluster with OVN
	@./scripts/k2/cluster/create-cluster.sh

cert-manager-k2: k2-cluster ## Install cert-manager on k2
	@./scripts/install-cert-manager.sh k2

k2-gateway: k2-cluster ## Install MetalLB + Envoy Gateway on k2
	@./scripts/install-metallb.sh k2 220 230
	@./scripts/install-envoy-gw.sh k2
	@./scripts/k2/gateway/apply-gateway.sh

kubevirt: k2-cluster ## Install KubeVirt and CDI on k2
	@./scripts/k2/kubevirt/install-kubevirt.sh

kyverno: k2-cluster helm-repos ## Install Kyverno on k2
	@./scripts/k2/kyverno/install-kyverno.sh

spiffe-k2: k2-cluster k2-gateway cert-manager-k2 helm-repos chart-deps ## Deploy SPIFFE umbrella chart on k2
	@echo "Deploying SPIFFE on k2 via umbrella chart..."
	@./scripts/deploy-spiffe.sh -c kind-k2 -f scripts/k2-values.yaml
	@echo "Applying SPIRE bundle endpoint route..."
	@kubectl apply -f scripts/k2/spire/spire-bundle-route.yaml --context kind-k2

nginx: k2-cluster spiffe-k2 configure-dns spiffe ## Deploy demo nginx on k2
	@./scripts/k2/nginx/deploy-nginx.sh

metadata-service: k2-cluster spiffe-k2 configure-dns spiffe ## Deploy metadata service on k2
	@./scripts/k2/metadata/deploy-metadata.sh

vm: k2-cluster spiffe-k2 kubevirt kyverno metadata-service ## Deploy Ubuntu VM on k2
	@./scripts/k2/vm/deploy-vm.sh
