# DSX Identity Helm chart

Parent chart for one identity shard: SPIRE, Identity Keys, identity injection,
the Identity Controller, Kata support, and DPF child-chart resources.

## Prerequisites

| When enabled | Requires | Used for |
|---|---|---|
| All installs | Kubernetes 1.21+, Helm 3, matching `spire-crds` chart | Helm deployment and SPIRE APIs |
| External agent (default) | SPIRE Workload API socket on each selected node | SPIFFE CSI mounts |
| Identity Keys (default) | NATS JetStream and signing-key Secrets. OAuth2 issuer and credentials when configured | Key exchange and token signing |
| Pod tokens or VM support | Kyverno. KubeVirt for VM support | Sidecar injection and VM identity |
| Service monitors (default) | Prometheus Operator CRDs | `ServiceMonitor` and `PodMonitor` |
| DPU services | DPF `DPUService` CRD and controller | IMDS and optional CSI child charts |
| Identity controller | DPU, LaunchLayer, and SPIRE CRDs | Source-to-SPIRE reconciliation |
| Kata support | Kata nodes, cgroup v2, systemd cgroups | Host-to-guest Workload API proxy |

## Configuration

```yaml
global:
  # Tenant encoded in identities for operator-owned DPUs, workers, and Pods.
  operatorTenant: operator-1
  dsx:
    # Issuer placed in exchanged DSX JWTs.
    jwtIssuer: https://identity.example.com
    # Deployment coordinates encoded in exchanged DSX identity trust domains.
    region: region-1
    partition: example.com
    # Service used when a source has no explicit service label.
    defaultServiceName: dsx
  spire:
    # Trust domain for shard-local SPIFFE IDs.
    trustDomain: shard.example.com
    # Kubernetes cluster name used by SPIRE workload attestation.
    clusterName: shard-a
    # Issuer placed in shard-local SPIRE JWT-SVIDs.
    jwtIssuer: https://spire.shard.example.com

spire:
  spire-server:
    controllerManager:
      extraEnv:
        # Must match global.operatorTenant for operator Pod registrations.
        - name: OPERATOR_TENANT
          value: operator-1

identityKeys:
  # Stable, unique publisher ID within the shared NATS KV bucket.
  shardID: shard-a
  # Public URL advertised in the OpenID discovery document.
  jwksURL: https://identity-keys.shard.example.com/.well-known/jwks.json
  # Existing Secrets containing all signing keys and the selected active kid.
  signingKeysetSecret: dsx-identity-keys-signing-keyset
  activeKIDSecret: dsx-identity-keys-active-kid
  nats:
    # Event Bus endpoint used to exchange public keys between shards.
    serverURL: tls://event-bus.example.com:4222
    kv:
      # Shared JetStream KV bucket containing every shard's public keys.
      bucket: DSX_IDENTITY_KEYS
      replicas: 3
    auth:
      # Obtain the NATS bearer token with OAuth2 client credentials.
      mode: oauth2
      credentialsSecret: dsx-identity-keys-nats-oauth
      credentialsKey: credentials.json
      issuerURL: https://auth.example.com
```

Identity Keys defaults to two replicas. The Identity Controller uses two when
enabled. SPIRE deployment and datastore settings pass through to the upstream
chart.

The JWT issuer is independent of exchanged SPIFFE IDs, which use
`<tenant>.<region>.<service>.<partition>`. Identities without a service label use
`global.dsx.defaultServiceName`.

```yaml
spire:
  spire-agent:
    # Hosts run an externally provisioned SPIRE agent instead of this subchart.
    enabled: false
  spire-server:
    image:
      registry: <registry>
      repository: <repository>/dsx-spire-server
      tag: <chart-version>
    customPlugins:
      nodeAttestor:
        dsx_imds:
          plugin_cmd: /opt/spire/plugins/dsx-imds-node-attestor-server
          plugin_checksum: ""
          plugin_data:
            # DSX JWT issuer accepted for host bootstrap attestation.
            issuer: https://identity.example.com
            # Audience required on the bootstrap token.
            audience: spiffe://shard.example.com/spire/server
            # Public keys used to validate the bootstrap token.
            jwks_url: https://identity-keys.shard.example.com/.well-known/jwks.json
  spiffe-csi-driver:
    enabled: true
    # Cluster-wide CSI driver name referenced by workload volumes.
    pluginName: csi.spiffe.io
    # Host path where the external SPIRE agent serves its Workload API.
    agentSocketPath: /run/spire/agent-sockets/spire-agent.sock
```

Provision the host agent outside this chart. The matching
`<registry>/<repository>/dsx-spire-agent:<chart-version>`
image embeds that plugin at `/opt/spire/plugins/dsx-imds-node-attestor-agent`.
If the bundled agent is enabled for another node class, constrain it so it
cannot share nodes with the external host agent.

```yaml
podTokenFetcher:
  enabled: true
  # Token-exchange endpoint used by injected Pod sidecars.
  identityKeysURL: https://identity-keys.shard.example.com/v1/exchange

vmSupport:
  enabled: true
  identityKeysProxy:
    # Upstream Identity Keys target exposed through the VM proxy CSI socket.
    target: TCP:dsx-identity-keys.dsx-identity.svc.cluster.local:8080

identityController:
  # Reconcile DPU and LaunchLayer sources into SPIRE entries.
  enabled: true
dpuService:
  imds:
    # Deploy the DSX IMDS child chart to selected DPU clusters.
    enabled: true
  spiffeCsi:
    # Enable only when the DPU platform does not provide csi.spiffe.io.
    enabled: false
  # Token-exchange endpoint reachable from those DPU clusters.
  identityKeysURL: https://identity-keys.shard.example.com/v1/exchange
```

When `dpuService.imds` is enabled, the identity controller derives selectors
from the release namespace and `dpuService.workload`. An explicit
`identityController.dpuMetadataSelectors` list replaces those defaults. DPF's
SPIFFE CSI bind-mounts `/run/spire`, preserving `agent.sock` for DSX IMDS.

```yaml
kataSupport:
  # Add the host and guest socket proxies for Kata workloads.
  enabled: true
  # RuntimeClasses whose Pods receive the guest proxy.
  runtimeClassNames:
    - kata-qemu-runtime-rs
    - kata-qemu
```

Kata Pods run inside VMs, so SPIFFE CSI cannot carry the Workload API socket
across the boundary. The [host proxy](../../kata-uds-proxy/README.md) serves it
over vsock and the guest proxy re-serves it at the path already used by the
token fetcher.

The host proxy uses `hostPID` and writes the host cgroup, so Kata support
requires `privileged` Pod Security Admission, cgroup v2 with the systemd driver,
and `sandbox_cgroup_only = true`. The default `nodeSelector` targets
`katacontainers.io/kata-runtime=true`. List every enabled Kata RuntimeClass.

`imagePullSecrets` applies to chart-owned and Kyverno-injected DSX images.
Each matched consumer namespace must contain Secrets with the same names.

Kyverno injects identity sidecars and prevents application containers from
mounting the Workload API or DSX Identity Keys Proxy sockets.

## Deploy

### Secrets

Create referenced Secrets in the release namespace before installing the chart.
Keep secret values out of Helm values and source control.
Secrets may be managed by any provider, provided they use the data keys and
decoded contents below.

| Value | Secret data key | Decoded contents |
|---|---|---|
| `identityKeys.signingKeysetSecret` | `keyset.json` | `{"keys":["<PEM-encoded private key>", ...]}` |
| `identityKeys.activeKIDSecret` | `active-kid.json` | `{"active_kid":"<RFC 7638 public JWK thumbprint>","alg":"<JOSE algorithm>"}` |
| `identityKeys.nats.auth.credentialsSecret` | `identityKeys.nats.auth.credentialsKey` | `{"client_id":"<non-empty>","client_secret":"<non-empty>"}` |

Signing keys may be SEC 1 EC, PKCS#1 RSA, or PKCS#8 asymmetric private keys.
Every key must support the algorithm selected by `active-kid.json`, and
`active_kid` must identify one key in `keyset.json`. Supported algorithms are
`EdDSA`, `ES256`, `ES384`, `ES512`, `RS256`, `RS384`, `RS512`, `PS256`, `PS384`,
and `PS512`.

Identity Keys reads `keyset.json` and `active-kid.json` only at process start.
It rereads OAuth2 credentials when its cached access token expires.

#### Vault Secrets Operator example

The following example exports every version of an
[exportable Vault Transit signing key](https://developer.hashicorp.com/vault/api-docs/secret/transit#export-key)
and transforms Vault values into the three Kubernetes Secret payloads:

```yaml
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultDynamicSecret
metadata:
  name: dsx-identity-keys-signing-keyset
  namespace: dsx-identity
spec:
  vaultAuthRef: <vault-auth-name>
  mount: transit
  path: export/signing-key/<transit-key>
  requestHTTPMethod: GET
  refreshAfter: 1m
  rolloutRestartTargets:
    - kind: Deployment
      name: dsx-identity-keys
  destination:
    name: dsx-identity-keys-signing-keyset
    create: true
    overwrite: true
    type: Opaque
    transformation:
      excludeRaw: true
      excludes:
        - .*
      templates:
        keyset.json:
          text: '{{ dict "keys" (values (get .Secrets "keys")) | mustToJson }}'
---
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultStaticSecret
metadata:
  name: dsx-identity-keys-active-kid
  namespace: dsx-identity
spec:
  type: kv-v2
  vaultAuthRef: <vault-auth-name>
  mount: <kv-mount>
  path: <active-kid-path>
  refreshAfter: 1m
  rolloutRestartTargets:
    - kind: Deployment
      name: dsx-identity-keys
  destination:
    name: dsx-identity-keys-active-kid
    create: true
    overwrite: true
    type: Opaque
    transformation:
      excludeRaw: true
      excludes:
        - .*
      templates:
        active-kid.json:
          text: '{{ dict "active_kid" (get .Secrets "active_kid") "alg" (get .Secrets "alg") | mustToJson }}'
---
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultStaticSecret
metadata:
  name: dsx-identity-keys-nats-oauth
  namespace: dsx-identity
spec:
  type: kv-v1
  vaultAuthRef: <vault-auth-name>
  mount: <oauth-client-mount>
  path: <oauth-client-path>
  refreshAfter: 1m
  destination:
    name: dsx-identity-keys-nats-oauth
    create: true
    overwrite: true
    type: Opaque
    transformation:
      excludeRaw: true
      excludes:
        - .*
      templates:
        credentials.json:
          text: '{{ dict "client_id" (get .Secrets "client_id") "client_secret" (get .Secrets "secret") | mustToJson }}'
```

Set the chart Secret references to the three `destination.name` values. Adapt
Vault mounts, paths, authentication, and source field names to the deployment.

#### Rotation

Signing-key rotation keeps old and new public keys available while issued JWTs
remain valid:

1. Add the new private key to the exported keyset without changing the active
   key.
2. Wait for Secret synchronization, a completed rolling restart, and the new
   `kid` in `/.well-known/jwks.json`.
3. Change `active_kid` and `alg`, then wait for another completed rolling
   restart.
4. Wait for JWTs signed by the old key to expire and downstream JWKS caches to
   refresh.
5. Remove the old private key from the exported keyset, then complete the final
   rolling restart.

External secret controllers without restart targeting must trigger a rolling
restart of `Deployment/dsx-identity-keys` after signing keyset or active-key
changes.

OAuth2 credential rotation does not require a restart. Create the replacement
credential at the issuer, update Vault, wait for the mounted Secret to change and
for Identity Keys to obtain a token with the replacement, then revoke the old
credential.

### Install

```bash
helm upgrade --install dsx-identity <oci-chart-reference> \
  --version <release-version> \
  --namespace dsx-identity \
  --create-namespace \
  --values <shard-values.yaml> \
  --atomic \
  --timeout 15m
```

To install from source, download the locked SPIRE dependency:

```bash
helm repo add spiffe https://spiffe.github.io/helm-charts-hardened/
helm dependency build charts/dsx-identity
```

Use `./charts/dsx-identity` as `<oci-chart-reference>`.

## Validation

```bash
make test-chart
```
