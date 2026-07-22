# DSX Identity Repository Rules

## Tool and file safety

- Preserve unrelated user changes and never use destructive Git or filesystem commands without explicit authorization.
- Maintain an active FIFO task list during review loops. Finish the current item before starting later requests unless the user explicitly reprioritizes them.
- Use project-relative paths, `rg` for searches, and file-editing tools for edits.
- After moving, renaming, or deleting anything, search the whole repository for stale references in code, configuration, scripts, and documentation, then run the relevant tests.
- Keep scripts idempotent. Trap and clean up port-forwards, child processes, and temporary files on success, failure, and signals. Prefer in-memory temporary data.
- Prefer idempotent operations, immutable data, atomic swaps, and atomic values. Keep mutation local to one owner when state is required.
- Nest each mutex with only the mutable fields it protects so the lock boundary is visible from the struct definition.
- Prefer standard-library `slices` and `maps` transformations when they make ownership or mutation clearer. Keep explicit loops for ordered I/O, early errors, and state transitions.
- Keep configuration in files rather than embedding it in scripts. Use `.tmpl` only when placeholders require rendering.
- Prefer existing repository code, the Go standard library, imported libraries, native Kubernetes/Helm features, maintained sidecars/charts, and OSS APIs before custom code or wrappers.
- Keep implementation and validation proportional to the external contract. Delete redundant fixtures, orchestration, and test scenarios before adding abstractions or runtime resources.
- Centralize shared parsing and validation contracts. Do not copy equivalent URL validation between components.
- Let owning clients parse configured dependency URLs. The URL scheme selects the transport. Do not add plaintext override flags or shared URL-policy helpers.
- Add compatibility code only when a released contract requires it.
- Add concise comments for every non-obvious behavior. Explain why code or configuration needs an ordering, ownership rule, security check, workaround, or unusual setting. Do not narrate self-explanatory syntax.
- Keep documentation factual and concise, without marketing language or decorative tone. Keep README topology and request-flow diagrams current.
- Plans record work and verification, not architecture. Record each intentional design decision in the closest durable README, diagram, configuration comment, or code comment.
- Keep one primary explanation for each decision. Link to it when needed instead of repeating it, and do not turn design documentation into walls of text.
- Do not use em dashes, en dashes, curly quotes, or semicolons in prose or comments. Use plain punctuation.
- Do not trim, normalize, case-fold, or coerce supplied input. Documented shell defaults may treat unset and empty values alike.
- Names must describe the current role and ownership. Remove stale temporary, legacy, and false component names instead of explaining them.
- Do not name data holders `state`. Name synchronized groups, snapshots, and aggregates for the resource or status they contain.

## Git and merge requests

- Before creating or updating a merge request, read the repository's current
  GitLab CI configuration and its pinned shared templates for commitlint,
  semantic release, and MR policy. Do not rely on generic tool defaults or
  branch history as a substitute for the active rules.
- Name release-producing branches with either the `feat/` or `fix/` prefix.
  Use no other semantic-release branch type.
- Use a Conventional Commits title recognized by semantic release for every
  release-producing merge request. Use only `feat` for new capabilities or
  `fix` for corrections, performance, reliability, and maintenance work.
- Include a Jira reference or `NO-REF` or `NO_REF` in every merge request title
  or description, as required by MR Cop.
- Validate every commit from the merge base with the repository's commitlint
  policy before pushing. Wrap commit body lines at 72 characters and never
  exceed commitlint's 100-character limit.
- Before submitting the merge request, verify the branch name, title,
  description, complete commit range, and clean worktree against these rules.
- Diagram actors must name the concrete form, such as DPU process, bare-metal host, Kubernetes Pod, sidecar, or KubeVirt VM. Distinguish cluster-local SPIRE JWT-SVIDs from exchanged DSX JWTs. Do not use `workload` or `raw JWT-SVID` as standalone labels. When documenting a plugin trust path, identify both custom NodeAttestor halves and the relevant WorkloadAttestor.
- Organize topology by shard so source CRs and concrete consumers converge on the same identity shard. Keep each diagram to one purpose, render it, and visually inspect it before committing.
- Update this file immediately when the user gives a general repository directive.

## Authoritative topology

- k1 and k2 each run independent SPIRE and a co-located DSX Identity Keys deployment. There is no SPIRE federation.
- Common Services and Data Hall are deployment placement labels only. They do not define parent, group, or trust-set hierarchy in the key propagation protocol. Cluster names `k1` and `k2` describe only the local test topology.
- DPUs are external hardware boundaries, not Kubernetes worker nodes. Each DPU runs a SPIRE agent that attests through `dsx_dpu` using its serial number or SPDM and receives the workload identities registered beneath that agent.
- Every Kubernetes worker node is an operator-owned bare-metal host. Tenant-facing bare-metal hosts are standalone LaunchLayer and NICo resources and never join Kubernetes as worker nodes.
- Every DPU and every Kubernetes Pod is operator-owned. The only tenant-owned resources are standalone NICo-managed bare-metal hosts and KubeVirt VMs, both declared by LaunchLayer. There are no tenant-owned Pods, Kubernetes workers, or DPUs.
- KubeVirt VMs run on operator-owned bare-metal workers. The virt-launcher Pod is an operator Pod, while the VM identity uses the tenant supplied dynamically by its LaunchLayer source CR.
- Every identity shard configures its own internal SPIRE trust domain and cluster name.
- DSX Identity Keys public-key propagation is flat. Every instance in one Event Bus account uses a globally unique shard ID, stores only its local public keys in one shared NATS KV bucket at `shard.<shard-id>.kid.<kid>`, watches the complete bucket, retains all valid public keys by globally unique `kid`, and serves the aggregate JWKS.
- Do not add bundle endpoints, federated trust, federation CA synchronization, or federation-only routes and certificates.
- Each DSX Identity Keys instance accepts only SPIRE JWT-SVIDs issued by its local SPIRE and rejects SPIRE JWT-SVIDs issued by every nonlocal shard.
- The DSX Event Bus and OAuth2 provider are external to the identity chart. Data Hall shards do not deploy either component. Local E2E uses one unauthenticated NATS server with in-memory JetStream for KV and does not deploy DSX Exchange or an OAuth2 provider.
- DSX Identity Keys supports unauthenticated NATS and OAuth2 client credentials using the DSX Exchange native NATS token-auth login shape. OAuth mode discovers the token endpoint from the configured issuer and passes the access token through `nats.TokenHandler`. It does not support basic authentication. Production DSX Exchange deployments place DSX Identity Keys in a dedicated NATS account, whose account boundary provides isolation.
- DSX Identity Keys uses the NATS KV library. Every instance starts an updates-only watch before loading `History`, so concurrent updates queue while current values reconstruct the aggregate. If the watcher closes, its replacement follows the same watch-before-history order. Each instance republishes only its local keys every quarter of the bucket TTL. KV expiry removes keys after every replica for their shard stops refreshing them.
- Run two active DSX Identity Keys replicas per shard. Rolling updates and Pod disruption must keep one replica available.
- Expose only the NATS client endpoint required by local DSX Identity Keys instances in the other E2E cluster.
- Kyverno is external. Its DSX identity policies belong in the identity chart. The Kyverno runtime does not.
- Relying parties use the aggregate JWKS from any reachable DSX Identity Keys instance. Repository validation deploys OpenBao in k1 only for convenience.
- Cross-shard trust exists only through exchanged DSX tokens and propagated public keys.

## Helm and deployment

- `charts/dsx-identity` is the single identity chart installed in each shard.
- Depend directly on the official SPIRE chart. Template repository-owned DSX Identity Keys, controller, metadata, CSI, and policy resources directly. Configure node-attestor plugins through SPIRE values. Do not create separately packaged local wrapper charts.
- Install the official SPIRE CRD chart first where its APIs are required.
- DSX Event Bus, Keycloak, Kyverno runtime, cluster infrastructure, OpenBao, nginx, VMs, and E2E clients remain external to the identity chart.
- Vault Secrets Operator and all Vault or OpenBao secret-source resources remain external to the identity chart. The chart only references the resulting Kubernetes Secrets.
- CI packages and publishes only artifacts the repository actually produces: the identity chart and current images.
- Apply every resource in an admission-compatible deployment phase before readiness waits. Split phases only to establish an API or admission webhook required to accept later resources.
- Put each required API, admission, or infrastructure status gate in its provider Skaffold config's deploy after-hook. Every direct consumer must require that provider. Do not rely on config traversal order or a designated first consumer.
- Deploy after-hooks intentionally wait for the required provider config's resources before checking its explicit contract. Keep each provider config cohesive instead of disabling status checks to avoid a real dependency.
- Never wait for a workload and then discover its Pod UID, node, placement, or service account to synthesize another deployment object. Use stable declarative source CRs and selectors known before startup.
- Use Helm values for deployment configuration and command-line overrides only for genuinely dynamic values.
- Do not infer or prescribe external deployment choices such as SPIRE datastore topology. Pass through official chart values unless DSX Identity owns the contract.
- Prefer upstream and Kubernetes defaults. Keep a nondefault chart or Pod setting only when a DSX contract or local fixture requires it, and document the reason next to the override.
- Let Skaffold tolerate transient Kubernetes failures until each deployment deadline. Keep explicit provider gates only where an API, admission, or infrastructure contract must hold before a dependent config can be applied.
- Use `kubectl rollout status` for controllers and `kubectl wait` for other resources. Avoid arbitrary sleeps.
- The local k2 fixture uses OVN-Kubernetes layer-3 UDN and KubeVirt with a cached `containerDisk` image. WSL is unsupported because the metadata bridge requires nftables.
- Local E2E installs each identity chart once. Its normal DSX Identity Keys Pod runs on the Phase 0 seed node while the worker `dsx_imds` agent attests. Do not add a staged DSX Identity Keys registration, bootstrap-specific DSX Identity Keys identity, or second Helm release.
- Use CSI drivers for workload identity delivery, not hostPath mounts.

## Identity contract

```text
spiffe://<tenant>.ds9.dsx.nvid.id/<dpu|host|vm>/<id>
  [/process/<name>][/role/<role>]
spiffe://<operator-tenant>.ds9.dsx.nvid.id/host/<id>/pod/<uuid>
  [/role/<role>]
```

- Service is `dsx`, partition is `nvid.id`, and the current region is `ds9`.
- Use DPU `metadata.name` as the DPU ID. Model a DPU as `/dpu/<id>`, never as a process.
- A `/process/<name>` suffix describes identity semantics only. It does not prove which executable called the Workload API and must not be treated as a security boundary.
- Every Kubernetes Pod uses `service=dsx`. Derive its tenant from the configured operator tenant and its host ID from the Kubernetes node. Do not model Pods as tenant-owned resources.
- SPIRE Pod paths use `/tenant/<operator-tenant>/service/dsx/host/<node-name>/pod/<uuid>`.
- Every Kubernetes Pod registration is parented to the `dsx_imds` agent for its operator-owned worker host. DPU agents and standalone tenant-host agents do not parent Kubernetes Pod registrations.

## DSX Identity Keys and key propagation

- Keep the existing unauthenticated RFC 8693 `/v1/exchange` HTTP contract. Do not add mTLS, endpoint authentication, sender constraints, or another audience form field.
- Validate the submitted local SPIRE JWT-SVID. Preserve validated claims except for the existing `sub`, `iss`, and signature transformation.
- Issue with `spiffe://ds9.dsx.nvid.id`.
- The same configured operator tenant covers operator DPUs, operator Pods, and operator bare-metal hosts. Enforce that ownership when SPIRE registrations are created. A standalone tenant-facing host subject instead carries the dynamic tenant ID supplied by its LaunchLayer source CR. Kubernetes Pod subjects use the host resource type and Kubernetes node name.
- DSX Identity Keys is tenant-agnostic. It must not receive, configure, distinguish, authorize, or otherwise depend on the operator tenant or any individual tenant. It verifies locally issued SPIRE JWT-SVIDs, validates the common subject grammar, preserves the tenant already present in the subject, and changes only the subject and issuer encoding before signing the DSX JWT.
- Each shard loads a private signing-keyset Secret containing `keyset.json` and a separate active-keyset Secret containing `active-kid.json` as `{"active_kid":"<kid>","alg":"<JOSE algorithm>"}`. Every tenant in that shard uses them. External secret automation requests the Transit signing-key export with `format=pem` and renders its versioned private keys into the keyset array. DSX Identity Keys derives each `kid` from its public JWK thumbprint.
- Store each public JWK at `shard.<shard-id>.kid.<kid>` in the shared NATS KV bucket and store the aggregate only by globally unique `kid`. Refresh each entry often enough for at least three further writes before the bucket TTL expires it.
- Every `kid` is globally unique and never reused. Derive local IDs from the go-jose SHA-256 public JWK thumbprint and preserve IDs received in valid remote KV entries.
- Apply the active keyset's configured signing algorithm to every local key and validate that it matches the key material. Discovery must advertise the algorithms present in the aggregate JWKS.
- Validate `kid` uniqueness within each local signing keyset. Accepted KV entries replace the globally unique `kid` they address.
- Rotation adds the new Transit version to the keyset while the old kid remains active, waits for public-key propagation, then changes the active-kid Secret. After old JWTs expire, advancing the Transit minimum valid version removes the old key, and its KV entry expires.

## Controller and metadata

- Run one DSX Identity Controller with each identity shard in the regular management cluster that owns its source CRs. The Common Services shard covers Common Services and Data Hall control-plane resources. A Data Hall shard covers that Data Hall's capacity resources.
- The only source inputs are read-only unstructured DPU and LaunchLayer objects. The controller also watches the `ClusterStaticEntry` objects it manages for drift.
- Every Kubernetes Node is an operator-owned worker host. This invariant is not a controller source or a configured inventory. Do not add an `operatorHosts` value, an operator-host flag, or a preloaded node-to-DPU mapping.
- Every DPU source is operator-owned. Only a standalone NICo-managed host or KubeVirt VM sourced by LaunchLayer is tenant-owned.
- Physical worker-to-DPU pairing is part of the external NICo and DPU integration boundary, not Identity Controller configuration.
- Until LaunchLayer publishes its final API, keep the hard-coded namespaced `LaunchLayerSourceFixture` as the temporary read-only source contract. Limit it to `type`, `tenantID`, `resourceID`, `parentID`, optional `role`, and `holder` namespace, service account, and optional Pod UID. Do not add `parentTenantID`, complete SPIFFE IDs, parent SPIFFE IDs, selectors, hints, class names, source finalizers, a configurable GVK, or compatibility code. Install the temporary CRD outside the identity chart.
- Derive DPU, host, and VM entries, parents, selectors, hints, and optional `/role/<role>` suffixes in the controller. Source CRs must not contain complete SPIRE registration entries.
- The separately supplied `dsx_dpu` plugin and DPU SPIRE agent are external hardware integration boundaries. The local environment emulates their SPIRE contracts and does not model a DPU as a Kubernetes node.
- The configured operator tenant applies to operator DPUs, operator Pods, and every bare-metal Kubernetes worker host. Configure it in YAML. Those worker hosts obtain operator-tenant DSX identities through IMDS. An operator Pod subject uses its worker host ID followed by `/pod/<uuid>`.
- Kubernetes Pod registrations use the official SPIRE controller-manager and produce the configured operator tenant, node host ID, Pod UID, and `dsx-pod` hint. The current identity chart runs one controller-manager. Its parent template always identifies the operator-tenant `dsx_imds` agent for the worker node. Do not add per-Node agent-type or tenant association metadata.
- A LaunchLayer host source supplies the tenant ID for a standalone tenant-facing bare-metal host that is never a Kubernetes worker. A LaunchLayer VM source supplies the tenant ID only for the VM identity. Its VM entry is parented to the operator-tenant `dsx_imds` agent for the operator-owned worker host running the virt-launcher Pod.
- LaunchLayer host and VM sources may supply an optional role for the derived identity. Holder selectors remain VM-only.
- The base DPU entry is parented to the `dsx_dpu` agent. For token-file delivery, its Unix WorkloadAttestor selectors identify the `dsx-token-fetcher` process because that process opens the Workload API socket. The protected token file is the capability consumed by the DPU process. DSX IMDS on the paired DPU separately selects the bare-metal host entry through the external DPU runtime's workload-attestation contract and the `dsx-imds` hint. Do not use one selector set for both registrations.
- Run the DPU token fetcher under a dedicated OS account. Prefer Unix UID, executable path, and packaged-binary SHA-256 selectors. Path and SHA-256 selectors require the external DPU SPIRE agent to enable Unix workload path discovery. Keep the executable root-owned and immutable.
- Do not use the Delegated Identity API or PID delegation for DPU token-file delivery. The ordinary Workload API attests its caller, so a separate fetcher cannot claim selectors belonging to the consuming DPU process.
- `dsx_imds` is the shared host/VM contract. Both NodeAttestor sides live in this repository.
- `dsx-token-fetcher` is the shared token-file delivery implementation. Kubernetes Pods run it as a sidecar. An external DPU supervisor runs the same binary as a local process outside the identity chart. Host-process-specific registration and delivery are outside the current remediation scope and require a concrete consumer and selector contract before they are added. Do not document a separate Local DSX Identity Keys Helper or DSX Identity Keys Sidecar component.
- DSX IMDS is the `metadata-service` implementation shared by NICo or Carbide bare-metal delivery and KubeVirt VM delivery. Do not confuse it with the `dsx_imds` NodeAttestor contract.
- DSX IMDS configures one `--identity-keys-url`, and its scheme selects direct HTTP or DSX Identity Keys Proxy CSI over a Unix socket. The deployment topology chooses the transport independently of the SVID hint. Do not enforce a hint-to-transport pairing or add a helper proxy or separate metadata binary.
- A NICo or Carbide bare-metal host reaches link-local IMDS through its paired DPU intercept. NICo or Carbide IMDS passes the identity request to DSX IMDS, which selects and exchanges the host SVID. The host SPIRE agent consumes the returned DSX JWT through `dsx_imds` for node attestation and serves host processes.
- Support only tokenless GET at `/latest/meta-data/identity?aud=<audience>` and `/2009-04-04/meta-data/identity?aud=<audience>`. The `aud` parameter may repeat to request additional audiences.
- Metadata responses support `application/json` and `text/plain`. An absent `Accept` header defaults to JSON. A present header with no acceptable supported media type returns HTTP 406.
- Require `Metadata: true`, reject proxy headers, and do not add an IMDS session-token PUT endpoint.
- Serve metadata responses with IPv4 TTL 1 and IPv6 hop limit 1.
- VM identity comes from the `dsx_imds`-attested agent and the real `metadata-service`, SPIRE CSI, and RFC 8693 path.
- The operator-owned virt-launcher Pod and tenant-owned VM identities use the operator worker host's Workload API. `metadata-service` selects the tenant-facing VM SVID by its exact `dsx-imds-vm` hint. Do not describe them as separate SPIRE agent paths.
- Data Hall KubeVirt metadata sidecars reach their local DSX Identity Keys through DSX Identity Keys Proxy CSI. Do not remove or bypass the proxy.

## OpenBao

- Use `/v1/auth/jwt/cel/login` with the full aggregate JWKS from any reachable DSX Identity Keys instance. Local E2E uses k1 DSX Identity Keys.
- CEL validates the full subject grammar and extracts tenant, region, service, resource type, resource ID, and optional role for policy evaluation.
- Policies remain narrowly scoped to authenticated, region, tenant, service, and resource-type identities.
- Use the official OpenBao chart with native self-initialization and expose it through the k1 Gateway. Do not manage init keys or a root token, and do not use port-forwards.

## Engineering rules

- Use the repository-pinned mise toolchain. Local and CI E2E must enter the same Skaffold workflow. Keep release image and chart publishing in the GitLab release pipeline.
- Use one Skaffold command with per-module kubecontexts to deploy the full k1 and k2 identity topology. Use native Skaffold Kind image loading for source images. Use the persistent local Zot registry only as an upstream pull-through cache.
- Reuse the local Zot container and volume across setup and cleanup. Do not recreate or delete them during normal workflows.
- Preserve Zot's running state across workflows. Mount its checked-in config read-only instead of copying or rendering it.
- Each deployment workflow invokes one root Skaffold config. Make may prepare Kind clusters, but it must not select, order, or invoke component Skaffold modules.
- Use `#!/bin/bash`, `set -euo pipefail` where appropriate, `curl` rather than `wget`, and logging helpers from `scripts/common.sh`.
- Build unreleased Go images locally with multi-stage Dockerfiles and minimal runtimes. Pin image tags. Never use `latest`.
- Give containers resource requests, limits, and appropriate security contexts.
- Use Go 1.26+ and the repository's pinned `go-spiffe`. Follow standard formatting, context cancellation, structured logging, flag-based CLI configuration, and focused unit tests for business logic.
- Use slog with JSON handlers for first-party Go logging. Configure application logging with `--log-level` and initialize logging and OpenTelemetry through the shared telemetry package. Do not use Zap.
- Use the DSX OTel Operator SDK injection as the shared configuration mechanism for chart-owned applications and repository-owned SPIRE plugin hosts. Do not require operators to duplicate standard OTel configuration under SPIRE values.
- Shared chart settings must apply to every in-scope component. Do not describe a setting as global or shared when a component hard-codes an exception.
- Comment non-obvious Helm value precedence, conditional rendering, and cross-resource contracts next to the relevant value or template. Explain why the structure exists without narrating YAML syntax.
- Keep README prose focused on architecture and operator intent. Put concrete configuration keys and values in concise YAML examples, not prose. Omit ports and implementation details unless they are part of an external contract.
- Do not use meta-documentation such as "this README records" or "this section documents." State the concrete behavior, constraint, or relationship instead. Use descriptive links without narrating that the linked file contains documentation.
- Preserve useful README diagrams and runnable examples when shortening prose. Do not replace concrete operator guidance with vague summaries.
- Write the README for readers who understand SPIFFE and Kubernetes but have no prior DSX Identity context. Introduce DSX-specific components and trust paths before their configuration.
- Describe the real deployed architecture first in all documentation. Keep Kind clusters, k1 and k2 fixture names, E2E-only components, and local workarounds in explicit local E2E sections after the deployed architecture and contracts.
- Show DPUs, bare-metal hosts, and KubeVirt VMs as first-class actors in the README architecture overview. Keep attestor names such as `k8s_psat` in lower-level identity delivery and trust-path documentation.
- Call `charts/dsx-identity` the identity chart. Do not call it an umbrella.
- Use `http.Header` methods instead of direct header-map access. Parse structured header values with the relevant standard-library grammar.
- Never call `os.Exit` outside `main`. Put command work and deferred cleanup in an error-returning function so `main` translates failure to `os.Exit` only after those defers run.
- Use two-space YAML indentation, descriptive labels, ConfigMaps/Secrets for configuration, and least-privilege service accounts.
- Keep policies in separate `.hcl` files and cluster-specific resources beside their deployment scripts.
- Query live clusters manually only for diagnosis and validation. Any required state change must be encoded in repeatable deployment code.
- Prefer `make -j` for independent deployment targets unless serial output is needed for debugging.

## Functional validation

- Keep E2E focused on unique deployed integration contracts. Cover restart, rotation,
  expiry, reconciliation lifecycle, and protocol rejection matrices in focused tests
  unless the project requirement depends on the deployed integration.
- Do not validate third-party behavior beyond the exact project contract that uses it.
- Tests assert observed runtime behavior only. Do not inspect YAML, templates, scripts, source, or rendered manifests as an E2E proxy.
- Tests must be deterministic. Do not accept rerun-dependent results.
- Do not assert Pod or controller counts, readiness, rollout status, or log content as proxies for behavior. Use the client, service, identity, and policy contracts under test.
- Do not use application or identity fixture containers as generic test runners. Run external-client checks from the test process. Execute a probe in a fixture only when its container-local interface is the contract under test.
- Do not use sleeps in tests. Synchronize on observable conditions with bounded deadlines, and use deterministic or virtual time for time-dependent behavior.
- Positive E2E paths must not use fake metadata responders, test JWT signers, directly signed tokens, or test-only authentication paths. Focused unit tests may use local fakes and signers.
- VM requests are initiated by an external client. Do not run recurring assertions or result markers inside the guest.
- Do not create DPU Kubernetes Nodes or tenant-facing bare-metal Kubernetes Nodes in local E2E. Use stable fixtures for their source and registration contracts. The worker-host fixture already proves both `dsx_imds` plugin halves, so do not run a second standalone-host agent fixture.
- Kind confines its ordinary `k8s_psat` agent to the Phase 0 seed node. The normal DSX Identity Keys Pod runs there, and the same node emulates the external `dsx_dpu` execution boundary. The fixture entry is separate from the controller-managed entry beneath the `dsx_dpu` parent and is not evidence for that association. Metadata-service, both `dsx_imds` plugins, and every downstream component run in E2E.
- Local E2E proves repository-owned components and their exact runtime identity contracts. Assert subjects, parent IDs, selectors, hints, and tenant sources from live SPIRE registration state, then prove token delivery through the real applicable components.
- Stable fixtures may represent external DPU and bare-metal boundaries. Local E2E need not emulate DPU hardware, a bare-metal operating system, physical link-local interception, or NICo or Carbide internals.
- Keep fixture-only registrations separate from controller-managed registrations. Never accept a fixture parent or subject as evidence for the deployed association or identity shape.
- Local E2E may expose the real DSX IMDS directly as the external intercept and passthrough boundary. Keep the deployed flow documented first, and do not claim that the fixture proves physical integration.
- Focused tests cover multiple flat DSX Identity Keys shards, rotation overlap, NATS KV replay and watch
  reconstruction after restart, and complete aggregates at multiple DSX Identity Keys instances without
  expanding the local E2E topology.
- E2E must prove:
  - real k1 nginx Pod -> k1 SPIRE -> k1 DSX Identity Keys.
  - real k2 nginx Pod -> k2 SPIRE -> k2 DSX Identity Keys.
  - real KubeVirt VM -> metadata-service -> DSX Identity Keys Proxy CSI -> k2 DSX Identity Keys.
  - the controller-managed DPU entry has the exact `dsx_dpu` parent, base DPU subject, `dsx-dpu` hint, and Unix selectors for the real `dsx-token-fetcher` process. A stable external-agent fixture runs that fetcher through local SPIRE and DSX Identity Keys and produces a protected DSX token file without creating a DPU Kubernetes Node.
  - operator worker-host IMDS returns the configured operator tenant and exact node ID.
  - the virt-launcher Pod's operator entry and dynamically tenant-supplied VM entry have the same operator-tenant host-agent parent. Real Pod token delivery and VM metadata delivery prove issuance through that agent.
  - mutual rejection of the other shard's SPIRE JWT-SVIDs.
  - unauthenticated NATS connectivity and KV entries from both DSX Identity Keys instances.
  - both aggregate JWKS endpoints contain every valid published `kid` and verify either shard's token.
  - real k2 token login to the external OpenBao validation deployment with exact policies and access.
- Focused tests must prove globally unique key rotation and KV entry expiry
  deterministically without waiting for wall-clock time.
- CI-required validation and E2E jobs must not be manual or allowed to fail.
- Run `make validate` and the full E2E lifecycle after code or configuration changes. Use repository-pinned tool and dependency versions.

## Primary commands

```bash
make validate
make -j install
make test
make clean
```
