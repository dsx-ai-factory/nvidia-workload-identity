# Retry Blocked File Operations

## When to Use
- File read operation is blocked by `.cursorignore`
- File write operation is blocked by `.cursorignore`
- Any tool call returns "filtered out by .cursorignore" error

## Instructions

### 1. Auto-Retry (3 attempts)
When a file operation fails with "filtered out by .cursorignore":
1. Silently retry the operation up to 3 times
2. If any retry succeeds, continue with the task
3. If all 3 retries fail, proceed to step 2

### 2. Prompt User After 3 Failures
After 3 failed attempts:
1. Inform the user which file was blocked
2. Report that 3 automatic retries failed
3. Wait for user response

### 3. Handle User Response
**If user says "try again", "retry", "continue", or similar:**
- Retry the operation (single attempt)
- If still blocked, wait for user response again

**If user says "stop", "cancel", "skip", "abort", or similar:**
- Abandon the blocked operation
- Continue with other tasks if any

**If user provides file content directly:**
- Use the provided content instead of reading the file
- Proceed with the task

### 4. On Success
- Continue with the original task

## Decision Points

### If blocked multiple times on same file:
1. First 3 retries are automatic and silent
2. After that, continue retrying as long as user says "try again"
3. User may need time to update `.cursorignore`
4. Only stop when user requests or operation succeeds

### If multiple files are blocked:
1. Each file gets its own 3 automatic retries
2. Report all files that failed after their auto-retries
3. Retry each as user permits

---

# SPIFFE + OpenBao CEL Project Rules

## Project Overview
This project demonstrates SPIFFE JWT authentication with OpenBao's CEL (Common Expression Language) enabled JWT auth method. It includes:
- Multi-cluster Kind setup: k1 (primary) and k2 (secondary)
- SPIFFE Federation: k1 accepts k2's SVIDs via bundle endpoint
- k1: SPIRE server, OpenBao, Exchange service, Envoy Gateway
- k2: Independent SPIRE server (exposes bundle endpoint), KubeVirt, workloads
- SPIFFE workload identity management via ClusterSPIFFEID CRDs
- OpenBao deployment with CEL-based dynamic policy assignment
- Go sidecar for JWT token fetching and refresh
- End-to-end integration testing

## Core Architecture Principles

### SPIFFE Identity Pattern
- Base domain: `nvidia.com`
- Pattern: `spiffe://{tenant}.{region}.{service}.{base-domain}/{resource_type}/{resource_id}`
- Examples:
  - Pod: `spiffe://tenant1.sjc-1.internal.nvidia.com/pod/{uuid}`
  - VM: `spiffe://tenant1.sjc-1.vmaas.nvidia.com/vm/{uuid}`
- Components from trust domain: tenant, region, service
- Components from path: resource_type, resource_id
- Required pod labels: `spiffe.nvidia.com/tenant`, `spiffe.nvidia.com/service`
- SPIRE template: `/tenant/{tenant}/service/{service}/{resource_type}/{resource_id}`

### Multi-Cluster Architecture
- k1 (primary): SPIRE server, OpenBao, Exchange service, Envoy Gateway with MetalLB
- k2 (secondary): SPIRE server (independent), KubeVirt, Kyverno, workloads (nginx, VMs), Envoy Gateway with MetalLB
- Cross-cluster communication via Envoy Gateway with Kubernetes Gateway API
- Only k2's SPIRE bundle endpoint is exposed externally (for federation)
- OVN-Kubernetes CNI required in k2 for UDN support
- Makefile supports parallel execution (`make -j`) with proper dependencies

### OpenBao CEL Integration
- Use JWT CEL authentication (`/v1/auth/jwt/cel/login`), not static JWT auth
- CEL program parses SPIFFE ID components for dynamic policy assignment
- Trust domain format: `{tenant}.{region}.{service}.nvidia.com`
- Path format: `/{resource_type}/{resource_id}`
- Policies: `spiffe-authenticated`, `region-*`, `tenant-*`, `service-*`, `resource-type-*`
- Supports both pod and VM workload types
- Use OIDC Discovery Provider with TLS certificate extraction

## Development Guidelines

### File Organization
- **NEVER embed files in scripts** - always reference external files on disk
- Cluster-specific scripts: `scripts/k1/{cluster,gateway,spire,bao,exchange}/` and `scripts/k2/{cluster,gateway,spire,kubevirt,kyverno,metadata,nginx,vm}/`
- Shared scripts at top level: `scripts/install-metallb.sh`, `scripts/install-envoy-gw.sh` (parameterized by cluster name)
- Keep related config files alongside their deploy scripts (e.g., `scripts/k1/spire/spire-values.yaml`)
- Policy files: Use separate `.hcl` files for each OpenBao policy in `scripts/k1/bao/policies/`
- Template files: Use `.tmpl` extension only for files with placeholder values that require sed/envsubst processing
- For Helm/kubectl configurations with dynamic values, prefer command-line overrides (e.g., `--set key=value`) over templating
- Each component manages its own Gateway API routes (e.g., `scripts/k1/bao/bao-route.yaml`)

### Script Requirements
- **All scripts must be repeatable and idempotent**
- Use `curl` instead of `wget` for all HTTP requests
- Scripts must handle cleanup and error scenarios
- Use common logging functions from `common.sh`

### Container and Image Standards
- Build images locally from source when using unreleased changes
- Load all custom images into Kind cluster via `kind load docker-image`
- Use specific image tags, avoid `latest` in production-like configs
- Go applications: Use multi-stage builds with minimal runtime images
- Include resource limits and requests for all containers
- KubeVirt VMs: Use containerDisk volumes with `imagePullPolicy: IfNotPresent` for image caching
- **Image preloading**: Extract images dynamically from helm charts/manifests, never hardcode in arrays
- Define workload images as variables at top of scripts (e.g., `NGINX_IMAGE="nginx:alpine"`)
- Image preloads must not fail silently - no `|| true` on kind-load-image.sh calls

### Kubernetes Patterns
- Use Helm charts with values files instead of raw YAML
- Configure via `values.yaml` files, use `--set` for dynamic values only
- Use `kubectl rollout status` for Deployments, StatefulSets, and DaemonSets (avoids race conditions)
- Use `kubectl wait` for Pods and other resources, with timeouts instead of sleep loops

### SPIRE Configuration
- Use official SPIRE Helm charts (`spiffe/spire`)
- k1 and k2 run independent SPIRE servers with different trust domains
- k1 trust domain: `sjc-1.nvidia.com`, k2 trust domain: `k2.sjc-1.nvidia.com`
- SPIFFE Federation: k1 accepts k2's SVIDs via `clusterFederatedTrustDomains`
- k2 exposes bundle endpoint via Envoy Gateway (port 8443) for federation
- k1 does NOT need SPIRE ingress - only k2 bundle endpoint crosses clusters
- Enable SPIFFE OIDC Discovery Provider for JWT validation
- Use CSI driver for workload token access, not hostPath mounts

### TPM-based Node Attestation (PoC)
- Use `spiffe/spire-tpm-plugin` for TPM-based node attestation instead of PSAT
- Deploy `swtpm` as a DaemonSet to provide virtual TPM for Kind nodes
- swtpm exposes Unix socket at `/var/run/swtpm/swtpm-sock`
- SPIRE agent uses `TPM2TOOLS_TCTI=swtpm:path=/var/run/swtpm/swtpm-sock` env var
- TPM CA is stored in `tpm-ca` Kubernetes Secret in spire-system namespace
- Use `customPlugins.nodeAttestor` in Helm values for external TPM plugin
- Plugin binaries are copied via initContainers from official plugin images
- swtpm and TPM CA are deployed via `scripts/swtpm/deploy-swtpm.sh`

### OpenBao Configuration
- Use official OpenBao 2.4.0 release from quay.io/openbao/openbao:2.4.0
- Use official OpenBao Helm chart with official image
- Configure JWT auth with OIDC Discovery URL and CA certificate
- Extract TLS certificates using temporary Alpine containers
- Use `bao` CLI with port-forwarding, not `kubectl exec` with `curl`
- Use temporary files for multi-line content (certificates, policies)
- Clean up temporary files and background processes

### PostgreSQL for Exchange Service
- Use CloudNativePG operator (CNCF Sandbox) for PostgreSQL deployment on k1
- Operator installed via Helm chart (`cnpg/cloudnative-pg`) in `cnpg-system` namespace
- Database cluster (`Cluster` CRD) deployed in `postgres-system` namespace
- Keyset table created via `postInitSQL` in cluster spec
- AES-256-GCM encryption for private key material before storage
- Encryption key provided via `EXCHANGE_KEY_ENCRYPTION_KEY_B64` env var (base64-encoded 32-byte key)
- CloudNativePG creates `postgres-app` secret with credentials, copied to `spiffe-exchange` namespace
- Service endpoint: `postgres-rw.postgres-system.svc.cluster.local`
- Postgres is a dependency of `exchange-service` target in Makefile

### Go Development
- Use Go 1.25+
- Import `spiffe-go` version matching base repository
- Use structured logging and proper error handling
- Build from project root with `cmd/` pattern

### Testing and Validation
- Include comprehensive integration tests
- Test both positive and negative scenarios  
- Verify SPIFFE ID assignment and CEL policy evaluation
- Check token refresh and retry mechanisms
- Validate OpenBao authentication and secret access
- Include end-to-end cleanup and restart testing

## Code Style Guidelines

### Shell Scripts
- Use `#!/bin/bash` shebang
- Include error handling with `set -euo pipefail` where appropriate  
- Use consistent logging with color coding (only in `common.sh`)
- Include usage documentation and examples
- Use meaningful variable names with consistent casing
- Include cleanup functions with proper trap handling

### YAML Configuration
- Use 2-space indentation
- Include resource limits and requests
- Use meaningful labels and annotations
- Include proper volume mounts and security contexts
- Use ConfigMaps/Secrets for sensitive data when possible

### Go Code
- Follow standard Go formatting and conventions
- Use structured logging with consistent fields
- Include comprehensive error handling and context
- Use flag package for CLI arguments with sensible defaults
- Include unit tests for business logic
- Use proper context handling for cancellation

## Naming Conventions
- Avoid redundant terminology (e.g., "JWT token" → "JWT")
- Use consistent naming: `spiffe-token-fetcher`, `demo-nginx`, `openbao`
- Kubernetes resources: Use descriptive names with component labels
- Environment variables: Use UPPER_SNAKE_CASE
- File names: Use kebab-case for scripts, camelCase for Go

## Dependencies and Versions
- Kubernetes: v1.30+ (required by OpenBao Helm chart)
- Kind: Latest stable version with `kindest/node:v1.30.0`
- SPIRE: Use latest stable Helm chart (`spiffe/spire`)
- OpenBao: Version 2.4.0 official release
- Go: Version 1.25+ with matching spiffe-go library version

## Common Patterns to Follow

### Error Handling
```bash
if ! command; then
    log_error "Command failed"
    cleanup_function
    return 1
fi
```

### Helm Repository Management
```bash
# Add Helm repository conditionally
if ! helm repo list | grep -q "repo-name"; then
    helm repo add repo-name https://repo-url/
    helm repo update >/dev/null
fi
```

### Kubernetes Resource Waiting
```bash
# For Deployments and DaemonSets - use rollout status (requires RollingUpdate strategy)
kubectl rollout status deployment/deployment-name --timeout=60s
kubectl rollout status daemonset/daemonset-name --timeout=60s

# For StatefulSets - use rollout status only if using RollingUpdate strategy
# Otherwise, use kubectl wait for pods
kubectl rollout status statefulset/statefulset-name --timeout=60s  # Only if RollingUpdate
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=app-name --timeout=60s  # Fallback

# For Pods and other resources - use kubectl wait
kubectl wait --for=condition=Ready pod/pod-name --timeout=60s
```

### Port Forward with Cleanup
Use a script-level variable and EXIT trap to ensure cleanup on any exit (error, signal, or normal):
```bash
# At script level (after sourcing common.sh)
PF_PID=""

cleanup_port_forward() {
    if [[ -n "$PF_PID" ]]; then
        kill "$PF_PID" 2>/dev/null || true
        PF_PID=""
    fi
}

# In functions that use port-forward
some_function() {
    kubectl port-forward svc/service 8200:8200 --context "${CONTEXT}" &
    PF_PID=$!
    sleep 3
    
    # ... do work ...
    
    cleanup_port_forward  # Explicit cleanup on success
}

# At end of script (before main)
trap 'cleanup_port_forward' EXIT
trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"
```

## Anti-Patterns to Avoid
- Embedding configuration in scripts
- Using `wget` instead of `curl`
- Manual `kubectl exec` for API calls when CLI tools exist
- Hardcoded timeouts without configurability
- Missing cleanup in error scenarios
- Reverting user's manual configuration changes
- Creating files without validating they don't exist
- Using `latest` tags in production configurations
- Skipping validation steps that could fail silently
- Hardcoding image names/tags in arrays - extract dynamically or use variables
- Duplicating install scripts for same components - use shared parameterized scripts
- Exposing unnecessary ingress (e.g., k1 SPIRE doesn't need ingress)

## When Making Changes
1. Always run full end-to-end tests after modifications
2. Update documentation and examples to reflect changes
3. Ensure backward compatibility where possible
4. Add appropriate error handling and logging
5. Check that configuration changes are applied via scripts, not manually
6. Only manually query cluster state for debugging and validation. Do not make changes to cluster state and then continue without updating the deployment scripts. Everything must be repeatable.
7. Prefer make -j to make for faster deployments unless debugging something that requires serial logging output.

## More Rules
- Keep documentation concise. It should be easy to skim and get all information needed. Do not add unneeded info.
- Avoid marketing language in documentation (e.g., "Zero-Trust Architecture", "Production-Ready"). Keep it factual and technical.
- No emojis, excessive exclamation marks, or overly enthusiastic language in code, documentation, scripts, or output messages. Keep tone professional and neutral.
- Update AGENTS.md as needed when given general directives.
- ALWAYS update AGENTS.md immediately when receiving general directives - do not forget this step.
- Prefer to keep temporary data in memory rather than as temp files. Temp files can leave leftovers if the scripts exit with error.
- Batch multiple tool calls together when possible. Chain commands with `&&` to reduce back-and-forth. Read multiple files in parallel when needed.
- **PREFER TOOL CALLS OVER SHELL COMMANDS**: Use the `grep` tool call instead of `grep` in shell, use `read_file` instead of `cat`, use file editing tools instead of `sed`. Only use shell commands when no appropriate tool exists or when executing actual deployment/installation operations.
- Use project relative paths. Files inside the project are blocked from being read with absolute paths.

## File Movement and Reference Validation
- When moving, renaming, or deleting files, ALWAYS validate that all references are updated:
  1. Use `grep` to search for old file names/paths across the entire codebase
  2. Check documentation files (README.md, scripts' help text, comments)
  3. Check configuration files (Makefile, Dockerfile, YAML files)
  4. Check script internal references and cross-references
  5. Update import statements, include paths, and relative references
  6. Test that the changes work end-to-end before considering the task complete
- This validation is MANDATORY and must be done systematically, not assumed
