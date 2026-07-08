# DSX Identity Repository Rules

## Tool and file safety

- Preserve unrelated user changes and never use destructive Git or filesystem commands without explicit authorization.
- Use project-relative paths, `rg` for searches, and file-editing tools for edits.
- After moving, renaming, or deleting anything, search the whole repository for stale references in code, configuration, scripts, and documentation, then run the relevant tests.
- Keep scripts idempotent. Trap and clean up port-forwards, child processes, and temporary files on success, failure, and signals. Prefer in-memory temporary data.
- Prefer idempotent operations, immutable data, atomic swaps, and atomic values. Keep mutation local to one owner when state is required.
- Nest each mutex with only the mutable fields it protects so the lock boundary is visible from the struct definition.
- Prefer standard-library `slices` and `maps` transformations when they make ownership or mutation clearer. Keep explicit loops for ordered I/O, early errors, and state transitions.
- Keep configuration in files rather than embedding it in scripts. Use `.tmpl` only when placeholders require rendering.
- Prefer existing repository code, the Go standard library, imported libraries, native Kubernetes/Helm features, maintained sidecars/charts, and OSS APIs before custom code or wrappers.
- Centralize shared parsing and validation contracts. Do not copy equivalent URL validation between components.
- Do not add backward-compatibility code, aliases, tests, comments, or documentation. There is no active release.
- Add concise comments where intent is not obvious. Explain why Go code needs an ordering, ownership rule, security check, or workaround. Do not narrate self-explanatory syntax.
- Keep documentation factual and concise, without marketing language or decorative tone. Keep README topology and request-flow diagrams current.
- Do not use em dashes, en dashes, curly quotes, or semicolons in prose or comments. Use plain punctuation.
- Do not trim, normalize, case-fold, or coerce supplied input. Documented shell defaults may treat unset and empty values alike.
- Names must describe the current role and ownership. Remove stale temporary, legacy, and false component names instead of explaining them.
- Do not name data holders `state`. Name synchronized groups, snapshots, and aggregates for the resource or status they contain.
- Diagram actors must name the concrete form, such as DPU process, bare-metal host, Kubernetes Pod, sidecar, or KubeVirt VM. Distinguish cluster-local SPIRE JWT-SVIDs from exchanged DSX JWTs. Do not use `workload` or `raw JWT-SVID` as standalone labels. When documenting a plugin trust path, identify both custom NodeAttestor halves and the relevant WorkloadAttestor.
- Organize topology by shard so source CRs and concrete consumers converge on the same identity shard. Keep each diagram to one purpose, render it, and visually inspect it before committing.
- Update this file immediately when the user gives a general repository directive.
- Maintain a FIFO review task list. Fully investigate and complete each review item before implementing a later one.

## Authoritative topology

- k1 and k2 each run independent SPIRE and a co-located Exchange. There is no SPIRE federation.
- Common Services and Data Hall are deployment placement labels only. They do not define parent, group, or trust-set hierarchy in the key propagation protocol. Cluster names `k1` and `k2` describe only the local test topology.
- Every identity shard configures its own internal SPIRE trust domain and cluster name.
- Exchange key propagation is flat. Every Exchange in one Event Bus topic space uses a globally unique shard ID, publishes only its local public keys at `spiffe-exchange/v1/pub-keys/shard/<shard-id>/kid/<kid>`, subscribes to `spiffe-exchange/v1/pub-keys/shard/+/kid/+`, retains all valid public keys by globally unique `kid`, and serves the aggregate JWKS.
- Do not add bundle endpoints, federated trust, federation CA synchronization, or federation-only routes and certificates.
- Each Exchange accepts only SPIRE JWT-SVIDs issued by its local SPIRE and rejects SPIRE JWT-SVIDs issued by every nonlocal shard.
- The DSX Event Bus and OAuth2 provider are external to the umbrella. Local E2E deploys one instance in k1. Data Hall shards do not deploy either component.
- Every Exchange uses OAuth2 client credentials for NATS with only `spiffe-exchange.>` publish/subscribe access. Anonymous broker access is disabled.
- Run two active Exchange replicas per shard with unique per-Pod MQTT client IDs. Rolling updates and Pod disruption must keep one replica available.
- Expose only the MQTT and OAuth token endpoints required by Exchanges placed outside the Event Bus cluster.
- Kyverno is external. Its DSX identity policies belong in the umbrella. The Kyverno runtime does not.
- Relying parties use any reachable Exchange aggregate JWKS. Repository validation deploys OpenBao in k1 only for convenience.
- Cross-shard trust exists only through exchanged DSX tokens and propagated public keys.

## Helm and deployment

- `charts/dsx-identity` is the single production identity umbrella installed in each shard.
- Depend directly on the official SPIRE chart. Template repository-owned Exchange, controller, metadata, CSI, and policy resources directly. Configure node-attestor plugins through SPIRE values. Do not create separately packaged local wrapper charts.
- Install the official SPIRE CRD chart first where its APIs are required.
- DSX Event Bus, Keycloak, Kyverno runtime, cluster infrastructure, OpenBao, nginx, VMs, and E2E clients remain external to the umbrella.
- CI packages and publishes only artifacts the repository actually produces: the umbrella chart and current images.
- Apply every resource in an admission-compatible deployment phase before readiness waits. Split phases only to establish an API or admission webhook required to accept later resources.
- Never wait for a workload and then discover its Pod UID, node, placement, or service account to synthesize another deployment object. Use stable declarative source CRs and selectors known before startup.
- Use Helm values for production configuration and command-line overrides only for genuinely dynamic values.
- Use `kubectl rollout status` for controllers and `kubectl wait` for other resources. Avoid arbitrary sleeps.
- The local k2 fixture uses OVN-Kubernetes layer-3 UDN and KubeVirt. WSL is unsupported because the metadata bridge requires nftables.
- Use CSI drivers for workload identity delivery, not hostPath mounts.

## Identity contract

```text
spiffe://nca-<16-digits>.ds9.dsx.nvid.id/<dpu|host|vm>/<id>
  [/process/<name>|/pod/<uuid>]
  [/role/<role>]
```

- Service is `dsx`, partition is `nvid.id`, and the current region is `ds9`.
- Use DPU `metadata.name` as the DPU ID. Model a DPU as `/dpu/<id>`, never as a process.
- Required workload labels are tenant, `service=dsx`, resource type, and resource ID.
- SPIRE workload paths use `/tenant/<tenant>/service/dsx/<type>/<id>/pod/<uuid>`.

## Exchange and key propagation

- Keep the existing unauthenticated RFC 8693 `/v1/exchange` HTTP contract. Do not add mTLS, endpoint authentication, sender constraints, or another audience form field.
- Validate the submitted local SPIRE JWT-SVID. Preserve validated claims except for the existing `sub`, `iss`, and signature transformation.
- Issue with `spiffe://ds9.dsx.nvid.id`.
- Each shard loads one static private JWKS Secret containing `signing-jwks.json` and `active-kid`. Every tenant in that shard uses it.
- Publish each public JWK at `spiffe-exchange/v1/pub-keys/shard/<shard-id>/kid/<kid>` and store the aggregate only by globally unique `kid`. Refresh each lease often enough for at least three further publish attempts before expiry.
- Every `kid` is globally unique and never reused. Preserve configured values and generate new IDs from the go-jose SHA-256 public JWK thumbprint.
- Derive each Exchange signing algorithm from the JWK `alg` and validate that it matches the key material. Discovery must advertise the algorithms present in the aggregate JWKS.
- Validate `kid` uniqueness within each local signing keyset. Retained publications replace the globally unique `kid` they address.
- Rotation publishes the new key under its new kid. Old Pods stop refreshing the old key's publication when replaced, and its lease expires after the configured retention period.

## Controller and metadata

- Run one DSX Identity Controller in each Data Hall's regular management cluster, not a DPU Kamaji cluster.
- Watch only unstructured DPU and LaunchLayer source objects. Both are externally owned inputs and must be read-only to the controller.
- TODO: Replace the hard-coded `LaunchLayerSourceFixture` E2E stand-in when LaunchLayer supplies its source CR. Do not package a production DSX stand-in CRD or add source finalizers.
- Derive DPU, host, and VM entries, parents, selectors, hints, and optional `/role/<role>` suffixes in the controller. Source CRs must not contain complete SPIRE registration entries.
- The separately supplied `dsx_dpu` plugin is the external hardware-attestation boundary.
- The configured DPU holder runs the shared token-fetcher with hint `dsx-dpu`.
- `dsx_imds` is the shared host/VM contract. Both NodeAttestor sides live in this repository.
- Bare-metal host identity is one flow: the DPU metadata-service obtains and serves the host DSX identity through IMDS, then the bare-metal host SPIRE agent consumes it through `dsx_imds` for node attestation and serves host processes.
- Support only tokenless GET at `/latest/meta-data/identity?aud=<audience>` and `/2009-04-04/meta-data/identity?aud=<audience>`.
- Metadata responses support `application/json` and `text/plain`. An absent `Accept` header defaults to JSON. A present header with no acceptable supported media type returns HTTP 406.
- Require `Metadata: true`, reject proxy headers, and do not add an IMDS session-token PUT endpoint.
- VM identity comes from the `dsx_imds`-attested agent and the real `metadata-service`, SPIRE CSI, and RFC 8693 path.
- Data Hall KubeVirt metadata sidecars reach their local Exchange through Exchange Proxy CSI. Do not remove or bypass the proxy.

## OpenBao

- Use `/v1/auth/jwt/cel/login` with any reachable Exchange's full aggregate JWKS. Local E2E uses k1 Exchange.
- CEL parses tenant, region, service, resource type/ID, and optional process, pod, and role suffixes.
- Policies remain narrowly scoped to authenticated, region, tenant, service, and resource-type identities.
- Use the pinned official OpenBao chart/image and `bao` CLI through a port-forward with guaranteed cleanup.

## Engineering rules

- Use `#!/bin/bash`, `set -euo pipefail` where appropriate, `curl` rather than `wget`, and logging helpers from `scripts/common.sh`.
- Build unreleased Go images locally with multi-stage Dockerfiles and minimal runtimes. Pin image tags. Never use `latest`.
- Extract preload images from charts/manifests rather than hardcoding image arrays. Image loads must fail visibly.
- Give containers resource requests, limits, and appropriate security contexts. KubeVirt uses cached `containerDisk` volumes with `IfNotPresent`.
- Use Go 1.25+ and the repository's pinned `go-spiffe`. Follow standard formatting, context cancellation, structured logging, flag-based CLI configuration, and focused unit tests for business logic.
- Use `http.Header` methods instead of direct header-map access. Parse structured header values with the relevant standard-library grammar.
- Never call `os.Exit` outside `main`. Put command work and deferred cleanup in an error-returning function so `main` translates failure to `os.Exit` only after those defers run.
- Use two-space YAML indentation, descriptive labels, ConfigMaps/Secrets for configuration, and least-privilege service accounts.
- Keep policies in separate `.hcl` files and cluster-specific resources beside their deployment scripts.
- Query live clusters manually only for diagnosis and validation. Any required state change must be encoded in repeatable deployment code.
- Prefer `make -j` for independent deployment targets unless serial output is needed for debugging.

## Functional validation

- Tests assert observed runtime behavior only. Do not inspect YAML, templates, scripts, source, or rendered manifests as an E2E proxy.
- Do not use sleeps in tests. Synchronize on observable conditions with bounded deadlines, and use deterministic or virtual time for time-dependent behavior.
- Do not use fake metadata responders, test JWT signers, directly signed positive tokens, or test-only authentication paths.
- VM requests are initiated by an external client. Do not run recurring assertions or result markers inside the guest.
- Kind may use its ordinary `k8s_psat` agent only to supply the bootstrap host SPIRE JWT-SVID at the external `dsx_dpu` boundary. Metadata-service, both `dsx_imds` plugins, and every downstream component run in E2E.
- Focused tests cover multiple flat Exchange shards and complete aggregates at multiple Exchanges without expanding the local E2E topology.
- E2E must prove:
  - real k1 nginx Pod -> k1 SPIRE -> k1 Exchange.
  - real k2 nginx Pod -> k2 SPIRE -> k2 Exchange.
  - real KubeVirt VM -> metadata-service -> Exchange Proxy CSI -> k2 Exchange.
  - mutual rejection of the other shard's SPIRE JWT-SVIDs.
  - OAuth2 NATS connectivity and retained publication from both Exchanges.
  - both aggregate JWKS endpoints contain every valid published `kid` and verify either shard's token.
  - real k2 token login to the external OpenBao validation deployment with exact policies and access.
  - retained-state reconstruction after restarting both Exchanges.
  - globally unique key rotation and publication lease expiry.
- Focused tests must prove publication lease expiry deterministically without waiting for wall-clock time.
- CI-required validation and E2E jobs must not be manual or allowed to fail.
- Run `make validate` and the full E2E lifecycle after code or configuration changes. Use repository-pinned tool and dependency versions.

## Primary commands

```bash
make validate
make identity-deploy
make identity-verify
make identity-clean

make -j install
make test
make clean
```
