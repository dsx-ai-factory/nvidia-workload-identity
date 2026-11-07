# NVIDIA Workload Identity Helm chart

Parent chart for one identity shard: [SPIRE](https://spiffe.io/docs/latest/spire-about/), Identity Keys, identity injection,
the Identity Controller, Kata support, and DPF child-chart resources.

## Prerequisites

| When enabled | Requires | Used for |
|---|---|---|
| All installs | Kubernetes 1.36+, Helm 3, matching `spire-crds` chart | Helm deployment and SPIRE APIs |
| External agent (default) | SPIRE Workload API socket on each selected node | [SPIFFE CSI](https://github.com/spiffe/spiffe-csi) mounts |
| Identity Keys (default) | Signing-key Secrets. [NATS](https://docs.nats.io/nats-concepts/jetstream/key-value-store) JetStream, OAuth2 issuer, and credentials when NATS is enabled and configured | Key exchange and token signing |
| Pod tokens or VM support | [KubeVirt](https://kubevirt.io/user-guide/) for VM support | Native sidecar injection and VM identity |
| DPU Pod tokens | DPF `DPUService` CRD and controller | Native token-fetcher injection in DPU clusters |
| Service monitors (default) | Prometheus Operator CRDs | `ServiceMonitor` and `PodMonitor` |
| DPU services | DPF `DPUService` CRD and controller | IMDS and token-fetcher child charts |
| Identity controller | DPU, LaunchLayer, and SPIRE CRDs | Source-to-SPIRE reconciliation |
| Kata support | Kata nodes, cgroup v2, systemd cgroups | Host-to-guest Workload API proxy |

## Configuration

```yaml
global:
  # Tenant encoded in identities for operator-owned DPUs, workers, and Pods.
  operatorTenant: operator-1
  dsx:
    # Issuer placed in exchanged DSX JWT-SVIDs.
    jwtIssuer: https://identity.example.com
    # Deployment coordinates encoded in exchanged DSX JWT-SVID trust domains.
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
      parentIDTemplate: 'spiffe://{{ .TrustDomain }}/spire/agent/dsx_imds/tenant/$OPERATOR_TENANT/bm/{{ .NodeMeta.Name }}'

identityKeys:
  # Stable, unique publisher ID within the shared NATS KV bucket.
  shardID: shard-a
  # Public URL advertised in the OpenID discovery document.
  jwksURL: https://identity-keys.shard.example.com/.well-known/jwks.json
  # Existing Secrets containing all signing keys and the selected active kid.
  signingKeysetSecret: dsx-identity-keys-signing-keyset
  activeKIDSecret: dsx-identity-keys-active-kid
  nats:
    # Disable to serve only this shard's public signing keys.
    enabled: true
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

The controller-manager's `OPERATOR_TENANT` must equal `global.operatorTenant`.
Changing both values updates the SPIRE Pod template and rolls the StatefulSet
without a restart hook.

Identity Keys defaults to two replicas. The Identity Controller uses two when
enabled. SPIRE deployment and datastore settings pass through to the upstream
chart.

Use `identityKeys.extraVolumes` and `identityKeys.extraVolumeMounts` to mount
existing Secrets or ConfigMaps into Identity Keys. Each referenced object must
exist in the release namespace. Use unique volume names and mount paths that
preserve the signing-key, active-key, NATS credentials, and SPIFFE mounts.

For a private OAuth2 or NATS TLS CA, mount a PEM bundle as one file in Go's
default trust directory. The ConfigMap must contain a `ca.crt` key:

```yaml
identityKeys:
  extraVolumes:
    - name: identity-ca
      configMap:
        name: identity-ca
  extraVolumeMounts:
    - name: identity-ca
      mountPath: /etc/ssl/certs/identity-ca.pem
      subPath: ca.crt
      readOnly: true
```

This preserves the image's existing trust files. Recreate the Identity Keys Pods
after changing the bundle because subPath mounts do not receive ConfigMap
updates and Go processes may cache their root pool. The extra mounts apply to
Identity Keys. Other consumers need their own trust mounts.

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
            # DSX JWT-SVID issuer accepted for host bootstrap attestation.
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
  tokenFetcher:
    # Deploy the native admission policy to supported DPU clusters.
    enabled: true
    # Socket filename preserved by DPF's CSI mount.
    spireSocketPath: /var/run/secrets/spire.io/agent.sock
  imds:
    # Deploy the DSX IMDS child chart to selected DPU clusters.
    enabled: true
  # Token-exchange endpoint reachable from those DPU clusters.
  identityKeysURL: https://identity-keys.shard.example.com/v1/exchange
```

`vmSupport.resources.networkSetup` and `vmSupport.resources.metadata` configure
the injected VM sidecars. Defaults are `10m` CPU / `32Mi` memory for network
setup and `50m` / `64Mi` for metadata-service. Set both `requests` and `limits`
when overriding them. Equal CPU and memory requests and limits preserve the
Guaranteed QoS required for dedicated CPU placement.

VM IMDS networking follows the primary [OVN](https://github.com/ovn-kubernetes/ovn-kubernetes) allocation and supports IPv4-only,
IPv6-only, and dual-stack guests. See the [guest network requirements](../../cmd/dsx-imds-network-setup/README.md#vm-bindings)
for supported bindings, addresses, and MAC requirements.

Each Pod selected for token-fetcher injection must set both
`spiffe.nvidia.com/inject-dsx-token-fetcher: "true"` and its relying-party
audience in `spiffe.nvidia.com/token-audience`. The
[examples](../../examples/token-fetcher/README.md) cover ordinary Kubernetes
Pods and DPUService deployment.

The fetcher health endpoint and startup probe use
`podTokenFetcher.startupProbe.httpGet.port` (default `8080`). A Pod can override
both with `spiffe.nvidia.com/token-fetcher-health-port: "18080"` when another
container uses that port. The annotation must contain a valid TCP port number.

Keep the token-fetcher policy in a standalone `DPUService`. A policy-only
service in a `DPUDeployment` blocks `DPUNodeMaintenance`.

Configure the external [DPF identity templates](#dpf-identity-configuration)
before provisioning DPUs. DPF registers DPU Agent and DPUService workload
identities. The Identity Controller registers the paired bare-metal host.

`identityController.sourceAPIPollInterval` sets the discovery retry period for
absent DPU and Launch Layer APIs. It defaults to `1h` and must be a positive
Go duration, such as `30m`. Installed APIs use watches for source changes.

When `dpuService.imds` is enabled, the identity controller derives selectors
from the release namespace and `dpuService.workload`. An explicit
`identityController.dpuMetadataSelectors` list replaces those defaults. DPF's
SPIFFE CSI bind-mounts `/run/spire`, preserving `agent.sock` for DSX IMDS.

```yaml
kataSupport:
  # Add the host and guest socket proxies independently of the token fetcher.
  enabled: true
  # RuntimeClasses whose Pods receive the guest proxy.
  runtimeClassNames:
    - kata-qemu-runtime-rs
    - kata-qemu
```

Kata Pods run inside VMs, so SPIFFE CSI cannot carry the Workload API socket
across the boundary. The [proxy](../../kata-uds-proxy/README.md) forwards it over
vsock for selected Kata Pods, independently of token-fetcher injection.

The host proxy uses `hostPID` and writes the host cgroup, so Kata support
requires `privileged` Pod Security Admission, cgroup v2 with the systemd driver,
and `sandbox_cgroup_only = true`. The default `nodeSelector` targets
`katacontainers.io/kata-runtime=true`. List every enabled Kata RuntimeClass.

`imagePullSecrets` applies to chart-owned and admission-injected DSX images.
Each matched consumer namespace must contain Secrets with the same names.

Native Kubernetes admission policies inject identity sidecars and prevent
application containers from mounting the Workload API or NVIDIA Workload
Identity Keys Proxy sockets. Whenever `spire.enabled` is true, validation
enforces operator Pod service and role label formats.

## Deploy

### DPF identity configuration

Setting `spec.security.spiffe` in `DPFOperatorConfig` enables DPF's
`csi.spiffe.io` driver for DPU workloads.

Configure DPF's identity templates in the external `DPFOperatorConfig` before
provisioning DPUs. The local templates must include the tenant and service
segments required by NVIDIA Workload Identity Keys. This fragment shows
operator tenant `operator-1`, service `dsx`, region `ds9`, and partition
`nvid.id`:

```yaml
spec:
  security:
    spiffe:
      dpuAgentSPIFFEIDTemplate: 'spiffe://{{ .TrustDomain }}/tenant/operator-1/service/dsx/dpu/{{ .SerialNumber }}/process/dpu-agent'
      dpuAgentExchangedSPIFFEIDTemplate: 'spiffe://operator-1.ds9.dsx.nvid.id/dpu/{{ .SerialNumber }}/process/dpu-agent'
      dpuServiceSPIFFEIDTemplate: 'spiffe://{{ .TrustDomain }}/tenant/operator-1/service/dsx/dpu/{{ .SerialNumber }}/dpuservice/{{ .ServiceID }}/namespace/{{ .Namespace }}'
      dpuServiceExchangedSPIFFEIDTemplate: 'spiffe://operator-1.ds9.dsx.nvid.id/dpu/{{ .SerialNumber }}/dpuservice/{{ .ServiceID }}/namespace/{{ .Namespace }}'
      tokenExchangeEndpoint: https://identity-keys.shard.example.com/v1/exchange
```

Match the tenant to `global.operatorTenant` and the service, region, and
partition to `global.dsx.defaultServiceName`, `global.dsx.region`, and
`global.dsx.partition`. DPF supplies the normalized serial,
namespace, and service ID. The DPU Agent's helper uses `tokenExchangeEndpoint`
before writing its Kubernetes API token. Its exchanged subject template must
match the token subject because DPF uses it for the DPU Agent's RoleBinding.
DPUService token-fetcher sidecars use the exchange URL configured in the
identity chart. DPF validates the DPUService exchanged subject template but
does not consume that subject itself.

Set the remaining DPF SPIFFE fields, provide the trust bundle, and configure
Kubernetes API authentication as described in the
[DPF SPIFFE guide](https://github.com/NVIDIA/doca-platform/blob/545320feb894f20577fa0abf6885c6aafc4fb2eb/docs/public/advanced-configuration/spiffe-identity.md).

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

Identity Keys reloads `keyset.json` and `active-kid.json` when either mounted
file changes. Filesystem notifications trigger reloads, and
`identityKeys.fileWatchPollInterval` sets the polling interval (default `30s`).
Invalid or mismatched sources retain the last valid signer and public keys.
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
2. Wait for Secret synchronization and the new `kid` in
   `/.well-known/jwks.json`.
3. Change `active_kid` and `alg`, then wait for exchanged JWTs to use the new
   key.
4. Wait for JWTs signed by the old key to expire and downstream [JWKS](https://www.rfc-editor.org/rfc/rfc7517#section-5) caches to
   refresh.
5. Remove the old private key from the exported keyset. Its public key remains
   available until its NATS lease expires.

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
