# DSX identity

DSX Identity turns shard-local SPIRE identities into DSX JWTs that relying
parties can validate across shards. An identity shard contains an independent
SPIRE deployment and a co-located DSX Identity Keys deployment. DSX Identity Keys validates only its
local SPIRE JWT-SVIDs, issues DSX JWTs, and distributes its public signing keys
through the external DSX Event Bus.

There is no SPIRE federation between shards. Cross-shard trust starts after
token exchange, when another shard or relying party validates a DSX JWT with
the aggregate public keys served by any DSX Identity Keys instance.

Component commands and runtime configuration:

- [DSX Identity Controller](cmd/dsx-identity-controller/README.md)
- [DSX IMDS Network Setup](cmd/dsx-imds-network-setup/README.md)
- [`dsx_imds` NodeAttestor agent plugin](cmd/dsx-imds-node-attestor-agent/README.md)
- [`dsx_imds` NodeAttestor server plugin](cmd/dsx-imds-node-attestor-server/README.md)
- [DSX Token Fetcher](cmd/dsx-token-fetcher/README.md)
- [DSX Identity Keys Proxy CSI Driver](cmd/dsx-identity-keys-proxy-csi/README.md)
- [Generate Signing Keyset](cmd/generate-signing-keyset/README.md)
- [DSX IMDS](cmd/metadata-service/README.md)
- [DSX Identity Keys](cmd/dsx-identity-keys/README.md)

Do not use files under `docs/plans` as design authority.

## Architecture

Local SPIRE identities are hierarchical. The DPU identity comes first, the
bare-metal host identity is attested through the DPU, and VM identity is served
from the attested host path.

```mermaid
flowchart TB
  subgraph Shard["Data Hall identity shard"]
    direction TB
    Sources["DPU and LaunchLayer source CRs"]
    Controller["Identity Controller Deployment"]
    SPIRE["Shard-local SPIRE server"]
    DPU["External DPU SPIRE agent"]
    DPUFetcher["dsx-token-fetcher process<br/>actual Workload API caller"]
    DPUProcess["DPU process"]
    DPUToken["protected DSX JWT file"]
    MDS["Paired DPU link-local intercept<br/>NICo or Carbide IMDS passthrough<br/>DSX IMDS (metadata-service)"]
    Host["Operator-owned bare-metal Kubernetes worker<br/>SPIRE agent"]
    Pod["Operator Kubernetes Pod<br/>dsx-token-fetcher sidecar"]
    VMHost["virt-launcher Pod<br/>DSX IMDS (metadata-service) sidecar"]
    VM["KubeVirt VM"]

    Sources --> Controller
    Controller -->|"derived SPIRE entries"| SPIRE
    DPU -->|"dsx_dpu node attestation"| SPIRE
    DPU -->|"Workload API parent"| DPUFetcher
    DPUFetcher -->|"atomic token write"| DPUToken
    DPUToken -->|"capability read"| DPUProcess
    DPU -->|"host SVID selected by"| MDS
    MDS -->|"host DSX identity over IMDS"| Host
    Host -->|"dsx_imds node attestation"| SPIRE
    Host -->|"Workload API parent"| Pod
    Host -->|"same Workload API parent"| VMHost
    VMHost -->|"DSX identity over IMDS"| VM
  end
```

Common Services owns the identity shard for Common Services and Data Hall
control-plane resources. Each Data Hall owns the shard for its capacity
resources. Both shards derive entries from their local DPU and LaunchLayer
source CRs, and their Pods register beneath local `dsx_imds` agents.

```mermaid
%%{init: {"flowchart": {"defaultRenderer": "elk"}, "elk": {"mergeEdges": true, "nodePlacementStrategy": "SIMPLE"}}}%%
flowchart TB
  subgraph CSHost["Operator-owned Common Services bare-metal Kubernetes worker"]
    direction TB
    CSHostDSX["Kubernetes Pod DSX JWT<br/>operator tenant"]
    CSHostPod["operator Kubernetes Pod<br/>dsx-token-fetcher sidecar"]
    CSHostSVIDs["Kubernetes Pod cluster-local SPIRE JWT-SVID"]
    CSHostAgent["host SPIRE agent process<br/>Unix and Kubernetes WorkloadAttestors<br/>dsx_imds NodeAttestor client plugin"]
    CSHostIMDS["host IMDS endpoint"]
    CSHostDeferred["host-process token delivery deferred"]
  end

  subgraph CSDPU["External Common Services DPU"]
    direction TB
    CSDPUDSX["exchanged DSX JWTs<br/>DPU token file: operator tenant<br/>host node attestation: operator tenant"]
    CSDPUFetcher["dsx-token-fetcher process<br/>actual Workload API caller"]
    CSDPUIMDS["paired DPU link-local intercept<br/>NICo or Carbide IMDS passthrough<br/>DSX IMDS (metadata-service)"]
    CSDPUAgent["external DPU SPIRE agent process<br/>Unix WorkloadAttestor<br/>dsx_dpu NodeAttestor client plugin"]
    CSHardware["DPU serial number and SPDM"]
  end

  subgraph CPHost["Operator-owned Data Hall control-plane bare-metal Kubernetes worker"]
    direction TB
    CPHostDSX["Kubernetes Pod DSX JWT<br/>operator tenant"]
    CPHostPod["operator Kubernetes Pod<br/>dsx-token-fetcher sidecar"]
    CPHostSVIDs["Kubernetes Pod cluster-local SPIRE JWT-SVID"]
    CPHostAgent["host SPIRE agent process<br/>Unix and Kubernetes WorkloadAttestors<br/>dsx_imds NodeAttestor client plugin"]
    CPHostIMDS["host IMDS endpoint"]
    CPHostDeferred["host-process token delivery deferred"]
  end

  subgraph CPDPU["External Data Hall control-plane DPU"]
    direction TB
    CPDPUDSX["exchanged DSX JWTs<br/>DPU token file: operator tenant<br/>host node attestation: operator tenant"]
    CPDPUFetcher["dsx-token-fetcher process<br/>actual Workload API caller"]
    CPDPUIMDS["paired DPU link-local intercept<br/>NICo or Carbide IMDS passthrough<br/>DSX IMDS (metadata-service)"]
    CPDPUAgent["external DPU SPIRE agent process<br/>Unix WorkloadAttestor<br/>dsx_dpu NodeAttestor client plugin"]
    CPHardware["DPU serial number and SPDM"]
  end

  subgraph CSCluster["Common Services cluster"]
    direction TB
    CSSources["DPU and LaunchLayer source CRs<br/>Common Services and Data Hall control plane"]
    CSController["DSX Identity Controller<br/>two replicas, leader elected"]
    CSEntries["ClusterStaticEntry CRs"]
    CSPodManager["one logical SPIRE controller-manager<br/>co-located in two SPIRE server Pods<br/>leader elected<br/>operator Pod registrations"]
    CSSPIRE["two Common Services SPIRE server replicas<br/>dsx_dpu and dsx_imds NodeAttestor server plugins"]
    CSIdentityKeys["Common Services DSX Identity Keys"]
  end

  CSSources --> CSController
  CSController --> CSEntries
  CSEntries --> CSSPIRE
  CSPodManager --> CSSPIRE

  CSHostAgent --> CSSPIRE
  CPHostAgent --> CSSPIRE
  CSDPUAgent --> CSSPIRE
  CPDPUAgent --> CSSPIRE

  CSHostAgent -->|"Kubernetes WorkloadAttestor<br/>dsx-pod"| CSHostSVIDs
  CSHostSVIDs --> CSHostPod
  CSHostPod --> CSIdentityKeys
  CSIdentityKeys --> CSHostDSX
  CPHostAgent -->|"Kubernetes WorkloadAttestor<br/>dsx-pod"| CPHostSVIDs
  CPHostSVIDs --> CPHostPod
  CPHostPod --> CSIdentityKeys
  CSIdentityKeys --> CPHostDSX
  CSDPUAgent -->|"Unix WorkloadAttestor<br/>DPU fetcher selectors, dsx-dpu"| CSDPUFetcher
  CSDPUAgent -->|"external DPU runtime selectors<br/>host identity, dsx-imds"| CSDPUIMDS
  CSDPUFetcher --> CSIdentityKeys
  CSDPUIMDS --> CSIdentityKeys
  CSIdentityKeys --> CSDPUDSX
  CPDPUAgent -->|"Unix WorkloadAttestor<br/>DPU fetcher selectors, dsx-dpu"| CPDPUFetcher
  CPDPUAgent -->|"external DPU runtime selectors<br/>host identity, dsx-imds"| CPDPUIMDS
  CPDPUFetcher --> CSIdentityKeys
  CPDPUIMDS --> CSIdentityKeys
  CSIdentityKeys --> CPDPUDSX

  CSHostAgent --> CSHostIMDS
  CSHostIMDS --> CSDPUIMDS
  CPHostAgent --> CPHostIMDS
  CPHostIMDS --> CPDPUIMDS

  subgraph TenantVM["tenant-owned KubeVirt VM"]
    direction TB
    VMWorkload["VM process"]
    VMIMDS["VM IMDS endpoint"]
  end

  subgraph VMHost["Operator-owned Data Hall capacity bare-metal host<br/>KubeVirt worker"]
    direction TB
    VMHostDSX["VM DSX JWT<br/>LaunchLayer CR tenant"]
    VMDSXIMDS["virt-launcher Pod<br/>DSX IMDS (metadata-service) sidecar"]
    VMHostSVIDs["one host Workload API<br/>virt-launcher Pod: dsx-pod, operator tenant<br/>VM: dsx-imds-vm, LaunchLayer CR tenant"]
    VMHostAgent["operator host SPIRE agent process<br/>Unix and Kubernetes WorkloadAttestors<br/>dsx_imds NodeAttestor client plugin"]
    VMHostIMDS["host IMDS endpoint"]
    VMHostDeferred["host-process token delivery deferred"]
  end

  subgraph VMDPU["External Data Hall capacity DPU<br/>paired with an operator KubeVirt worker"]
    direction TB
    VMDPUDSX["exchanged DSX JWTs<br/>DPU token file: operator tenant<br/>host node attestation: operator tenant"]
    VMDPUFetcher["dsx-token-fetcher process<br/>actual Workload API caller"]
    VMDPUIMDS["paired DPU link-local intercept<br/>NICo or Carbide IMDS passthrough<br/>DSX IMDS (metadata-service)"]
    VMDPUAgent["external DPU SPIRE agent process<br/>Unix WorkloadAttestor<br/>dsx_dpu NodeAttestor client plugin"]
    VMHardware["DPU serial number and SPDM"]
  end

  subgraph BMHost["Standalone tenant-facing Data Hall capacity bare-metal host"]
    direction TB
    BMHostIdentity["host agent identity<br/>LaunchLayer CR tenant"]
    BMHostAgent["host SPIRE agent process<br/>Unix WorkloadAttestor<br/>dsx_imds NodeAttestor client plugin"]
    BMHostIMDS["host IMDS endpoint"]
    BMHostDeferred["host-process token delivery deferred"]
  end

  subgraph BMDPU["External Data Hall capacity DPU<br/>paired with a standalone tenant host"]
    direction TB
    BMDPUDSX["exchanged DSX JWTs<br/>DPU token file: operator tenant<br/>host node attestation: LaunchLayer CR tenant"]
    BMDPUFetcher["dsx-token-fetcher process<br/>actual Workload API caller"]
    BMDPUIMDS["paired DPU link-local intercept<br/>NICo or Carbide IMDS passthrough<br/>DSX IMDS (metadata-service)"]
    BMDPUAgent["external DPU SPIRE agent process<br/>Unix WorkloadAttestor<br/>dsx_dpu NodeAttestor client plugin"]
    BMHardware["DPU serial number and SPDM"]
  end

  subgraph DHCluster["Data Hall control-plane cluster"]
    direction TB
    DHSources["DPU and LaunchLayer source CRs<br/>Data Hall capacity"]
    DHController["DSX Identity Controller<br/>two replicas, leader elected"]
    DHEntries["ClusterStaticEntry CRs"]
    DHPodManager["one logical SPIRE controller-manager<br/>co-located in two SPIRE server Pods<br/>leader elected<br/>operator Pod registrations"]
    DHSPIRE["two Data Hall SPIRE server replicas<br/>dsx_dpu and dsx_imds NodeAttestor server plugins"]
    DHIdentityKeys["Data Hall DSX Identity Keys"]
    DHProxy["DSX Identity Keys Proxy CSI<br/>node plugin Pod"]
  end

  DHSources --> DHController
  DHController --> DHEntries
  DHEntries --> DHSPIRE
  DHPodManager --> DHSPIRE

  VMHostAgent --> DHSPIRE
  BMHostAgent --> DHSPIRE
  VMDPUAgent --> DHSPIRE
  BMDPUAgent --> DHSPIRE

  VMHostAgent -->|"Kubernetes WorkloadAttestor<br/>distinct dsx-pod and dsx-imds-vm hints"| VMHostSVIDs
  VMHostSVIDs --> VMDSXIMDS
  VMDSXIMDS --> DHProxy
  DHProxy --> DHIdentityKeys
  DHIdentityKeys --> VMHostDSX
  VMDPUAgent -->|"Unix WorkloadAttestor<br/>DPU fetcher selectors, dsx-dpu"| VMDPUFetcher
  VMDPUAgent -->|"external DPU runtime selectors<br/>host identity, dsx-imds"| VMDPUIMDS
  VMDPUFetcher --> DHIdentityKeys
  VMDPUIMDS --> DHIdentityKeys
  DHIdentityKeys --> VMDPUDSX
  BMDPUAgent -->|"Unix WorkloadAttestor<br/>DPU fetcher selectors, dsx-dpu"| BMDPUFetcher
  BMDPUAgent -->|"external DPU runtime selectors<br/>host identity, dsx-imds"| BMDPUIMDS
  BMDPUFetcher --> DHIdentityKeys
  BMDPUIMDS --> DHIdentityKeys
  DHIdentityKeys --> BMDPUDSX

  VMHostAgent --> VMHostIMDS
  VMHostIMDS --> VMDPUIMDS
  BMHostAgent --> BMHostIMDS
  BMHostIMDS --> BMDPUIMDS
  BMHostAgent --> BMHostIdentity
  VMWorkload --> VMIMDS
  VMIMDS --> VMDSXIMDS

```

The parent association is an attested SPIRE agent, not a resource-ownership
hierarchy. DPUs are external hardware and are not Kubernetes worker nodes.
Every Kubernetes worker is an operator-owned bare-metal host, and every Pod
uses the configured operator tenant and a `/host/<node-name>/pod/<pod-uid>`
subject. A tenant-facing bare-metal host is standalone and never becomes a
Kubernetes node. A KubeVirt VM runs in an operator-owned virt-launcher Pod, so
the Pod keeps its operator identity while its metadata sidecar selects the
dynamically tenant-supplied VM SVID from the same operator host Workload API.

Local consumers exchange local SPIRE JWT-SVIDs with their shard's DSX Identity Keys for
DSX JWTs. DSX Identity Keys instances share only public signing keys across shards.

```mermaid
flowchart LR
  Local["dsx-token-fetcher and DSX IMDS"]
  IdentityKeys["DSX Identity Keys Deployment"]
  Consumers["DPU, host, Pod, and VM consumers"]

  Local -->|"local SPIRE JWT-SVID"| IdentityKeys
  IdentityKeys -->|"DSX JWT"| Consumers
  IdentityKeys <-->|"NATS KV updates and watch replay"| Bus["External DSX Event Bus"]
  Bus <-->|"NATS KV updates and watch replay"| Peers["Peer shard DSX Identity Keys instances"]
  RelyingParty["External relying party"] -->|"aggregate JWKS"| IdentityKeys
```

A SPIRE JWT-SVID is a cluster-local credential obtained from the local SPIRE
Workload API. A DSX JWT is the credential returned by the local DSX Identity Keys.
SPIRE JWT-SVIDs never establish trust between shards.

Install the identity chart once per shard. Each regular management cluster that
owns DPU or LaunchLayer source CRs runs the Identity Controller for its shard.
Every DSX Identity Keys instance validates only local SPIRE JWT-SVIDs through its node-local
Workload API and serves the aggregate public keys received through the Event
Bus.

The Event Bus, OAuth2 provider, Kyverno runtime, and relying parties are
external to the identity chart.

### Identity delivery

| Consumer | SPIRE trust path | DSX JWT delivery |
|---|---|---|
| [Kubernetes Pod](cmd/dsx-token-fetcher/README.md) | Kubernetes WorkloadAttestor under the worker's `dsx_imds`-attested host agent. The registration uses the operator tenant and worker host subject | The `dsx-token-fetcher` sidecar selects `dsx-pod`, exchanges the local SPIRE JWT-SVID, and writes the DSX JWT to a Pod-private volume mounted read-only in application containers |
| [DPU process](cmd/dsx-token-fetcher/README.md) | The external DPU agent attests through the `dsx_dpu` NodeAttestor agent and server. For token-file delivery, Unix WorkloadAttestor selectors bind the base DPU SVID to the `dsx-token-fetcher` process because it is the Workload API caller | `dsx-token-fetcher` selects `dsx-dpu`, exchanges the local SPIRE JWT-SVID, and writes a protected token file. The DPU process consumes that file as a capability |
| [Bare-metal host process](cmd/dsx-imds-node-attestor-agent/README.md) | The DPU agent is attested through `dsx_dpu`. The host agent is attested through the repository `dsx_imds` NodeAttestor agent and server, then can serve local processes through the Unix WorkloadAttestor | The paired DPU intercept carries link-local IMDS to NICo or Carbide IMDS, which passes identity requests to DSX IMDS. DPU-hosted DSX IMDS exchanges directly with its reachable shard-local DSX Identity Keys. The host agent uses the returned DSX JWT for node attestation. A host-process-specific token delivery contract is not currently defined |
| [KubeVirt VM](cmd/metadata-service/README.md) | The controller derives the VM subject from the dynamically supplied tenant ID and parents it to the operator-owned worker host's `dsx_imds` agent. The Kubernetes WorkloadAttestor serves the operator-owned virt-launcher Pod, and SPIFFE CSI exposes that same agent's Workload API socket | The DSX IMDS sidecar selects the VM SPIRE JWT-SVID by its exact `dsx-imds-vm` hint, exchanges it through DSX Identity Keys Proxy CSI, and serves the DSX JWT through IMDS |

## Helm chart

Install `charts/dsx-identity` once per identity shard. Install the official
SPIRE CRD chart first. The identity chart installs SPIRE and can install the
repository-owned DSX Identity Keys, controller, metadata, CSI, and identity policies.

Each installation supplies its shard identity, external Event Bus connection,
OAuth2 client, and signing keyset.

The chart defaults DSX Identity Keys and the Identity Controller to two active replicas.
SPIRE server deployment and datastore settings pass through to the official
SPIRE chart.

```yaml
global:
  operatorTenant: operator-1
  spire:
    trustDomain: shard.example.com
    clusterName: shard-a
    jwtIssuer: spiffe://shard.example.com

spire:
  spire-server:
    controllerManager:
      extraEnv:
        - name: OPERATOR_TENANT
          value: operator-1

identityKeys:
  shardID: shard-a
  jwksURL: https://identity-keys.shard.example.com/.well-known/jwks.json
  signingKeysetSecret: dsx-identity-keys-signing-keyset
  activeKIDSecret: dsx-identity-keys-active-kid
  nats:
    serverURL: tls://event-bus.example.com:4222
    kv:
      bucket: DSX_IDENTITY_KEYS
      replicas: 3
    auth:
      mode: oauth2
      credentialsSecret: dsx-identity-keys-nats-oauth
      credentialsKey: credentials.json
      issuerURL: https://auth.example.com
```

Each identity shard configures the `dsx_imds` server plugin in SPIRE and exposes
the external host agent's canonical socket through the single SPIFFE CSI
driver. The plugin validates host bootstrap DSX JWTs against the shard's
aggregate JWKS served by DSX Identity Keys.

```yaml
spire:
  spire-agent:
    enabled: false
  spire-server:
    initContainers:
      - name: install-dsx-imds-node-attestor-server
        image: nvcr.io/0837451325059433/components-dev/dsx-imds-node-attestor-server:<chart-version>
        args:
          - /plugins/dsx-imds-node-attestor-server
        volumeMounts:
          - name: dsx-imds-server-plugin
            mountPath: /plugins
    extraVolumes:
      - name: dsx-imds-server-plugin
        emptyDir: {}
    extraVolumeMounts:
      - name: dsx-imds-server-plugin
        mountPath: /opt/spire/plugins
        readOnly: true
    customPlugins:
      nodeAttestor:
        dsx_imds:
          plugin_cmd: /opt/spire/plugins/dsx-imds-node-attestor-server
          plugin_checksum: ""
          plugin_data:
            trust_domain: shard.example.com
            audience: spiffe://shard.example.com/spire/server
            jwks_url: https://identity-keys.shard.example.com/.well-known/jwks.json
  spiffe-csi-driver:
    enabled: true
    pluginName: csi.spiffe.io
    agentSocketPath: /run/spire/agent-sockets/spire-agent.sock
```

The host agent and its `dsx_imds` agent plugin are provisioned with the host,
outside the identity chart. An ordinary in-cluster agent is disabled by
default. If a separate node class needs one, enable and constrain it so it
cannot share a node with the host agent.

Identity shards that reconcile DPU or LaunchLayer sources enable the controller.
Data Hall capacity shards also enable the VM identity path. The two DPU
selector sets configure distinct WorkloadAttestor contracts.

```yaml
podTokenFetcher:
  enabled: true
  identityKeysURL: https://identity-keys.shard.example.com/v1/exchange

vmSupport:
  enabled: true
  identityKeysProxy:
    target: TCP:dsx-identity-keys.dsx-identity.svc.cluster.local:8080

identityController:
  enabled: true
  dpuTokenFetcherSelectors:
    - unix:uid:32001
    - unix:path:/usr/local/bin/dsx-token-fetcher
    - unix:sha256:<packaged-binary-digest>
  dpuMetadataSelectors:
    - <selector for DPU-hosted metadata-service>
```

Path and SHA-256 selectors require `discover_workload_path = true` in the
external DPU agent's Unix WorkloadAttestor configuration. The fetcher runs
under the dedicated UID, and the executable is root-owned and immutable.

The referenced Secrets must exist before their consumers start. Controller
deployments require the external DPU and LaunchLayer APIs. Identity injection
requires Kyverno. Tracing requires the DSX observability stack. ServiceMonitor
resources require the Prometheus Operator APIs.

Kyverno is the admission boundary, not a source of identity data. The identity
chart installs fail-closed policies before protected registrations. Configured
deployment subjects may manage release `ClusterSPIFFEID` objects, the Identity
Controller may manage derived `ClusterStaticEntry` objects, and the official
controller-manager may update status and finalizers. The controller is the sole
writer and validator for derived static entries. Admission validates release
registration shapes and prevents application containers from mounting the
Workload API or DSX Identity Keys Proxy sockets.

## Identity contract

DSX JWT subjects use:

```text
spiffe://<tenant>.ds9.dsx.nvid.id/<dpu|host|vm>/<resource-id>
  [/process/<name>][/role/<role>]
spiffe://<operator-tenant>.ds9.dsx.nvid.id/host/<node-name>/pod/<pod-uuid>
  [/role/<role>]
```

Cluster-local SPIRE identities encode the same tenant, service, resource type,
and resource ID under the shard trust domain.

```text
spiffe://<local-trust-domain>/tenant/<tenant>/service/dsx/<resource-type>/<resource-id>[/...]
```

| Resource | Tenant source | Subject resource path |
|---|---|---|
| DPU | `global.operatorTenant` | `/dpu/<dpu-id>` |
| Operator bare-metal Kubernetes worker | `global.operatorTenant` | `/host/<node-name>` |
| Kubernetes Pod | `global.operatorTenant` | `/host/<node-name>/pod/<pod-uid>` |
| Bare-metal host with a LaunchLayer source | Source CR `tenantID`, supplied dynamically | `/host/<host-id>` |
| VM with a LaunchLayer source | Source CR `tenantID`, supplied dynamically | `/vm/<vm-id>` |

Every Kubernetes worker is an operator-owned bare-metal host. There is no
configured worker list and no LaunchLayer source for these hosts. The Pod
registration template derives the host ID directly from the Kubernetes node
name. Physical worker-to-DPU pairing belongs to the external NICo and DPU
integration boundary, not the Identity Controller configuration.

Every DSX Kubernetes Pod supplies the service label. One SPIRE
controller-manager derives its operator tenant, host resource type, node name,
and Pod UID. Every worker is an operator-owned bare-metal host, so the fixed
parent template identifies that worker's operator-tenant `dsx_imds` agent.

```yaml
metadata:
  labels:
    spiffe.nvidia.com/service: dsx
    spiffe.nvidia.com/role: database  # optional
```

```yaml
spire:
  spire-server:
    controllerManager:
      extraEnv:
        - name: OPERATOR_TENANT
          value: operator-1
      parentIDTemplate: 'spiffe://{{ .TrustDomain }}/spire/agent/dsx_imds/tenant/$OPERATOR_TENANT/host/{{ .NodeMeta.Name }}'
```

The chart requires the controller-manager value to equal
`global.operatorTenant`. Because it is part of the SPIRE Pod template, changing
both values causes the StatefulSet to roll out without a restart hook.

DPU identities remain separate controller-derived static entries parented to
the external `dsx_dpu` agent. DPUs are not Kubernetes nodes and do not
participate in Pod registration. The
[DSX Token Fetcher](cmd/dsx-token-fetcher/README.md) obtains and exchanges the
DPU SPIRE JWT-SVID, then writes the DSX JWT to a protected file.

The [DSX Identity Controller](cmd/dsx-identity-controller/README.md) derives
SPIRE registration entries from DPU and LaunchLayer sources. Until LaunchLayer
publishes its final API, the repository uses a temporary
`LaunchLayerSourceFixture` with only the fields shown below.

```yaml
spec:
  type: vm                             # host or vm
  tenantID: tenant-2       # supplied dynamically by this source
  resourceID: vm-1234
  parentID: host-1234                  # DPU ID for host, operator worker host ID for VM
  role: database                       # optional for host or VM
  holder:
    namespace: tenant-tenant1
    serviceAccount: vm-identity-client
    podUID: <pod-uuid>                 # optional
```

The temporary source has no `parentTenantID`. The configured operator tenant
determines a VM entry's operator-worker parent. It also has no SPIFFE IDs,
selectors, hints, or class names because the Identity Controller derives those
registration outputs.

## DSX Identity Keys and Event Bus

[DSX Identity Keys](cmd/dsx-identity-keys/README.md) validates
local SPIRE JWT-SVIDs, issues DSX JWTs, publishes public signing keys to the
Event Bus, and serves aggregate JWKS. The external Gateway terminates TLS and
applies source-aware request quotas. Clients configure the Gateway HTTPS
endpoint. Local fixtures configure their HTTP endpoints directly.

## DSX IMDS host and VM flows

[DSX IMDS](cmd/metadata-service/README.md) serves the tokenless host and VM
identity API. The
[`dsx_imds` NodeAttestor agent](cmd/dsx-imds-node-attestor-agent/README.md) and
[`dsx_imds` NodeAttestor server](cmd/dsx-imds-node-attestor-server/README.md)
bootstrap the host SPIRE agent. The
[DSX Identity Keys Proxy CSI Driver](cmd/dsx-identity-keys-proxy-csi/README.md) carries VM
exchange requests, and
[DSX IMDS Network Setup](cmd/dsx-imds-network-setup/README.md) routes the VM's
link-local metadata traffic.

## Observability

First-party Go processes emit JSON logs to stdout or stderr. Chart-owned metric
endpoints support Prometheus discovery. Injected processes export traces
through the SDK and sidecar collector installed by the DSX observability stack.
Structured allow, deny, and dependency events never include credentials.

```yaml
observability:
  logLevel: info
  metrics:
    enabled: true
    serviceMonitor:
      enabled: true
```

Tracing adds the SDK and sidecar annotations shown below to chart-owned
application Pods and the SPIRE server Pod. Repository-owned SPIRE server
plugins inherit the injected server-container environment.

```yaml
metadata:
  annotations:
    instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
    sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```

Pods that receive an identity container supply the same tracing annotations on
their Pod template. For token-fetcher injection:

```yaml
metadata:
  labels:
    spiffe.nvidia.com/service: dsx
  annotations:
    instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
    sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```

For KubeVirt IMDS injection, put them on the VM Pod template:

```yaml
spec:
  template:
    metadata:
      annotations:
        spiffe.nvidia.com/inject-dsx-imds: "true"
        instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
        sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```

See the
[DSX service observability guide](https://gitlab-master.nvidia.com/ncp/dsx/dsx-docs/-/blob/9aa5ed1e74f2cd33261bd05e118372ef220c0aba/docs/dev-environments/observability-guide.md).

## Local E2E

The repository validates the architecture with two Kind clusters. k1 hosts a
Common Services identity shard, one unauthenticated NATS server with in-memory
JetStream for KV, and the
OpenBao relying party. k2 hosts a Data Hall identity shard, the Identity
Controller, OVN-Kubernetes layer-3 UDN, and a KubeVirt VM. The k1 and k2 names
describe only this fixture and do not define a trust hierarchy. Local identity
components use one replica and SPIRE uses its SQLite default.

The identity chart is installed independently in both clusters. The remaining
components are separate Skaffold modules. Shared fixture settings and the
shard-specific overrides are in:

- `scripts/identity-e2e-values.yaml`
- `scripts/k1-values.yaml`
- `scripts/k2-values.yaml`

### E2E-only `dsx_imds` bootstrap

Kind starts without an external DPU or an attested worker agent. Before the
worker agent is available, the control-plane node represents the Phase 0 seed
host. Each identity chart is installed once. Its normal DSX Identity Keys Pod runs on
the seed node and uses the ordinary operator-Pod registration there while the
worker's `dsx_imds` agent attests. DSX Identity Keys has no bootstrap-specific identity
or deployment path.

Both local shards must exercise host attestation without physical DPU hardware.
Each therefore runs an official SPIRE agent DaemonSet constrained to its worker
as the external host-agent fixture. The ordinary shard agent remains on the
control-plane node as the emulated external DPU boundary. One SPIFFE CSI driver
exposes each node's canonical Workload API socket. The worker agent's
`dsx_imds` plugin obtains a DSX JWT from DSX IMDS and submits it to the
repository-owned `dsx_imds` server plugin for node attestation. k2 then reuses
that worker agent for its KubeVirt VM path.

The local fixture exposes the real DSX IMDS implementation directly at the
external intercept and passthrough boundary. E2E sends the IMDS request,
exchanges the selected SPIRE JWT-SVID, and uses the returned DSX JWT for
`dsx_imds` node attestation. It does not test physical link-local routing or
NICo or Carbide integration.

The ordinary-agent entry is the permitted fixture at the external `dsx_dpu`
boundary. E2E requests the operator-host identity from IMDS and attests the
worker's `dsx_imds` SPIRE agent with it. The fixture derives the operator host
ID from the worker name without creating a controller input for that worker.

This external-agent fixture uses the shared
`scripts/dsx-imds-host-agent-values.yaml` values and a shard override under
`scripts/k1` or `scripts/k2`. It is not part of the identity chart release.

```mermaid
flowchart TB
  DPUAgent -->|"Workload API socket"| CSI
  CSI -->|"host SPIRE JWT-SVID"| DPUMetadata
  DPUMetadata -->|"direct RFC 8693 exchange"| IdentityKeys
  IdentityKeys -->|"host DSX JWT"| DPUMetadata
  Agent -->|"invokes"| Plugin
  Plugin -->|"IMDS identity request"| DPUMetadata
  DPUMetadata -->|"host DSX JWT"| Plugin
  Plugin -->|"attestation data"| Agent
  Agent -->|"submit DSX JWT"| Server
  Server -->|"node SVID"| Agent

  DPUAgent["Ordinary k2 SPIRE agent process<br/>E2E DPU boundary"]
  CSI["SPIFFE CSI<br/>node plugin Pod"]
  DPUMetadata["E2E DSX IMDS<br/>(metadata-service)"]
  Agent["Bare-metal worker<br/>dsx_imds SPIRE agent process"]
  Plugin["dsx_imds NodeAttestor<br/>agent plugin"]
  IdentityKeys["k2 DSX Identity Keys Pod"]
  Server["k2 SPIRE server Pod<br/>dsx_imds server plugin"]
```

```mermaid
flowchart TB
  Agent["Attested dsx_imds<br/>SPIRE agent process"]
  CSI["Attested SPIFFE CSI<br/>node plugin Pod"]
  VirtLauncher["KubeVirt virt-launcher Pod<br/>DSX IMDS (metadata-service) sidecar"]
  Guest["KubeVirt VM guest"]

  Agent -->|"Workload API socket"| CSI
  CSI -->|"mounted socket"| VirtLauncher
  Guest -->|"IMDS over layer-3 UDN"| VirtLauncher
```

The KubeVirt VM contains no SPIRE agent. SPIFFE CSI exposes the attested second
agent's Workload API socket to the virt-launcher Pod, and DSX IMDS uses that
socket to fetch the VM SPIRE JWT-SVID.

### Cross-shard OpenBao validation

The local E2E topology deploys OpenBao in k1 as an external relying party. A VM
DSX JWT issued in k2 is accepted because the k2 DSX Identity Keys public key propagates
through the Event Bus and appears in the k1 DSX Identity Keys aggregate JWKS.

```mermaid
sequenceDiagram
  participant Test as E2E test process
  participant Gateway as Envoy Gateway<br/>data-plane Pod in k1
  participant Bao as OpenBao Pod in k1
  participant JWKS as k1 DSX Identity Keys Pod<br/>aggregate JWKS endpoint

  Test->>Gateway: JWT CEL login with k2 VM DSX JWT
  Gateway->>Bao: Forward login request
  Bao->>JWKS: Fetch aggregate JWKS
  JWKS-->>Bao: Public keyset including the k2 kid
  Bao-->>Gateway: Client token and policies
  Gateway-->>Test: Return login response
  Test->>Gateway: Read allowed and forbidden secrets
  Gateway->>Bao: Forward secret requests
  Bao-->>Gateway: Values or 403
  Gateway-->>Test: Return responses
```

### Workflow

Repository validation requires Git, mise, Make, and `curl`. Mise installs the
repository-pinned tools. The full E2E lifecycle also requires Docker with
Buildx, OpenSSL, OpenSSH, and a supported Linux host with the kernel features
needed by OVN-Kubernetes and KubeVirt. WSL is unsupported.

```bash
git submodule update --init --recursive
make validate
```

One root Skaffold workflow deploys both Kind clusters.

```bash
make -j install
make test
make clean
```

The E2E suite verifies operator-host IMDS through both `dsx_imds` plugin halves,
dynamic standalone-host registration, DPU file delivery, Pod and VM identity
delivery, shard isolation, NATS KV propagation, aggregate JWKS behavior,
OpenBao authorization, restart recovery, and signing-key rotation. External
DPU and standalone-host hardware is not modeled as Kubernetes Nodes.
