# DSX Identity Implementation Plan

> Execute the seven slices in order. For each slice, add a focused failing test, implement the smallest complete behavior, run focused and broader checks, review the diff, and commit a green checkpoint.

## Target architecture

k1 and k2 are independent identity shards. Each runs SPIRE and a local Exchange. There is no SPIRE federation. Each Exchange accepts JWT-SVIDs only from its co-located SPIRE installation.

k1 also runs the single DSX Event Bus, its OAuth2 provider, and OpenBao. Both Exchanges use OAuth2 client credentials to publish retained public-JWKS snapshots to that Event Bus and subscribe to every shard snapshot. Both Exchanges serve the same flattened public JWKS.

k2 runs the DSX Identity Controller, KubeVirt, the layer-3 tenant UDN, `dsx_imds`, and the Exchange Proxy CSI path. KubeVirt metadata sidecars exchange their local SPIRE JWT-SVIDs through the proxy to k2 Exchange.

```text
Contract + truthful tests
        ↓
Shared identity grammar
   ┌────┼──────────────┐
   ↓    ↓              ↓
Exchange/keysets   Identity controller   dsx_imds/helpers
   └────┴──────────────┘
        ↓
Shard-local Helm topology
        ↓
OpenBao + functional E2E
```

The production identity release is one `charts/spiffe` umbrella per shard. It uses the official SPIRE chart as its third-party subchart and directly renders all repository-owned production resources. The official SPIRE CRD chart is installed first. The Event Bus and OAuth2 provider, Kyverno runtime, cluster infrastructure, OpenBao, nginx, VMs, and E2E clients are external to the umbrella.

Locked contracts:

- Exchange issuer: `spiffe://ds9.dsx.nvid.id`.
- Public identity service: `dsx`. Region: `ds9`. Partition: `nvid.id`.
- Exchange signing: shard-local static signing JWKS Secret plus required active `kid`.
- `kid` generation: go-jose SHA-256 public-JWK thumbprint. Every retained key across all shards has a unique `kid`, including keys present during rotation overlap. Retired values are never reused.
- Event Bus: NVIDIA `dsx-exchange` v2.6.0, deployed only in k1.
- Event Bus authentication: OAuth2 client credentials with narrow `spiffe-exchange.>` publish/subscribe permissions and no anonymous access.
- DPU source: unstructured `provisioning.dpu.nvidia.com/v1alpha1, Kind=DPU`. DPU ID: `metadata.name`.
- DPU identity: first-class `/dpu/{dpu-id}` resource held by a configured DPU identity helper.
- Host and VM source: one minimal `IdentityAssignment` CR per resource.
- Metadata: tokenless DSX IMDS v1 GET served by `metadata-service`.

## 0. Make validation truthful and lock the identity grammar

Add validation targets for:

- `go test ./...`
- `bash -n` over every script
- Helm lint and render for k1, k2, and local values
- the external Event Bus values used by the demo

All E2E assertions must observe deployed behavior. Tests must not inspect YAML, templates, scripts, or source as a proxy for behavior. Positive identity and token inputs must traverse the deployed SPIRE, controller where applicable, Exchange, metadata, and relying-party paths. VM IMDS requests originate from an external test client. Test-only JWT signers and static-token metadata responders are not permitted.

Implement `internal/identity` with table-driven tests for:

```text
spiffe://nca-<16-digits>.ds9.dsx.nvid.id/<dpu|host|vm>/<id>
  [/process/<name>|/pod/<uuid>]
  [/role/<role>]
```

The package must:

- Build local SPIRE registration IDs:

  ```text
  spiffe://<local-trust-domain>/tenant/<tenant>/service/dsx/<resource-path>
  ```

- Transform local registration IDs into the public DSX form.
- Preserve valid process, pod, and role suffixes.
- Require the exact tenant, region, service, partition, resource type, and resource ID grammar.
- Require UUID pod IDs.
- Reject empty, malformed, or whitespace-normalized components.
- Supply the builders used by Exchange and the controller.

A DPU CR creates the base `/dpu/{dpu-id}` identity. Process and pod suffixes apply to separately declared workloads.

Exit gate:

- Static checks fail when a check fails or a required test cannot run.
- Canonical DPU, host, VM, process, pod, and role examples produce exact local and public IDs.
- Invalid tenant, domain, path, and suffix cases fail.

## 1. Implement shard-local Exchange signing

`POST /v1/exchange` implements the RFC 8693 request and response contract:

- No mTLS or endpoint authentication.
- No additional `audience` form field.
- The submitted SPIRE JWT-SVID is validated through the local SPIRE JWT source.
- A cluster-local SPIRE JWT-SVID issued by the other cluster is rejected.
- Validated `aud`, `iat`, `exp`, and other claims remain unchanged.
- `sub`, `iss`, and the signature are set by Exchange.

`cmd/spiffe-exchange-service` must:

- Require issuer `spiffe://ds9.dsx.nvid.id`.
- Load `signing-jwks.json` and `active-kid` from a mounted Secret.
- Require the active key to contain private asymmetric signing material with an `alg` compatible with the key.
- Allow non-active public keys so rotation overlap does not expose retired private material.
- Require a non-empty, unique `kid` on every configured key.
- Use the active shard key for every tenant and resource handled by that Exchange.
- Project only public JWK fields.
- Serve JWKS independently of the HTTP `Host` header.
- Return the configured issuer and JWKS URL from OIDC discovery.
- Report not-ready for missing or malformed keysets, missing active keys, public-only active keys, duplicate `kid` values, or unsupported algorithms.

Exit gate:

- Restarting Exchange retains its signing identity.
- Different tenants receive the same configured issuer and shard signing key.
- Claims other than `sub` and `iss` remain unchanged.
- Discovery and JWKS use configured values.
- Invalid signing configuration prevents readiness.
- Each Exchange accepts a SPIRE JWT-SVID issued by its local SPIRE and rejects a SPIRE JWT-SVID issued by the other shard.

## 2. Implement retained public-key propagation

Publishing and aggregation run inside the Exchange binary. Record the local
contract in `schemas/asyncapi/spiffe-exchange/pub-keysets.yaml` and use the
NVIDIA DSX Exchange MQTT 3.1.1 endpoint:

```text
Topic:    spiffe-exchange/v1/pub-keysets/shard/{shard_id}
QoS:      1
Retained: true
Payload:  { "keys": [ <public RFC 7517 JWK>, ... ] }
```

The topic supplies `shard_id`. A public snapshot contains at least one key. A retained zero-length message removes that shard snapshot. Private JWK fields, symmetric keys, unsupported algorithms, and malformed JWKS reject the entire update.

Each Exchange:

1. Loads its shard-local keyset.
2. Obtains and refreshes an OAuth2 client-credentials token.
3. Connects with MQTT username `oauthtoken`, the access token as password, and scope `mqtt`.
4. Subscribes to `spiffe-exchange/v1/pub-keysets/shard/+`.
5. Publishes its complete public snapshot under its shard ID.
6. Stores complete snapshots by shard ID.
7. Atomically replaces one shard snapshot per valid update.
8. Serves a deterministic flattened union at `/.well-known/jwks.json`.

Key handling:

- Preserve every configured source `kid` unchanged.
- Require `kid` uniqueness within each local signing keyset.
- Keep the last valid snapshot after any malformed or private publication.
- Readiness requires a valid local keyset, an active subscription, and an acknowledged local publication.
- Reconnection republishes the local snapshot and consumes retained snapshots.
- Rotation publishes the new active key with the retained public key, then publishes a reduced snapshot when the overlap ends.

Focused tests start an OAuth2-protected local broker and at least two Exchange/key-sync instances. They prove:

- Both instances serve both shard snapshots.
- Tokens from either shard verify with either aggregate.
- A new instance reconstructs the aggregate from retained messages.
- Malformed and private publications leave the aggregate unchanged.
- Rotation exposes both unique keys during overlap and removes only the retired key afterward.
- A shard tombstone removes only that shard.
- Missing and incorrect OAuth2 credentials are rejected.

## 3. Implement the DSX Identity Controller

Add `cmd/dsx-identity-controller`, a focused internal controller package, direct umbrella templates for its Deployment, ServiceAccount, and narrow RBAC, and `identity.dsx.nvidia.com/v1alpha1, Kind=IdentityAssignment`.

Use controller-runtime and the SPIRE CR types pinned by the umbrella chart.

### DPU reconciliation

Watch this object dynamically in the regular k2 management cluster:

```yaml
apiVersion: provisioning.dpu.nvidia.com/v1alpha1
kind: DPU
```

Kind installs an API-only CRD fixture with this GVK so the informer remains active without the DPF operator. Do not connect to a DPU Kamaji cluster or import the DPF API module.

For each DPU:

- Use `metadata.name` as the DPU ID.
- Parent the identity to:

  ```text
  spiffe://<local-trust-domain>/spire/agent/dsx_dpu/<dpu-id>
  ```

- Create one base DPU `ClusterStaticEntry`:

  ```text
  spiffe://<local-trust-domain>/tenant/<operator-tenant>/service/dsx/dpu/<dpu-id>
  ```

- Use hint `dsx-dpu`.
- Select the configured identity helper namespace and service account.
- Scope the helper to the specific DPU parent.

The `dsx_dpu` NodeAttestor artifact and SPDM configuration are supplied separately.

### IdentityAssignment

The CR accepts only source data:

```yaml
spec:
  type: host | vm
  tenantID: nca-...
  resourceID: ...
  role: ...              # optional final /role/<role> suffix
  parentID: ...          # DPU ID for host, host ID for VM
  parentTenantID: ...    # required for VM
  holder:
    namespace: ...
    serviceAccount: ...
    podUID: ...          # optional UUID
```

The controller derives SPIFFE IDs, parent IDs, selectors, hints, and the explicit SPIRE `className`:

- Host: `/host/{resourceID}`, parent `dsx_dpu` agent, hint `dsx-imds`.
- VM: `/vm/{resourceID}`, parent `dsx_imds` agent for `parentTenantID` and `parentID`, hint `dsx-imds-vm`.
- Optional role: validated final `/role/{role}` suffix.

The Kind VM fixture pins placement to `k2-worker` and submits the UDN,
controller source CRs, external client, VM service account,
`IdentityAssignment`, and VM before any readiness wait. Deployment scripts do
not derive controller inputs from running Pods.

Lifecycle requirements:

- Deterministic child names and source-UID labels.
- Server-side apply for updates and re-tenanting.
- Finalizer cleanup of cluster-scoped children before source deletion.
- No namespaced-to-cluster-scoped owner references.
- Invalid updates remove the managed identity and emit a Kubernetes event.
- Issued JWTs expire according to their configured short TTL.

Exit gate:

- Fake-client tests cover DPU, host, and VM creation.
- The base DPU subject has no process suffix.
- Tests cover re-tenanting, parent changes, deletion, finalizer cleanup, hints, selectors, class name, optional role, and invalid updates.

## 4. Implement `dsx_imds` and metadata delivery

`dsx_imds` is the host SPIRE NodeAttestor. DSX IMDS v1 is the shared tokenless HTTP identity API for hosts and VMs.

The HTTP contract is:

- `GET /latest/meta-data/identity?aud=...`
- `GET /2009-04-04/meta-data/identity?aud=...`
- Required `Metadata: true` header.
- Rejection of proxy headers.
- JSON and plain-text responses.
- No session-token `PUT`, mTLS, or endpoint authentication.

`cmd/metadata-service` must:

- Fetch all JWT-SVIDs for the requested audience.
- Require exactly one SVID with the configured `dsx-imds` or `dsx-imds-vm` hint.
- Exchange it through the RFC 8693 client.
- Use the Exchange Proxy CSI Unix socket in KubeVirt sidecars.
- Send k2 metadata exchanges to k2 Exchange.

Implement both NodeAttestor halves:

- Agent plugin: calls DSX IMDS v1 with `Metadata: true`, requests the configured SPIRE-server audience, and sends the returned exchanged host JWT as attestation data.
- Server plugin: validates issuer, signature against the local Exchange aggregate JWKS, exact audience, expiry/not-before, canonical tenant grammar, and a base `/host/{host-id}` subject. It then emits:

  ```text
  spiffe://<local-trust-domain>/spire/agent/dsx_imds/tenant/<tenant>/host/<host-id>
  ```

The token fetcher must select its configured SVID hint, atomically replace its output, create directories with `0700`, write files with `0600`, remove stale output on shutdown or unrecoverable refresh failure, and refresh before expiry.

Exit gate:

- Agent/server integration tests attest a valid host to the expected stable agent ID.
- Wrong issuer, audience, signature, subject type, suffix, expiry, or not-before fails.
- Missing metadata header, proxy headers, missing or duplicate hint, and unavailable Exchange proxy fail.
- A real k2 VM obtains its token through metadata-service, SPIRE CSI, Exchange Proxy CSI, and k2 Exchange.
- The VM request is initiated by an external test client.
- No recurring guest test process or test-only HTTP responder exists.

## 5. Deploy the shard topology with Helm

| Component | k1 | k2 |
|---|---:|---:|
| Local SPIRE | Yes | Yes |
| Local Exchange | Yes | Yes |
| DSX Event Bus and OAuth2 provider | Yes | No |
| DSX Identity Controller | No | Yes |
| OpenBao | Yes | No |
| KubeVirt, OVN, and layer-3 UDN | No | Yes |
| Exchange Proxy CSI and VM injection | No | Yes |
| `dsx_imds` integration | No | Yes |

Install one `charts/spiffe` umbrella release in each shard:

- Use the official SPIRE chart as its third-party subchart.
- Render Exchange, the Identity Controller, Exchange Proxy CSI, VM support, workload support, and Kyverno policies directly from the umbrella.
- Install the official `spire-crds` chart first.
- Mount one distinct shard-local signing Secret in each Exchange.
- Configure each Exchange with its local SPIRE JWT source.
- Deploy the Identity Controller only in k2, where the watched CRs live.
- Point k2 metadata and token-fetcher flows to k2 Exchange.

Deploy external prerequisites separately:

- One `dsx-exchange` Event Bus and OAuth2 provider release in k1.
- Kyverno runtime.
- Cluster networking and Gateway infrastructure.
- OpenBao and dedicated E2E workloads.

Event Bus topology:

- Deploy a single-node, memory-backed, OAuth2-authenticated Kind profile.
- Use the pinned Keycloak 26.0.7 image and minimal realm fixture.
- Store the Exchange OAuth2 client credential in each Exchange namespace.
- Use in-cluster MQTT and OAuth endpoints for k1 Exchange.
- Expose only the MQTT and OAuth endpoints that k2 Exchange needs through explicit Gateway and DNS resources.
- Do not expose SPIRE bundle endpoints or configure SPIRE federation.
- Configure shard IDs `k1` and `k2`.

Exit gate:

- k1 runs SPIRE, Exchange, the Event Bus, OAuth2 provider, and OpenBao.
- k2 runs SPIRE, Exchange, the controller, KubeVirt support, Exchange Proxy CSI, and `dsx_imds`.
- Both Exchanges obtain OAuth2 tokens and connect to the k1 Event Bus.
- Each Exchange accepts a SPIRE JWT-SVID issued by its local SPIRE and rejects a SPIRE JWT-SVID issued by the other shard.
- Both Exchanges publish retained snapshots and serve both public keysets.
- k2 VM traffic reaches k2 Exchange through the CSI socket.
- Both umbrella profiles lint and render.
- Repeated installation is idempotent.

## 6. Prove cross-shard trust through OpenBao

Configure OpenBao to:

- Consume k1 Exchange's aggregate `/.well-known/jwks.json`.
- Use JWT CEL authentication at `/v1/auth/jwt/cel/login`.
- Bind issuer `spiffe://ds9.dsx.nvid.id` and the requested audience.
- Parse exact `nca-<16-digits>.ds9.dsx.nvid.id` subjects.
- Derive tenant, region, service, resource type, resource ID, optional process or pod, and optional role.
- Assign only `spiffe-authenticated`, `region-*`, `tenant-*`, `service-*`, and `resource-type-*` CEL policies.
- Grant only the secret paths selected by those policies.

The full E2E suite must prove functional behavior:

- A real k1 workload obtains a local SPIRE JWT-SVID, exchanges it through k1 Exchange, and authenticates to OpenBao.
- A real k2 workload obtains a local SPIRE JWT-SVID, exchanges it through k2 Exchange, and authenticates to the same OpenBao.
- A real k2 VM obtains its identity through metadata-service and Exchange Proxy CSI to k2 Exchange, then authenticates to OpenBao.
- k1 Exchange rejects a raw k2 SPIRE JWT-SVID and k2 Exchange rejects a raw k1 SPIRE JWT-SVID.
- Both Exchanges authenticate to the single k1 Event Bus with OAuth2 client credentials.
- Both Exchanges publish retained public keys.
- Both aggregate JWKS endpoints contain both shards' public keysets.
- A token from either Exchange verifies against either aggregate endpoint.
- A k2 token receives exactly the expected OpenBao policies and secret access. Forbidden access fails.
- A real wrong-audience SPIRE JWT-SVID exchanged by its local Exchange is rejected by OpenBao.
- Restarting both Exchanges reconstructs the full aggregate from retained Event Bus state without changing signing identities.
- Rotating either shard exposes its old and new globally unique keys during overlap.
- Focused Go tests advance publication lease time deterministically and remove only the retired key.
- The installation and complete E2E test succeed from a clean environment.

E2E tests may not substitute source inspection, rendered-manifest inspection, fake metadata responders, test-only JWT signers, directly signed positive tokens, or guest boot markers for these observations.

## Final validation

```bash
make validate
make -j install
make test
make clean
```

The separately supplied `dsx_dpu` plugin and SPDM configuration are the only external identity implementation input. The controller targets its stable agent-ID contract.
