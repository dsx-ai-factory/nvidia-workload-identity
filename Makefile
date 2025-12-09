# SPIFFE + OpenBao CEL Demo Makefile

.PHONY: help install clean test verify-spire verify-vm cluster kubevirt kyverno spire nginx bao metadata-service exchange-service vm status

# Default target
help: ## Show this help message
	@echo "SPIFFE + OpenBao CEL Demo"
	@echo "========================="
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: cluster kubevirt kyverno spire verify-spire exchange-service bao nginx metadata-service vm ## Complete setup: Kind cluster + KubeVirt + Kyverno + SPIRE + Exchange Service + OpenBao + Demo workload + Metadata Service + VM
	@echo ""
	@echo "Installation complete"
	@echo ""
	@echo "Your cluster is ready with:"
	@echo "- KubeVirt and CDI for virtual machine management"
	@echo "- Kyverno policy engine for Kubernetes governance"
	@echo "- SPIRE server and agent running"
	@echo "- OpenBao with CEL JWT authentication configured"
	@echo "- Demo nginx pod with SPIFFE identity"
	@echo "- JWT tokens automatically fetched by Go sidecar"
	@echo "- SPIFFE metadata service DaemonSet"
	@echo "- Lightweight Ubuntu VM for testing"
	@echo ""
	@echo "Next steps:"
	@echo "  make verify-vm # Verify VM metadata service"
	@echo "  make test      # Run integration tests"
	@echo "  make status    # Check component status"
	@echo "  make clean     # Clean up when done"

clean: ## Clean up: Remove Kind cluster and all resources
	@echo "Cleaning up SPIFFE + OpenBao demo..."
	@./scripts/01-setup-kind-cluster.sh cleanup
	@echo "Cleanup complete"

test: ## Run integration tests
	@echo "Running integration tests..."
	@./test-integration.sh
	@echo "Tests complete"

verify-spire: ## Verify SPIRE installation
	@echo "Verifying SPIRE installation..."
	@./scripts/05-verify-spire.sh

verify-vm: ## Verify VM metadata service
	@echo "Verifying VM metadata service..."
	@./scripts/10-verify-vm.sh

status: ## Show status of all components
	@echo "Component Status"
	@echo "==================="
	@echo ""
	@echo "Kind Cluster:"
	@kubectl get nodes -o wide 2>/dev/null || echo "Kind cluster not found"
	@echo ""
	@echo "SPIRE Server:"
	@kubectl get pods -n spire-server -l app.kubernetes.io/name=server 2>/dev/null || echo "SPIRE server not found"
	@echo ""
	@echo "SPIRE Agent:"
	@kubectl get pods -n spire-server -l app.kubernetes.io/name=agent 2>/dev/null || echo "SPIRE agent not found"
	@echo ""
	@echo "Demo Nginx:"
	@kubectl get pod demo-nginx 2>/dev/null || echo "Demo nginx not found"
	@echo ""
	@echo "OpenBao:"
	@kubectl get pods -n bao -l app.kubernetes.io/name=openbao 2>/dev/null || echo "OpenBao not found"
	@echo ""
	@echo "Metadata Service:"
	@kubectl get daemonset -l app=metadata-service 2>/dev/null || echo "Metadata service not found"
	@echo ""
	@echo "Exchange Service:"
	@kubectl get deployment spiffe-exchange-service 2>/dev/null || echo "Exchange service not found"
	@echo ""
	@echo "VM:"
	@kubectl get vm 2>/dev/null || echo "VM not found"

# Individual step targets for development
cluster: ## Create Kind cluster only
	@echo "Creating Kind cluster..."
	@./scripts/01-setup-kind-cluster.sh

kubevirt: ## Install KubeVirt and CDI only
	@echo "Installing KubeVirt and CDI..."
	@./scripts/02-install-kubevirt.sh

kyverno: ## Install Kyverno only
	@echo "Installing Kyverno..."
	@./scripts/03-install-kyverno.sh

spire: ## Install SPIRE only
	@echo "Installing SPIRE..."
	@./scripts/04-install-spire-helm.sh

exchange-service: ## Deploy SPIFFE exchange service only
	@echo "Deploying exchange service..."
	@./scripts/06-deploy-exchange-service.sh

bao: ## Deploy OpenBao only
	@echo "Deploying OpenBao..."
	@./scripts/07-deploy-bao.sh

nginx: ## Deploy demo nginx only
	@echo "Deploying demo nginx..."
	@./scripts/08-demo-nginx.sh

metadata-service: ## Deploy SPIFFE metadata service DaemonSet only
	@echo "Deploying metadata service..."
	@./scripts/09-deploy-metadata-service.sh

vm: ## Deploy lightweight Ubuntu VM only
	@echo "Deploying Ubuntu VM..."
	@./scripts/10-deploy-vm.sh
