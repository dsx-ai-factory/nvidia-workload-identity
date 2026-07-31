# Security Policy: DSX Identity (dsx-identity)

NVIDIA is dedicated to the security and trust of its software products and services, including source code repositories managed through NVIDIA organizations.

If you discover a potential security vulnerability in DSX Identity, please report it privately through one of the channels below. Do not open a public issue, merge request, discussion, or public comment for security vulnerabilities.

## Reporting a Vulnerability

Use one of the following private reporting channels:

- **NVIDIA Vulnerability Disclosure Program (preferred):** https://www.nvidia.com/en-us/security/
- **Email:** [psirt@nvidia.com](mailto:psirt@nvidia.com)
  - We encourage use of the [NVIDIA public PGP key](https://www.nvidia.com/en-us/security/pgp-key) for secure email communication.
- **GitLab/GitHub Private Vulnerability Reporting:** Use this repository's **Security** tab and **Report a vulnerability** flow when available.

Please include the following information:

- Product/project name and affected branch, commit, chart version, or container image tag.
- Component affected, such as `dsx-identity-keys`, `metadata-service`, `dsx-token-fetcher`, `dsx_imds`, `dpu_hw`, `dsx-identity-controller`, the Helm chart, or a script under `scripts/`.
- Vulnerability type, such as authentication bypass, token disclosure, key distribution issue, privilege escalation, denial of service, misregistration, injection, or insecure deployment default.
- Step-by-step reproduction instructions.
- Proof-of-concept code or request examples, if available.
- Impact assessment, including which identity path is affected: DPU, host, Kubernetes Pod, KubeVirt VM, Event Bus public-key synchronization, or SPIRE registration.

Detailed reports help NVIDIA evaluate and address issues faster. NVIDIA PSIRT will acknowledge receipt, validate severity, coordinate remediation, and publish security bulletins as appropriate.

## Security Architecture & Context

DSX Identity turns shard-local SPIRE JWT-SVIDs into DSX JWTs that relying parties can validate across shards. An identity shard contains an independent SPIRE deployment and a co-located DSX Identity Keys deployment. DSX Identity Keys validates only local SPIRE JWT-SVIDs, signs DSX JWTs, publishes public signing keys through the external DSX Event Bus, and serves an aggregate JWKS for relying parties.

This repository is primarily a Go service and Kubernetes Helm chart repository. It contains multiple security-sensitive binaries:

- `cmd/dsx-identity-keys`: HTTP token-exchange service, OIDC discovery endpoint, JWKS endpoint, local signing-key loader, and NATS JetStream KV public-key synchronizer.
- `cmd/dsx-token-fetcher`: sidecar or DPU process helper that fetches SPIRE JWT-SVIDs from the SPIRE Workload API, exchanges them for DSX JWTs, and writes tokens to a configured file path.
- `cmd/metadata-service`: tokenless metadata service for host and VM identity flows, serving `/latest/meta-data/identity` and `/2009-04-04/meta-data/identity`.
- `cmd/dsx-imds-node-attestor-agent` and `cmd/dsx-imds-node-attestor-server`: SPIRE NodeAttestor plugins that bootstrap host agents using DSX JWTs obtained through IMDS.
- `cmd/dsx-dpu-node-attestor-server` and `internal/dpu`: SPIRE NodeAttestor plugin for DPU node attestation.
- `cmd/dsx-identity-controller` and `internal/identitycontroller`: Kubernetes controller that derives SPIRE `ClusterStaticEntry` resources from DPU and LaunchLayer source resources.
- `cmd/dsx-identity-keys-proxy-csi`: CSI node plugin exposing a local socket path used by VM metadata sidecars to reach DSX Identity Keys.
- `charts/dsx-identity`: Helm chart for DSX Identity Keys, SPIRE integration, identity injection policies, VM support, observability, service accounts, and deployment security contexts.

Primary security responsibilities:

- Preserve the integrity of DSX JWT issuance from already-validated local SPIRE JWT-SVIDs.
- Preserve tenant, resource type, resource ID, workload, and role identity semantics while converting local SPIFFE IDs to DSX SPIFFE IDs.
- Protect signing key material and active key selection.
- Distribute only public signing keys across shards.
- Ensure SPIRE registrations derived by the identity controller are deterministic, non-conflicting, and scoped to the correct parent agent.
- Expose host and VM identity only through the intended metadata and CSI paths.
- Keep Workload API sockets, token files, and proxy sockets available only to intended local workloads.

**Repository Exposure Classification:** Internal.
Basis: origin remote is hosted on `gitlab-master.nvidia.com`, an internal/self-managed GitLab host.

**Service Exposure Classification:** Internal-Sensitive (high confidence).
Basis: confirmed by requesting user; repository implements identity issuance, SPIRE node attestation, signing-key handling, Kubernetes admission/injection, NATS public-key synchronization, and token delivery for internal DSX infrastructure.

Key trust boundaries and interfaces:

- Local SPIRE Workload API sockets are trusted sources for SPIRE JWT-SVIDs. DSX Identity Keys validates `subject_token` values with `jwtsvid.ParseAndValidate` in `cmd/dsx-identity-keys/main.go` before signing DSX JWTs.
- DSX Identity Keys exposes HTTP endpoints: `POST /v1/exchange`, `GET /.well-known/jwks.json`, `GET /.well-known/openid-configuration`, `GET /live`, and `GET /ready`.
- DSX IMDS exposes link-local metadata endpoints through `cmd/metadata-service/main.go` and requires direct requests with `Metadata: true`; it rejects forwarded requests and constrains response hop limit with a TTL/hop-limit listener.
- DSX public signing keys are shared through NATS JetStream KV in `cmd/dsx-identity-keys/nats.go`; the payload is validated as public JWK material before entering the aggregate registry.
- NATS OAuth2 mode reads client credentials from a mounted secret file in `cmd/dsx-identity-keys/nats_auth.go`.
- Signing keysets and active KID documents are read from mounted secrets in `cmd/dsx-identity-keys/keyset.go`.
- The identity controller writes SPIRE `ClusterStaticEntry` resources with server-side apply and validates duplicate SPIFFE IDs and overlapping VM holder selectors in `internal/identitycontroller/reconciler.go`.
- Kyverno policies under `charts/dsx-identity/templates` inject token fetchers and constrain identity-related Pod shapes. README guidance identifies Kyverno as the admission boundary.
- TLS for DSX Identity Keys client access is expected to be terminated by the external Gateway according to the README; local fixtures may use direct HTTP endpoints.

### Threat Model

The following scenarios represent the primary security concerns for this project, ordered by likely security impact and grounded in repository components.

1. **Token Exchange Trust Expansion Through DSX Identity Keys:** `cmd/dsx-identity-keys/main.go` accepts `POST /v1/exchange`, validates local SPIRE JWT-SVIDs, converts their subjects with `internal/identity/identity.go`, and preserves validated claims except `sub` and `iss`. A bug in subject parsing, tenant validation, audience handling, or claim preservation could mint a DSX JWT that gives a workload broader cross-shard authority than its local SPIRE identity should allow.

2. **Event Bus Public Key Poisoning or Staleness:** `cmd/dsx-identity-keys/nats.go` publishes local public signing keys and watches every `shard.*.kid.*` key in the configured NATS KV bucket. Although received keys are validated as public JWKs, the shared Event Bus, bucket access policy, shard IDs, key IDs, TTLs, and OAuth2 configuration remain security-critical. Misconfigured NATS authorization, unauthorized writers, replayed stale keys, or shard ID collisions could affect the aggregate JWKS served to relying parties.

3. **Metadata Service Identity Disclosure Through IMDS Reachability:** `cmd/metadata-service/main.go` serves DSX JWTs from `/latest/meta-data/identity` and `/2009-04-04/meta-data/identity` when requests include `Metadata: true` and are not forwarded. If link-local routing, sidecar placement, network namespace isolation, or the external NICo/Carbide IMDS path allows unintended callers to reach the service directly, the metadata endpoint can return DSX JWTs for the selected host or VM SVID.

4. **DPU Node Attestation With Plaintext Serial Evidence:** `internal/dpu/evidence.go` currently provides `PlaintextVerifier`, and `cmd/dsx-dpu-node-attestor-server/main.go` wires `dpu.New()` as the default server plugin. The verifier treats the attestation payload as the asserted DPU serial after length and format checks. Production deployments must replace or wrap this development-oriented evidence path with cryptographic DPU evidence validation, otherwise possession of the attestation channel and a known serial can influence DPU agent identity.

5. **Controller-Derived SPIRE Entry Misregistration:** `internal/identitycontroller/reconciler.go` derives SPIRE `ClusterStaticEntry` objects from DPU and LaunchLayer sources, validates holder selectors, detects duplicate SPIFFE IDs, and forces ownership of derived fields. A bug in source parsing, parent ID derivation, conflict detection, or Kyverno/RBAC boundaries could register a host, VM, DPU, or Pod identity under the wrong tenant, parent agent, role, or selector set.

6. **Token File Exposure From Sidecar Delivery:** `cmd/dsx-token-fetcher/main.go` writes exchanged DSX JWTs to a configured output path and sets temporary token files to mode `0644` before atomic rename. The Helm injection policy mounts the token volume read-only into application containers, but token confidentiality relies on Pod-private volume semantics, consumer container isolation, namespace policy, and the configured output path. An overly broad mount, privileged consumer container, or shared host path could expose bearer DSX JWTs.

7. **Observability and Diagnostic Data Leakage:** First-party processes log identity decisions and values such as SPIFFE IDs, audiences, endpoints, token lengths, shard IDs, KIDs, and remote addresses. README states structured allow, deny, and dependency events never include credentials, but telemetry exporters, log retention, and debug-level configuration can still reveal sensitive topology, tenant, or identity metadata if handled outside the expected observability boundary.

### Critical Security Assumptions

- External Gateway TLS termination and source-aware quotas are correctly configured for DSX Identity Keys. The service code starts an HTTP listener and does not configure TLS in `cmd/dsx-identity-keys/main.go`; README states clients use the Gateway HTTPS endpoint while local fixtures may use direct HTTP.
- SPIRE Workload API sockets are protected by SPIFFE CSI, filesystem permissions, Pod security contexts, and node-local isolation. DSX Identity Keys, DSX Token Fetcher, and metadata-service trust JWT-SVIDs obtained from those sockets.
- The external Event Bus enforces writer identity, bucket isolation, OAuth2 policy, and transport security. The repository validates received public JWK shape but does not itself own Event Bus authorization policy.
- Signing keyset, active KID, and NATS OAuth2 credentials are provisioned by external secret automation and mounted only into intended Pods. The Helm chart mounts these Kubernetes Secrets read-only, but this repository does not create or rotate the underlying secrets.
- The configured active signing algorithm and key material are generated and rotated by a trusted upstream key-management process. `cmd/dsx-identity-keys/keyset.go` validates algorithm compatibility and duplicate KIDs but does not verify the provenance of mounted private key material.
- Kyverno admission, Kubernetes RBAC, and chart deployment subjects enforce identity injection and registration boundaries. README identifies Kyverno as the admission boundary and the controller as the sole writer for derived static entries.
- External DPU, NICo, Carbide IMDS, LaunchLayer, and worker-to-DPU bootstrap integrations provide correct source data and routing. The README explicitly places some bootstrap registrations and external DPU/host mapping outside this repository.
- Production DPU attestation uses cryptographic evidence validation rather than the development-oriented plaintext serial verifier. The default `PlaintextVerifier` in `internal/dpu/evidence.go` is suitable for development wiring only.
- Relying parties validate DSX JWT signatures against the aggregate JWKS, validate issuer and audience, enforce token expiry, and apply their own authorization policy. DSX Identity Keys issues bearer JWTs but does not enforce relying-party authorization decisions.
- Consumer workloads protect DSX JWT files as bearer credentials. The token fetcher removes stale tokens and refreshes on expiry, but it does not encrypt tokens at rest in the shared Pod volume.
- Kubernetes and container runtime isolation prevent unintended access to token files, proxy sockets, Workload API sockets, and mounted secrets. The repository uses non-root, read-only-root-filesystem security contexts for chart-owned services where applicable, but it cannot enforce every consumer container setting.
- The local E2E environment is not a production security configuration. README describes unauthenticated NATS and direct HTTP fixture endpoints for local Kind clusters.

## Dependency Security

Security-relevant dependencies include:

- SPIFFE and SPIRE libraries and plugin SDKs: `github.com/spiffe/go-spiffe/v2`, `github.com/spiffe/spire-controller-manager`, and `github.com/spiffe/spire-plugin-sdk`.
- JOSE, OIDC, and OAuth2 libraries: `github.com/go-jose/go-jose/v4`, `github.com/coreos/go-oidc/v3`, and `golang.org/x/oauth2`.
- NATS and JetStream KV libraries: `github.com/nats-io/nats.go` and `github.com/nats-io/nats-server/v2`.
- Kubernetes controller and API libraries: `sigs.k8s.io/controller-runtime`, `k8s.io/client-go`, and `k8s.io/apimachinery`.
- Networking and node setup libraries: `github.com/google/nftables`, `github.com/vishvananda/netlink`, and `golang.org/x/sys`.
- OpenTelemetry libraries for logs, traces, metrics, and HTTP/gRPC instrumentation.

Keep Go modules, base images, Helm dependencies, and charted sidecar images updated through the repository validation workflow. Review dependency changes for effects on JWT verification behavior, accepted signing algorithms, OIDC discovery, NATS reconnection/watch semantics, Kubernetes controller ownership semantics, and SPIRE plugin APIs.

## Deployment Assumptions

- Install `charts/dsx-identity` once per identity shard.
- Provide `global.operatorTenant`, SPIRE trust domain, cluster name, shard ID, public JWKS URL, NATS server URL, signing keyset Secret, active KID Secret, and any NATS OAuth2 credentials before starting DSX Identity Keys.
- Use the external Gateway for TLS termination and request quotas in production.
- Enable NATS OAuth2 or an equivalent Event Bus access-control mode in production.
- Keep local fixture settings, unauthenticated NATS, direct HTTP endpoints, and example values out of production deployments.
- Apply least-privilege service accounts, Pod Security admission, read-only secret mounts, image pull secret scoping, and namespace selectors consistent with the Helm chart and README identity contract.
- Treat DSX JWTs, SPIRE JWT-SVIDs, signing keysets, OAuth2 credentials, Workload API sockets, and proxy sockets as sensitive security assets.
