# SPIFFE + OpenBao CEL Multi-Cluster Demo Makefile

.PHONY: help install clean test verify-spire verify-nginx verify-vm cluster metallb gateway spire nginx bao metadata-service exchange-service postgres vm status k1 k2 k2-cluster k2-gateway kubevirt kyverno k2-spire configure-dns cert-manager-k2 sync-federation-ca helm-repos

# Default target
help: ## Show this help message
	@echo "SPIFFE + OpenBao CEL Multi-Cluster Demo"
	@echo "========================================"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: k1 k2 ## Complete setup: k1 + k2 + SPIRE federation
	@echo ""
	@echo "Installation complete"
	@echo ""
	@echo "Multi-cluster setup ready:"
	@echo "  k1: Root SPIRE (sjc-1.nvidia.com), OpenBao, Exchange Service"
	@echo "  k2: Child SPIRE (nested under k1), KubeVirt, workloads"
	@echo ""
	@echo "Next steps:"
	@echo "  make verify-nginx  # Verify nginx SPIFFE auth"
	@echo "  make verify-vm     # Verify VM metadata service"
	@echo "  make test          # Run all integration tests"
	@echo "  make status        # Check component status"
	@echo "  make clean         # Clean up when done"

configure-dns: gateway k2-gateway ## Configure cross-cluster DNS for both gateways
	@echo "Configuring cross-cluster DNS..."
	@./scripts/configure-dns.sh

# k1 cluster setup (primary: SPIRE root, Bao, Exchange, Gateway)
k1: cluster gateway spire exchange-service bao ## Setup k1 cluster with SPIRE root, OpenBao, Exchange, Gateway
	@echo ""
	@echo "k1 cluster ready"

# k2 cluster setup (secondary: workloads with nested SPIRE)
k2: k2-cluster kubevirt kyverno k2-spire nginx metadata-service vm ## Setup k2 cluster with workloads
	@echo ""
	@echo "k2 cluster ready"

clean: ## Clean up: Remove both Kind clusters
	@echo "Cleaning up multi-cluster demo..."
	@./scripts/k1/cluster/setup-cluster.sh cleanup 2>/dev/null || true
	@./scripts/k2/cluster/create-cluster.sh cleanup 2>/dev/null || true
	@echo "Cleanup complete"

test: verify-clusters verify-nginx verify-vm ## Run integration tests
	@echo "Tests complete"

verify-clusters: ## Verify cluster components (SPIRE, OpenBao, etc.)
	@echo "Verifying cluster components..."
	@./scripts/verify.sh

verify-nginx: ## Verify nginx SPIFFE authentication
	@echo "Verifying nginx SPIFFE auth..."
	@./scripts/k2/nginx/verify-nginx.sh

verify-vm: ## Verify VM metadata service
	@echo "Verifying VM metadata service..."
	@./scripts/k2/vm/verify-vm.sh

status: ## Show status of all components
	@echo "Multi-Cluster Status (Nested SPIRE)"
	@echo "===================================="
	@echo ""
	@echo "=== k1 Cluster (Root - sjc-1.nvidia.com) ==="
	@echo "Nodes:"
	@kubectl get nodes --context kind-k1 2>/dev/null || echo "k1 cluster not found"
	@echo ""
	@echo "SPIRE Server:"
	@kubectl get pods -n spire-system -l app.kubernetes.io/name=server --context kind-k1 2>/dev/null || echo "Not found"
	@echo ""
	@echo "PostgreSQL (CloudNativePG):"
	@kubectl get cluster postgres -n postgres-system --context kind-k1 2>/dev/null || echo "Not found"
	@echo ""
	@echo "Exchange Service:"
	@kubectl get deployment spiffe-exchange-service -n spiffe-exchange --context kind-k1 2>/dev/null || echo "Not found"
	@echo ""
	@echo "OpenBao:"
	@kubectl get pods -n bao -l app.kubernetes.io/name=openbao --context kind-k1 2>/dev/null || echo "Not found"
	@echo ""
	@echo "Gateway IP:"
	@kubectl get gateway k1-gateway -n envoy-gateway-system -o jsonpath='{.status.addresses[0].value}' --context kind-k1 2>/dev/null || echo "Not configured"
	@echo ""
	@echo ""
	@echo "=== k2 Cluster (Child - nested under k1) ==="
	@echo "Nodes:"
	@kubectl get nodes --context kind-k2 2>/dev/null || echo "k2 cluster not found"
	@echo ""
	@echo "Upstream Agent (connects to k1):"
	@kubectl get pods -n spire-system -l app.kubernetes.io/name=agent-upstream --context kind-k2 2>/dev/null || echo "Not found"
	@echo ""
	@echo "Internal Server:"
	@kubectl get pods -n spire-server -l app.kubernetes.io/name=internal-server --context kind-k2 2>/dev/null || echo "Not found"
	@echo ""
	@echo "Downstream Agent:"
	@kubectl get pods -n spire-system -l app.kubernetes.io/name=agent-downstream --context kind-k2 2>/dev/null || echo "Not found"
	@echo ""
	@echo "=== Workloads ==="
	@echo "Demo Nginx:"
	@kubectl get pod demo-nginx -n default --context kind-k2 2>/dev/null || echo "Not found"
	@echo ""
	@echo "VM:"
	@kubectl get vm -A --context kind-k2 2>/dev/null || echo "Not found"

# Helm repositories (serialized before parallel helm installs)
helm-repos:
	@./scripts/setup-helm-repos.sh

# k1 individual targets (with dependencies)
cluster: ## Create k1 Kind cluster
	@echo "Creating k1 cluster..."
	@./scripts/k1/cluster/setup-cluster.sh

gateway: cluster ## Install MetalLB and Envoy Gateway on k1
	@./scripts/install-metallb.sh k1
	@./scripts/install-envoy-gw.sh k1
	@./scripts/k1/gateway/apply-gateway.sh

sync-federation-ca: cert-manager-k2 cluster ## Sync k2's CA to k1 for federation
	@./scripts/sync-federation-ca.sh

spire: gateway sync-federation-ca helm-repos ## Install SPIRE root on k1 (with k2 federation CA)
	@echo "Installing SPIRE root on k1..."
	@./scripts/k1/spire/deploy-spire.sh

postgres: cluster helm-repos ## Deploy CloudNativePG and PostgreSQL cluster on k1
	@echo "Deploying PostgreSQL on k1..."
	@./scripts/k1/postgres/deploy-postgres.sh

exchange-service: cluster spire gateway postgres ## Deploy SPIFFE exchange service on k1
	@echo "Deploying exchange service on k1..."
	@./scripts/k1/exchange/deploy-exchange.sh

bao: cluster spire gateway exchange-service helm-repos ## Deploy OpenBao on k1 (requires exchange-service for JWT)
	@echo "Deploying OpenBao on k1..."
	@./scripts/k1/bao/deploy-bao.sh

# k2 individual targets (with dependencies)
k2-cluster: ## Create k2 Kind cluster with OVN
	@echo "Creating k2 cluster..."
	@./scripts/k2/cluster/create-cluster.sh

cert-manager-k2: k2-cluster ## Install cert-manager on k2
	@./scripts/install-cert-manager.sh k2

k2-gateway: k2-cluster ## Install MetalLB and Envoy Gateway on k2
	@./scripts/install-metallb.sh k2 220 230
	@./scripts/install-envoy-gw.sh k2
	@./scripts/k2/gateway/apply-gateway.sh

kubevirt: k2-cluster ## Install KubeVirt and CDI on k2
	@echo "Installing KubeVirt on k2..."
	@./scripts/k2/kubevirt/install-kubevirt.sh

kyverno: k2-cluster helm-repos ## Install Kyverno on k2
	@echo "Installing Kyverno on k2..."
	@./scripts/k2/kyverno/install-kyverno.sh

k2-spire: k2-cluster k2-gateway cert-manager-k2 helm-repos ## Install SPIRE on k2 (with cert-manager TLS for federation)
	@echo "Installing SPIRE on k2..."
	@./scripts/k2/spire/deploy-spire.sh

nginx: k2-cluster k2-spire configure-dns exchange-service ## Deploy demo nginx on k2
	@echo "Deploying demo nginx on k2..."
	@./scripts/k2/nginx/deploy-nginx.sh

metadata-service: k2-cluster k2-spire configure-dns exchange-service ## Deploy SPIFFE metadata service on k2
	@echo "Deploying metadata service on k2..."
	@./scripts/k2/metadata/deploy-metadata.sh

vm: k2-cluster k2-spire kubevirt kyverno metadata-service ## Deploy Ubuntu VM on k2
	@echo "Deploying Ubuntu VM on k2..."
	@./scripts/k2/vm/deploy-vm.sh
