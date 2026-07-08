# DSX identity Makefile

.PHONY: help install clean test verify-clusters verify-vm verify-openbao status
.PHONY: k1-cluster k1-gateway identity-k1 openbao configure-dns sync-event-bus-oauth helm-repos
.PHONY: k2-cluster kubevirt kyverno identity-k2 nginx vm
.PHONY: build-token-fetcher build-images build-images-k2 build-dsx-imds-agent build-images-all load-images-k2 chart-deps
.PHONY: identity-deploy identity-verify identity-template identity-clean
.PHONY: validate test-go test-shell test-helm event-bus-tools event-bus-prereqs

LOCAL_IMAGE_TAG := 0.1.0

# Default target
help: ## Show this help message
	@echo "DSX identity"
	@echo "========================================"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ============================================================
# Single-cluster smoke path
# ============================================================

identity-deploy: k1-cluster ## Deploy the k1 identity smoke environment
	@./scripts/deploy-identity.sh

identity-verify: ## Verify the k1 identity smoke environment
	@./scripts/verify-identity.sh

identity-template: chart-deps ## Render the identity umbrella
	@helm template dsx-identity charts/dsx-identity/ --namespace dsx-identity -f scripts/local-values.yaml --debug

identity-clean: ## Remove the k1 identity smoke environment
	@./scripts/deploy-identity.sh cleanup

validate: test-go test-shell test-helm ## Run local static validation

test-go: ## Run all Go tests
	@go test -race ./...

test-shell: ## Check every shell script for syntax errors
	@find scripts -type f -name '*.sh' -print0 | xargs -0 -r -n1 bash -n

test-helm: helm-repos chart-deps ## Lint and render both cluster profiles
	@helm lint charts/dsx-identity -f scripts/k1-values.yaml
	@helm lint charts/dsx-identity -f scripts/k2-values.yaml
	@helm template dsx-identity charts/dsx-identity --namespace dsx-identity -f scripts/k1-values.yaml >/dev/null
	@helm template dsx-identity charts/dsx-identity --namespace dsx-identity -f scripts/k2-values.yaml >/dev/null
	@helm lint third-party/dsx-exchange/deploy/nats-event-bus -f scripts/k1/event-bus/values.yaml
	@helm template event-bus third-party/dsx-exchange/deploy/nats-event-bus -f scripts/k1/event-bus/values.yaml --set global.eventBus.gateway.routes.mqtt.enabled=true >/dev/null

# ============================================================
# Multi-cluster environment with independent SPIRE and Exchange shards
# ============================================================

install: k1 k2 ## Complete setup: two local Exchange shards with one k1 Event Bus
	@echo ""
	@echo "Installation complete"
	@echo ""
	@echo "Multi-cluster setup ready:"
	@echo "  k1: SPIRE (sjc-1.nvidia.com), Exchange, Event Bus, OAuth2"
	@echo "  k2: SPIRE, Exchange, nginx Pod, KubeVirt VM"
	@echo "  external relying party: OpenBao (local k1 placement)"
	@echo ""
	@echo "Next steps:"
	@echo "  make test          # Run all integration tests"
	@echo "  make status        # Check component status"
	@echo "  make clean         # Clean up when done"

k1: openbao ## Set up Common Services and the external OpenBao test instance
	@echo ""
	@echo "k1 cluster ready"

k2: nginx vm ## Set up the Data Hall shard, nginx Pod, and KubeVirt VM
	@echo ""
	@echo "k2 cluster ready"

clean: ## Clean up: Remove both Kind clusters
	@echo "Cleaning up..."
	@./scripts/k1/cluster/setup-cluster.sh cleanup
	@./scripts/k2/cluster/create-cluster.sh cleanup
	@echo "Cleanup complete"

test: verify-openbao ## Run the complete integration test sequence
	@echo "Tests complete"

verify-clusters: ## Verify cluster components
	@./scripts/verify.sh

verify-vm: verify-clusters ## Verify VM metadata service
	@./scripts/k2/vm/verify-vm.sh

verify-openbao: verify-vm ## Verify OpenBao, restart, and rotation flows
	@./scripts/k1/openbao/verify-openbao.sh

status: ## Show status of all components
	@echo "Multi-Cluster Status"
	@echo "===================="
	@echo ""
	@echo "=== k1 Cluster ==="
	@kubectl get nodes --context kind-k1 2>/dev/null || echo "k1 cluster not found"
	@echo ""
	@kubectl get pods -n dsx-identity --context kind-k1 2>/dev/null || echo "No identity pods"
	@echo ""
	@kubectl get pods -n openbao -l app.kubernetes.io/name=openbao --context kind-k1 2>/dev/null || echo "No OpenBao"
	@echo ""
	@echo "=== k2 Cluster ==="
	@kubectl get nodes --context kind-k2 2>/dev/null || echo "k2 cluster not found"
	@echo ""
	@kubectl get pods -n dsx-identity --context kind-k2 2>/dev/null || echo "No identity pods"

# ============================================================
# Build targets
# ============================================================

build-token-fetcher: ## Build the shared token-fetcher image
	@docker build -t dsx-token-fetcher:$(LOCAL_IMAGE_TAG) -f Dockerfile.dsx-token-fetcher .

build-images: build-token-fetcher ## Build Exchange and token-fetcher images
	@docker build -t spiffe-exchange-service:$(LOCAL_IMAGE_TAG) -f Dockerfile.spiffe-exchange-service .

build-images-k2: build-images build-dsx-imds-agent ## Build k2 Exchange, controller, VM support, and dsx_imds plugins
	@DOCKER_BUILDKIT=1 docker build -t metadata-service:$(LOCAL_IMAGE_TAG) -f Dockerfile.metadata-service .
	@DOCKER_BUILDKIT=1 docker build -t dsx-imds-network-setup:$(LOCAL_IMAGE_TAG) -f Dockerfile.dsx-imds-network-setup .
	@DOCKER_BUILDKIT=1 docker build -t exchange-proxy-csi:$(LOCAL_IMAGE_TAG) -f Dockerfile.exchange-proxy-csi .
	@DOCKER_BUILDKIT=1 docker build -t dsx-identity-controller:$(LOCAL_IMAGE_TAG) -f Dockerfile.dsx-identity-controller .
	@DOCKER_BUILDKIT=1 docker build -t dsx-imds-node-attestor-server:$(LOCAL_IMAGE_TAG) -f Dockerfile.dsx-imds-node-attestor-server .

build-dsx-imds-agent: ## Build the repository-owned host-agent plugin artifact
	@DOCKER_BUILDKIT=1 docker build -t dsx-imds-node-attestor-agent:$(LOCAL_IMAGE_TAG) -f Dockerfile.dsx-imds-node-attestor-agent .

load-images-k2: k2-cluster build-images-k2 ## Load locally built k2 images into Kind
	@./scripts/kind-load-image.sh metadata-service:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh dsx-imds-network-setup:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh exchange-proxy-csi:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh dsx-identity-controller:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh dsx-imds-node-attestor-server:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh dsx-imds-node-attestor-agent:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh spiffe-exchange-service:$(LOCAL_IMAGE_TAG) k2
	@./scripts/kind-load-image.sh dsx-token-fetcher:$(LOCAL_IMAGE_TAG) k2

build-images-all: build-images build-images-k2 ## Build all repository-owned images

chart-deps: helm-repos ## Build Helm chart dependencies
	@helm dependency build --skip-refresh third-party/dsx-exchange/deploy/nats-event-bus
	@helm dependency build --skip-refresh charts/dsx-identity

event-bus-tools: ## Install pinned DSX Event Bus NKey tools when missing
	@test -x "$(CURDIR)/bin/nsc" || GOBIN="$(CURDIR)/bin" go install github.com/nats-io/nsc/v2@v2.14.0
	@test -x "$(CURDIR)/bin/nk" || GOBIN="$(CURDIR)/bin" go install github.com/nats-io/nkeys/nk@v0.4.15

event-bus-prereqs: k1-gateway event-bus-tools chart-deps ## Prepare the k1 DSX Event Bus behind its Gateway
	@PATH="$(CURDIR)/bin:$$PATH" ./scripts/k1/event-bus/prepare-event-bus.sh

helm-repos:
	@./scripts/setup-helm-repos.sh

configure-dns: k1-gateway k2-cluster ## Configure k2 DNS for the k1 Event Bus and OpenBao Gateway
	@./scripts/configure-dns.sh

sync-event-bus-oauth: event-bus-prereqs k2-cluster ## Copy the k1 Exchange OAuth client credential into k2
	@./scripts/sync-event-bus-oauth.sh

# ============================================================
# k1 targets
# ============================================================

k1-cluster: ## Create the Common Services Kind cluster
	@./scripts/k1/cluster/setup-cluster.sh

k1-gateway: k1-cluster ## Install MetalLB and Envoy Gateway on k1
	@./scripts/install-metallb.sh k1
	@./scripts/install-envoy-gw.sh k1

identity-k1: k1-gateway event-bus-prereqs helm-repos chart-deps build-images ## Deploy the Common Services identity shard
	@./scripts/kind-load-image.sh spiffe-exchange-service:$(LOCAL_IMAGE_TAG) k1
	@./scripts/kind-load-image.sh dsx-token-fetcher:$(LOCAL_IMAGE_TAG) k1
	@./scripts/deploy-identity.sh -c kind-k1 -f scripts/k1-values.yaml

openbao: identity-k1 helm-repos ## Deploy the external OpenBao test instance in k1
	@./scripts/k1/openbao/deploy-openbao.sh

# ============================================================
# k2 targets
# ============================================================

k2-cluster: ## Create k2 Kind cluster with OVN
	@./scripts/k2/cluster/create-cluster.sh

kubevirt: k2-cluster ## Install KubeVirt on k2
	@./scripts/k2/kubevirt/install-kubevirt.sh

kyverno: k2-cluster helm-repos ## Install Kyverno on k2
	@./scripts/install-kyverno.sh k2

identity-k2: k2-cluster kyverno load-images-k2 helm-repos chart-deps identity-k1 configure-dns sync-event-bus-oauth ## Deploy the Data Hall identity shard
	@./scripts/deploy-identity.sh -c kind-k2 -f scripts/k2-values.yaml

nginx: identity-k2 ## Deploy the E2E nginx fixture on k2
	@./scripts/k2/nginx/deploy-nginx.sh

vm: identity-k2 kubevirt ## Deploy Ubuntu VM and its tenant fixtures on k2
	@./scripts/k2/vm/deploy-vm.sh
