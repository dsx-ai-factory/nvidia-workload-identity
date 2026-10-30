# Architecture

Local SPIRE identities are hierarchical. The DPU identity comes first, the
bare-metal host identity is attested through the DPU, and VM identity is served
from the attested host path. The bundled `dpu_hw` server accepts
the DPU serial as plaintext attestation data.

```mermaid
flowchart TB
  subgraph Shard["Data Hall identity shard"]
    direction TB
    Sources["DPU and LaunchLayer source CRs"]
    Controller["Identity Controller Deployment"]
    SPIRE["Shard-local SPIRE server"]
    DPU["External DPU SPIRE agent"]
    MDS["Paired DPU link-local intercept<br/>NICo IMDS passthrough<br/>DSX IMDS (metadata-service)"]
    Host["Operator-owned bare-metal Kubernetes worker<br/>SPIRE agent"]
    Pod["Operator Kubernetes Pod<br/>dsx-token-fetcher sidecar"]
    VMHost["virt-launcher Pod<br/>DSX IMDS (metadata-service) sidecar"]
    VM["KubeVirt VM"]

    Sources --> Controller
    Controller -->|"static entries via SPIRE controller-manager"| SPIRE
    DPU -->|"dpu_hw node attestation"| SPIRE
    DPU -->|"Kubernetes workload attestation"| MDS
    MDS -->|"host DSX JWT over IMDS"| Host
    Host -->|"dsx_imds node attestation"| SPIRE
    Host -->|"Workload API parent"| Pod
    Host -->|"same Workload API parent"| VMHost
    VMHost -->|"DSX JWT over IMDS"| VM
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
    CSHostPod["operator Kubernetes Pod<br/>dsx-token-fetcher and application"]
    CSHostAgent["host SPIRE agent<br/>dsx_imds node attestation<br/>Unix and Kubernetes workload attestation"]
  end

  subgraph CSDPU["External Common Services DPU"]
    direction TB
    CSDPUIMDS["paired DPU IMDS<br/>link-local intercept<br/>NICo passthrough<br/>metadata-service"]
    CSDPUAgent["external DPU SPIRE agent<br/>dpu_hw node attestation<br/>Kubernetes workload attestation"]
  end

  subgraph CPHost["Operator-owned Data Hall control-plane bare-metal Kubernetes worker"]
    direction TB
    CPHostPod["operator Kubernetes Pod<br/>dsx-token-fetcher and application"]
    CPHostAgent["host SPIRE agent<br/>dsx_imds node attestation<br/>Unix and Kubernetes workload attestation"]
  end

  subgraph CPDPU["External Data Hall control-plane DPU"]
    direction TB
    CPDPUIMDS["paired DPU IMDS<br/>link-local intercept<br/>NICo passthrough<br/>metadata-service"]
    CPDPUAgent["external DPU SPIRE agent<br/>dpu_hw node attestation<br/>Kubernetes workload attestation"]
  end

  subgraph CSCluster["Common Services cluster"]
    direction TB
    CSSources["DPU and LaunchLayer source CRs<br/>Common Services and Data Hall control plane"]
    CSController["NVIDIA Workload Identity<br/>Controller<br/>leader elected"]
    CSEntries["ClusterStaticEntry CRs"]
    CSPodManager["SPIRE controller-manager<br/>static entries and Pod registrations"]
    CSSPIRE["Common Services SPIRE server<br/>dpu_hw and dsx_imds server plugins"]
    CSIdentityKeys["Common Services<br/>NVIDIA Workload Identity<br/>Keys"]
  end

  CSSources --> CSController
  CSController --> CSEntries
  CSEntries --> CSPodManager
  CSPodManager --> CSSPIRE

  CSHostAgent --> CSSPIRE
  CPHostAgent --> CSSPIRE
  CSDPUAgent --> CSSPIRE
  CPDPUAgent --> CSSPIRE

  CSHostAgent -->|"dsx-pod JWT-SVID"| CSHostPod
  CSHostAgent <-->|"IMDS host attestation JWT"| CSDPUIMDS
  CSHostPod <-->|"JWT-SVID / DSX JWT"| CSIdentityKeys
  CPHostAgent -->|"dsx-pod JWT-SVID"| CPHostPod
  CPHostAgent <-->|"IMDS host attestation JWT"| CPDPUIMDS
  CPHostPod <-->|"JWT-SVID / DSX JWT"| CSIdentityKeys
  CSDPUAgent -->|"Kubernetes WorkloadAttestor<br/>DPU IMDS selectors, dsx-imds"| CSDPUIMDS
  CSDPUIMDS <-->|"JWT-SVID / DSX JWT"| CSIdentityKeys
  CPDPUAgent -->|"Kubernetes WorkloadAttestor<br/>DPU IMDS selectors, dsx-imds"| CPDPUIMDS
  CPDPUIMDS <-->|"JWT-SVID / DSX JWT"| CSIdentityKeys

  VMWorkload["Tenant-owned KubeVirt VM"]

  subgraph VMHost["Operator-owned Data Hall capacity bare-metal host<br/>KubeVirt worker"]
    direction TB
    VMDSXIMDS["virt-launcher Pod<br/>DSX IMDS (metadata-service) sidecar"]
    VMHostAgent["operator host SPIRE agent<br/>dsx_imds node attestation<br/>Unix and Kubernetes workload attestation"]
  end

  subgraph VMDPU["External Data Hall capacity DPU<br/>paired with an operator KubeVirt worker"]
    direction TB
    VMDPUIMDS["paired DPU IMDS<br/>link-local intercept<br/>NICo passthrough<br/>metadata-service"]
    VMDPUAgent["external DPU SPIRE agent<br/>dpu_hw node attestation<br/>Kubernetes workload attestation"]
  end

  subgraph BMHost["Standalone tenant-facing Data Hall capacity bare-metal host"]
    direction TB
    BMHostAgent["host SPIRE agent<br/>LaunchLayer CR tenant<br/>dsx_imds node attestation<br/>Unix workload attestation"]
  end

  subgraph BMDPU["External Data Hall capacity DPU<br/>paired with a standalone tenant host"]
    direction TB
    BMDPUIMDS["paired DPU IMDS<br/>link-local intercept<br/>NICo passthrough<br/>metadata-service"]
    BMDPUAgent["external DPU SPIRE agent<br/>dpu_hw node attestation<br/>Kubernetes workload attestation"]
  end

  subgraph DHCluster["Data Hall control-plane cluster"]
    direction TB
    DHSources["DPU and LaunchLayer source CRs<br/>Data Hall capacity"]
    DHController["NVIDIA Workload Identity<br/>Controller<br/>leader elected"]
    DHEntries["ClusterStaticEntry CRs"]
    DHPodManager["SPIRE controller-manager<br/>static entries and Pod registrations"]
    DHSPIRE["Data Hall SPIRE server<br/>dpu_hw and dsx_imds server plugins"]
    DHIdentityKeys["Data Hall<br/>NVIDIA Workload Identity<br/>Keys"]
    DHProxy["NVIDIA Workload Identity<br/>Keys Proxy CSI<br/>node plugin Pod"]
  end

  DHSources --> DHController
  DHController --> DHEntries
  DHEntries --> DHPodManager
  DHPodManager --> DHSPIRE

  VMHostAgent --> DHSPIRE
  BMHostAgent --> DHSPIRE
  VMDPUAgent --> DHSPIRE
  BMDPUAgent --> DHSPIRE

  VMDSXIMDS <-->|"JWT-SVID / DSX JWT"| DHProxy
  DHProxy <--> DHIdentityKeys
  VMDPUAgent -->|"Kubernetes WorkloadAttestor<br/>DPU IMDS selectors, dsx-imds"| VMDPUIMDS
  VMDPUIMDS <-->|"JWT-SVID / DSX JWT"| DHIdentityKeys
  BMDPUAgent -->|"Kubernetes WorkloadAttestor<br/>DPU IMDS selectors, dsx-imds"| BMDPUIMDS
  BMDPUIMDS <-->|"JWT-SVID / DSX JWT"| DHIdentityKeys

  VMHostAgent -->|"dsx-imds-vm JWT-SVID<br/>LaunchLayer CR tenant"| VMDSXIMDS
  VMHostAgent <-->|"IMDS host attestation JWT"| VMDPUIMDS
  BMHostAgent <-->|"IMDS host attestation JWT"| BMDPUIMDS
  VMWorkload <-->|"IMDS VM JWT"| VMDSXIMDS
```

The parent association is an attested SPIRE agent, not a resource-ownership
hierarchy. DPUs are Kubernetes workers in their Kamaji clusters, not in this
management cluster.
Every Kubernetes worker is an operator-owned bare-metal host, and every Pod
uses the configured operator tenant and a `/bm/<node-name>/pod/<pod-uid>`
subject. A tenant-facing bare-metal host is standalone and never becomes a
Kubernetes node. A KubeVirt VM runs in an operator-owned virt-launcher Pod, so
the Pod keeps its operator identity while its metadata sidecar selects the
dynamically tenant-supplied VM SVID from the same operator host Workload API.

## Registration parentage

The NVIDIA Workload Identity Controller owns DPU-derived, standalone host, and
VM `ClusterStaticEntry` CRs. The SPIRE controller-manager registers those
entries and registers Pods directly from the chart-owned `ClusterSPIFFEID`
template.

Hexagons show inputs, stadiums show attested parent agent IDs, and rectangles
show static CRs or Pod registration entries. `{service}` comes from
`global.dsx.defaultServiceName` for static CRs and the
`spiffe.nvidia.com/service` label for Pods.

```mermaid
%%{init: {"flowchart": {"defaultRenderer": "elk"}, "elk": {"nodePlacementStrategy": "SIMPLE"}}}%%
flowchart TB

  subgraph DPUParented["Entries parented to a DPU agent ID"]
    direction LR

    subgraph DPULane["DPU-paired operator bare-metal host"]
      direction TB
      DPUCR{{"INPUT CR<br/>kind: DPU<br/>BM id: carbide.nvidia.com/dpu-machine-id label<br/>agent serial: spec.serialNumber"}}
      DPUParent(["PARENT AGENT ID<br/>type: dpu_hw<br/>spiffe://{td}/spire/agent/dpu_hw/{normalized spec.serialNumber}"])
      DPUEntry["STATIC ENTRY<br/>kind: ClusterStaticEntry<br/>owner: NVIDIA Workload Identity Controller<br/>subject: /tenant/{operator}/service/{service}/bm/{dpu-machine-id}<br/>hint: dsx-imds<br/>selectors: DPU IMDS Kubernetes selectors"]
      DPUCR -->|"derives subject"| DPUEntry
      DPUParent -->|"spec.serialNumber"| DPUEntry
    end

    subgraph HostLane["Standalone tenant-facing bare-metal host"]
      direction TB
      HostSource{{"INPUT CR<br/>kind: BareMetalInstance<br/>tenant: org-id annotation<br/>BM id: spec.instance_id<br/>DPU id and serial: status.primary_dpu"}}
      HostParent(["PARENT AGENT ID<br/>type: dpu_hw<br/>spiffe://{td}/spire/agent/dpu_hw/{normalized DPU serial}"])
      HostEntry["STATIC ENTRY<br/>kind: ClusterStaticEntry<br/>owner: NVIDIA Workload Identity Controller<br/>subject: /tenant/{org-id}/service/{service}/bm/{spec.instance_id}<br/>hint: dsx-imds<br/>selectors: DPU IMDS Kubernetes selectors"]
      HostSource -->|"derives subject"| HostEntry
      HostParent -->|"status.primary_dpu.serial_number"| HostEntry
    end
  end

  subgraph HostParented["Entries parented to an operator worker dsx_imds agent ID"]
    direction LR

    subgraph PodLane["Operator Kubernetes Pod"]
      direction TB
      PodTemplate{{"INPUT TEMPLATE CR<br/>kind: ClusterSPIFFEID<br/>owner: identity chart"}}
      PodObject{{"MATCHED OBJECT<br/>kind: Pod<br/>label: spiffe.nvidia.com/service exists"}}
      PodParent(["PARENT AGENT ID<br/>type: dsx_imds<br/>spiffe://{td}/spire/agent/dsx_imds/tenant/{operator}/bm/{Pod spec.nodeName}"])
      PodEntry["SPIRE REGISTRATION ENTRY<br/>registered by: SPIRE controller-manager<br/>subject: /tenant/{operator}/service/{service}/bm/{Pod spec.nodeName}/pod/{Pod metadata.uid}[/role/{role}]<br/>hint: dsx-pod<br/>selectors: matched Kubernetes Pod"]
      PodTemplate -->|"supplies templates"| PodEntry
      PodObject -->|"supplies metadata"| PodEntry
      PodParent -->|"parentIDTemplate"| PodEntry
    end

    subgraph VMLane["Tenant-owned KubeVirt VM"]
      direction TB
      VMSource{{"INPUT CR<br/>kind: VirtualInstance<br/>tenant: org-id annotation<br/>VM id: spec.instance_id<br/>host: spec.node_name<br/>holder: status.pod_uid"}}
      VMParent(["PARENT AGENT ID<br/>type: dsx_imds<br/>spiffe://{td}/spire/agent/dsx_imds/tenant/{operator}/bm/{spec.node_name}"])
      VMEntry["STATIC ENTRY<br/>kind: ClusterStaticEntry<br/>owner: NVIDIA Workload Identity Controller<br/>subject: /tenant/{org-id}/service/{service}/vm/{spec.instance_id}<br/>hint: dsx-imds-vm<br/>selector: Pod UID"]
      VMSource -->|"derives subject"| VMEntry
      VMParent -->|"spec.node_name"| VMEntry
    end
  end
```

The DPU resource supplies the worker-to-DPU relationship. Its IMDS registration
allows DPU-hosted DSX IMDS to obtain the paired worker's SPIRE JWT-SVID and
exchange it for the DSX JWT returned through link-local IMDS. The worker uses
that DSX JWT to attest its local SPIRE agent through `dsx_imds`.

For a tenant-assigned host, the `BareMetalInstance` supplies the host-to-DPU
relationship. Its entry replaces the operator fallback derived from the DPU
resource.

Local consumers exchange local SPIRE JWT-SVIDs with their shard's NVIDIA
Workload Identity Keys for DSX JWTs. NVIDIA Workload Identity Keys instances
share only public signing keys across shards.

```mermaid
flowchart LR
  Local["dsx-token-fetcher and DSX IMDS"]
  IdentityKeys["NVIDIA Workload Identity<br/>Keys Deployment"]
  Consumers["DPU, host, Pod, and VM consumers"]

  Local -->|"local SPIRE JWT-SVID"| IdentityKeys
  IdentityKeys -->|"DSX JWT"| Local
  Local -->|"token file or IMDS"| Consumers
  IdentityKeys <-->|"Public keys in NATS KV (optional)"| Bus["External DSX Event Bus"]
  Bus <-->|"Public keys in NATS KV"| Peers["Peer shard<br/>NVIDIA Workload Identity<br/>Keys instances"]
  IdentityKeys -->|"public JWKS"| RelyingParty["External relying party"]
```

A SPIRE JWT-SVID is a cluster-local credential obtained from the local SPIRE
Workload API. A DSX JWT is the credential returned by the local NVIDIA
Workload Identity Keys. SPIRE JWT-SVIDs never establish trust between shards.

Install the identity chart once per shard. Each regular management cluster
that owns DPU or LaunchLayer source CRs runs the Identity Controller for its
shard. Every NVIDIA Workload Identity Keys instance validates only local SPIRE
JWT-SVIDs through its node-local Workload API. It serves aggregate public keys
received through the Event Bus when NATS is enabled, or only local public keys
when NATS is disabled.

The Event Bus, OAuth2 provider, and relying parties are external to the
identity chart.

DPF registers DPU Agent and DPUService workload identities through the external
[DPF identity configuration](identity-contract.md#dpf-identity-configuration).
The Identity Controller registers the paired host identity served by DPU IMDS.

## Identity delivery

| Consumer | SPIRE trust path | DSX JWT delivery |
|---|---|---|
| [Kubernetes Pod](../cmd/dsx-token-fetcher/README.md) | Kubernetes WorkloadAttestor under the worker's `dsx_imds`-attested host agent. The registration uses the operator tenant and worker host subject | The `dsx-token-fetcher` sidecar selects `dsx-pod`, exchanges the local SPIRE JWT-SVID, and writes the DSX JWT to a Pod-private volume mounted read-only in application containers |
| DPU service Pod | DPF exposes the DPU SPIRE agent through `csi.spiffe.io` | Native admission injects the standard token-fetcher sidecar and token volume |
| DPU IMDS | The external DPU agent attests through the `dpu_hw` NodeAttestor agent and server. Its Kubernetes WorkloadAttestor binds the paired BM identity to the IMDS Pod | SPIFFE CSI exposes the DPU agent Workload API socket. DSX IMDS selects `dsx-imds`, exchanges the local SPIRE JWT-SVID, and serves the BM DSX JWT through IMDS |
| [Bare-metal host process](../cmd/dsx-imds-node-attestor-agent/README.md) | The DPU agent is attested through `dpu_hw`. The host agent is attested through the repository `dsx_imds` NodeAttestor agent and server, then can serve local processes through the Unix WorkloadAttestor | The paired DPU intercept carries link-local IMDS to NICo IMDS, which passes identity requests to DSX IMDS. DPU-hosted DSX IMDS exchanges directly with its reachable shard-local NVIDIA Workload Identity Keys. The host agent uses the returned DSX JWT for node attestation. A host-process-specific token delivery contract is not currently defined |
| [Kata Containers Pod](../kata-uds-proxy/README.md) | The guest is a VM, so `kata-uds-proxy` forwards the host Workload API over vsock | A guest sidecar independently serves the standard Workload API socket path |
| [KubeVirt VM](../cmd/metadata-service/README.md) | The controller derives the VM subject from the dynamically supplied tenant ID and parents it to the operator-owned worker host's `dsx_imds` agent. The Kubernetes WorkloadAttestor serves the operator-owned virt-launcher Pod, and SPIFFE CSI exposes that same agent's Workload API socket | The DSX IMDS sidecar selects the VM SPIRE JWT-SVID by its exact `dsx-imds-vm` hint, exchanges it through NVIDIA Workload Identity Keys Proxy CSI, and serves the DSX JWT through IMDS |
