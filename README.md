# DSX identity

DSX Identity turns shard-local SPIRE identities into DSX JWTs that relying
parties can validate across shards. An identity shard contains an independent
SPIRE deployment and a co-located DSX Identity Keys deployment. DSX Identity Keys validates only its
local SPIRE JWT-SVIDs, issues DSX JWTs, and distributes its public signing keys
through the external DSX Event Bus.

There is no SPIRE federation between shards. Cross-shard trust starts after
token exchange, when another shard or relying party validates a DSX JWT with
the aggregate public keys served by any DSX Identity Keys instance.

## User guide

See the [examples user guide](examples/README.md) to acquire and consume DSX
JWTs through token-fetcher injection or the link-local IMDS endpoint.

For release maintainers, see [Release candidates](#release-candidates).

Component commands and runtime configuration:

- [DSX Identity Controller](cmd/dsx-identity-controller/README.md)
- [DSX IMDS Network Setup](cmd/dsx-imds-network-setup/README.md)
- [`dsx_imds` NodeAttestor agent plugin](cmd/dsx-imds-node-attestor-agent/README.md)
- [`dsx_imds` NodeAttestor server plugin](cmd/dsx-imds-node-attestor-server/README.md)
- [`dpu_hw` NodeAttestor server plugin](third-party/doca-platform-foundation/cmd/dpu-hw-server)
- [DSX Token Fetcher](cmd/dsx-token-fetcher/README.md)
- [DSX Identity Keys Proxy CSI Driver](cmd/dsx-identity-keys-proxy-csi/README.md)
- [Generate Signing Keyset](cmd/generate-signing-keyset/README.md)
- [DSX IMDS](cmd/metadata-service/README.md)
- [DSX Identity Keys](cmd/dsx-identity-keys/README.md)
- [Kata UDS Proxy](kata-uds-proxy/README.md)

### SPIRE upgrades

The bundled SPIRE server and agent use 1.15.1. For an existing 1.13.x shard,
follow [SPIRE's upgrade order](https://github.com/spiffe/spire/blob/v1.15.1/doc/upgrading.md):
upgrade all servers to 1.14.x, then all agents to 1.14.x, then all servers to
1.15.1, then all agents to 1.15.1. Upgrade each shard independently. A direct
1.13.x to 1.15.x rolling upgrade exceeds the supported version skew.

SPIRE 1.14 removes the deprecated `retry_rebootstrap` agent setting and requires
the agent KeyManager directory to exist and be writable. SPIRE 1.15 changes
some CLI JSON results from a one-element array to an object and renames the
`bootstraped` metric label to `bootstrapped`. The repository does not use the
removed setting or renamed label. Its `entry show -output json` consumer
continues to receive an object with an `entries` array.

## Release candidates

1. Send feature PRs to `master`.
2. When ready for QA, cut protected `release/1.15.0` from a tested `master` commit.
3. Send release fixes to `release/1.15.0`, then cherry-pick them back to `master` through PRs.

Release-worthy merges create `v1.15.0-rc.N`. The [RC workflow](.github/workflows/release-rc.yml)
publishes matching images and charts directly to NGC, without a GitLab mirror.
PRs only run validation. This workflow does not publish from `master` or promote RCs to stable.

[One-time setup](https://github.com/dsx-ai-factory/dsx-github-actions/blob/main/docs/release-candidate-publishing.md#prerequisites-and-scope):
configure the runner, protected release environment, NGC key, and release permissions.

### Promote an RC

Promotion is disabled by default. Complete the
[release setup](https://github.com/dsx-ai-factory/dsx-github-actions/blob/main/docs/promote-rc.md)
before enabling `.github/rc-promotion.json` and setting `DSX_RELEASE_MODE=rc-promotion`.
Only RCs with a captured promotion manifest can be selected.

1. Have QA validate a published RC with a successful manifest capture.
2. Open **Actions > Promote RC > Run workflow**. Select `master` and enter `X.Y.Z-rc.N`.
3. Confirm the RC version and source commit with the configured release reviewers.
4. After approval, the workflow publishes `X.Y.Z` using the tested image digests.

The [promotion workflow](.github/workflows/promote-rc.yml) calls the shared publisher.
RC publishing now records the artifact digests needed for promotion.

## CI

PRs run CI on `copy-pr-bot`'s `pull-request/<number>` branches. Pushes to `master`
and `release/**` also run `make validate`, the full [local E2E lifecycle](local-e2e-tests/README.md#workflow),
secret detection, and container scans. The bot must be installed for this repo,
and its [configuration](.github/copy-pr-bot.yaml) must be on `master` to take effect.
The shared build action scans both image platforms with Syft and Grype, uploads
SBOMs and reports, and fails on Critical vulnerabilities or scanner errors.
These scans do not submit results to Pulse.

All product jobs use the event's commit and read-only repository permissions,
without release secrets. The RC workflow calls product CI once and waits on
`needs: dsx-component-ci`, without listing individual jobs. The [image inventory](.github/images.json) is shared by scanning
and publishing. See the [local E2E guide](local-e2e-tests/README.md#workflow) for runner
requirements.

## Architecture

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
    CSController["DSX Identity Controller<br/>leader elected"]
    CSEntries["ClusterStaticEntry CRs"]
    CSPodManager["SPIRE controller-manager<br/>static entries and Pod registrations"]
    CSSPIRE["Common Services SPIRE server<br/>dpu_hw and dsx_imds server plugins"]
    CSIdentityKeys["Common Services DSX Identity Keys"]
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
    DHController["DSX Identity Controller<br/>leader elected"]
    DHEntries["ClusterStaticEntry CRs"]
    DHPodManager["SPIRE controller-manager<br/>static entries and Pod registrations"]
    DHSPIRE["Data Hall SPIRE server<br/>dpu_hw and dsx_imds server plugins"]
    DHIdentityKeys["Data Hall DSX Identity Keys"]
    DHProxy["DSX Identity Keys Proxy CSI<br/>node plugin Pod"]
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

### Registration parentage

The DSX Identity Controller owns DPU-derived, standalone host, and VM
`ClusterStaticEntry` CRs. The SPIRE controller-manager registers those entries
and registers Pods directly from the chart-owned `ClusterSPIFFEID` template.

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
      DPUEntry["STATIC ENTRY<br/>kind: ClusterStaticEntry<br/>owner: DSX Identity Controller<br/>subject: /tenant/{operator}/service/{service}/bm/{dpu-machine-id}<br/>hint: dsx-imds<br/>selectors: DPU IMDS Kubernetes selectors"]
      DPUCR -->|"derives subject"| DPUEntry
      DPUParent -->|"spec.serialNumber"| DPUEntry
    end

    subgraph HostLane["Standalone tenant-facing bare-metal host"]
      direction TB
      HostSource{{"INPUT CR<br/>kind: BareMetalInstance<br/>tenant: org-id annotation<br/>BM id: spec.instance_id<br/>DPU id and serial: status.primary_dpu"}}
      HostParent(["PARENT AGENT ID<br/>type: dpu_hw<br/>spiffe://{td}/spire/agent/dpu_hw/{normalized DPU serial}"])
      HostEntry["STATIC ENTRY<br/>kind: ClusterStaticEntry<br/>owner: DSX Identity Controller<br/>subject: /tenant/{org-id}/service/{service}/bm/{spec.instance_id}<br/>hint: dsx-imds<br/>selectors: DPU IMDS Kubernetes selectors"]
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
      VMEntry["STATIC ENTRY<br/>kind: ClusterStaticEntry<br/>owner: DSX Identity Controller<br/>subject: /tenant/{org-id}/service/{service}/vm/{spec.instance_id}<br/>hint: dsx-imds-vm<br/>selector: Pod UID"]
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

Local consumers exchange local SPIRE JWT-SVIDs with their shard's DSX Identity Keys for
DSX JWTs. DSX Identity Keys instances share only public signing keys across shards.

```mermaid
flowchart LR
  Local["dsx-token-fetcher and DSX IMDS"]
  IdentityKeys["DSX Identity Keys Deployment"]
  Consumers["DPU, host, Pod, and VM consumers"]

  Local -->|"local SPIRE JWT-SVID"| IdentityKeys
  IdentityKeys -->|"DSX JWT"| Local
  Local -->|"token file or IMDS"| Consumers
  IdentityKeys <-->|"Public keys in NATS KV (optional)"| Bus["External DSX Event Bus"]
  Bus <-->|"Public keys in NATS KV"| Peers["Peer shard DSX Identity Keys instances"]
  IdentityKeys -->|"public JWKS"| RelyingParty["External relying party"]
```

A SPIRE JWT-SVID is a cluster-local credential obtained from the local SPIRE
Workload API. A DSX JWT is the credential returned by the local DSX Identity Keys.
SPIRE JWT-SVIDs never establish trust between shards.

Install the identity chart once per shard. Each regular management cluster that
owns DPU or LaunchLayer source CRs runs the Identity Controller for its shard.
Every DSX Identity Keys instance validates only local SPIRE JWT-SVIDs through
its node-local Workload API. It serves aggregate public keys received through
the Event Bus when NATS is enabled, or only local public keys when NATS is
disabled.

The Event Bus, OAuth2 provider, and relying parties are external to the
identity chart.

### Identity delivery

| Consumer | SPIRE trust path | DSX JWT delivery |
|---|---|---|
| [Kubernetes Pod](cmd/dsx-token-fetcher/README.md) | Kubernetes WorkloadAttestor under the worker's `dsx_imds`-attested host agent. The registration uses the operator tenant and worker host subject | The `dsx-token-fetcher` sidecar selects `dsx-pod`, exchanges the local SPIRE JWT-SVID, and writes the DSX JWT to a Pod-private volume mounted read-only in application containers |
| DPU service Pod | DPF exposes the DPU SPIRE agent through `csi.spiffe.io` | Native admission injects the standard token-fetcher sidecar and token volume |
| DPU IMDS | The external DPU agent attests through the `dpu_hw` NodeAttestor agent and server. Its Kubernetes WorkloadAttestor binds the paired BM identity to the IMDS Pod | SPIFFE CSI exposes the DPU agent Workload API socket. DSX IMDS selects `dsx-imds`, exchanges the local SPIRE JWT-SVID, and serves the BM DSX JWT through IMDS |
| [Bare-metal host process](cmd/dsx-imds-node-attestor-agent/README.md) | The DPU agent is attested through `dpu_hw`. The host agent is attested through the repository `dsx_imds` NodeAttestor agent and server, then can serve local processes through the Unix WorkloadAttestor | The paired DPU intercept carries link-local IMDS to NICo IMDS, which passes identity requests to DSX IMDS. DPU-hosted DSX IMDS exchanges directly with its reachable shard-local DSX Identity Keys. The host agent uses the returned DSX JWT for node attestation. A host-process-specific token delivery contract is not currently defined |
| [Kata Containers Pod](kata-uds-proxy/README.md) | The guest is a VM, so `kata-uds-proxy` forwards the host Workload API over vsock | A guest sidecar independently serves the standard Workload API socket path |
| [KubeVirt VM](cmd/metadata-service/README.md) | The controller derives the VM subject from the dynamically supplied tenant ID and parents it to the operator-owned worker host's `dsx_imds` agent. The Kubernetes WorkloadAttestor serves the operator-owned virt-launcher Pod, and SPIFFE CSI exposes that same agent's Workload API socket | The DSX IMDS sidecar selects the VM SPIRE JWT-SVID by its exact `dsx-imds-vm` hint, exchanges it through DSX Identity Keys Proxy CSI, and serves the DSX JWT through IMDS |

## Deployment

Install [`charts/dsx-identity`](charts/dsx-identity/README.md) once per identity
shard with Helm. When enabled, the parent chart creates DPF DPUService resources
that reference the [DSX IMDS](charts/dsx-imds/README.md), [DSX SPIFFE
CSI](charts/dsx-spiffe-csi/README.md), and [DPU token-fetcher
admission](charts/dsx-token-fetcher/README.md) child charts.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development and DCO sign-off,
[GOVERNANCE.md](GOVERNANCE.md) for maintainers and review, and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community standards. Report
security issues through [SECURITY.md](SECURITY.md).

## Licensing

DSX Identity uses the [Apache License 2.0](LICENSE). Third-party code retains
its upstream licenses and notices. [THIRD-PARTY-NOTICES](THIRD-PARTY-NOTICES)
contains dependency attribution and license texts for Go, Rust, and the
pinned submodules.

`make update-licenses` runs SkyWalking Eyes to add source headers and uses
`go-licenses` and `cargo licenses` to regenerate dependency notices.
SkyWalking Eyes checks Helm template headers but cannot add their comment syntax.
Add those headers when creating templates. `make validate` checks headers
and rejects stale notices through the required CI Validate job.

The repository pins these public submodules:

| Path | Source | License |
| --- | --- | --- |
| `third-party/doca-platform-foundation` | [NVIDIA/doca-platform](https://github.com/NVIDIA/doca-platform) | [Apache-2.0](third-party/doca-platform-foundation/LICENSE) |
| `third-party/ovn-kubernetes` | [ovn-org/ovn-kubernetes](https://github.com/ovn-org/ovn-kubernetes) | [Apache-2.0](third-party/ovn-kubernetes/LICENSE) |

## Identity contract

DSX JWT subjects use complete key/value pairs with unique keys:

```text
spiffe://<tenant>.<region>.<service>.<partition>/<key>/<value>[/<key>/<value>...]
spiffe://<operator-tenant>.<region>.<service>.<partition>/bm/<node-name>/pod/<pod-uuid>
  [/role/<role>]
```

DPU service identities use:

```text
spiffe://<operator-tenant>.<region>.<service>.<partition>/dpu/<serial>/dpuservice/<service-id>/namespace/<namespace>
```

Exchange preserves path keys and values exactly. For example:

```text
local:     spiffe://<local-domain>/tenant/<tenant>/service/dsx/cluster/cluster-1/namespace/team-a/workload/api
exchanged: spiffe://<tenant>.<region>.dsx.<partition>/cluster/cluster-1/namespace/team-a/workload/api
```

Path keys and values retain their case and order. Each segment starts with an
ASCII letter or digit, followed by ASCII letters, digits, `.`, `_`, or `-`.

Cluster-local SPIRE identities encode the same fields under the shard trust
domain. Exchange takes the region and partition from explicit configuration,
the tenant and service from the local subject, and preserves the resource path.

```text
spiffe://<local-trust-domain>/tenant/<tenant>/service/<service>/<key>/<value>[/<key>/<value>...]
```

| Resource | Tenant source | Subject resource path |
|---|---|---|
| DPU | `global.operatorTenant` | `/dpu/<dpu-id>` |
| Operator bare-metal Kubernetes worker | `global.operatorTenant` | `/bm/<node-name>` |
| Kubernetes Pod | `global.operatorTenant` | `/bm/<node-name>/pod/<pod-uid>` |
| Bare-metal host with a Launch Layer source | `launchlayer.nvidia.com/org-id` annotation | `/bm/<bm-id>` |
| VM with a Launch Layer source | `launchlayer.nvidia.com/org-id` annotation | `/vm/<vm-id>` |

Every Kubernetes worker is an operator-owned bare-metal host. There is no
configured worker list and no LaunchLayer source for these hosts. The Pod
registration template derives the host ID directly from the Kubernetes node
name. The DPU source supplies the BM resource ID through its
`carbide.nvidia.com/dpu-machine-id` label.

Every DSX Kubernetes Pod supplies its service label. SPIRE uses its value for
the service path segment. The fixed parent template identifies the worker's
operator-tenant `dsx_imds` agent.

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
      parentIDTemplate: 'spiffe://{{ .TrustDomain }}/spire/agent/dsx_imds/tenant/$OPERATOR_TENANT/bm/{{ .NodeMeta.Name }}'
```

The chart requires the controller-manager value to equal
`global.operatorTenant`. Because it is part of the SPIRE Pod template, changing
both values causes the StatefulSet to roll out without a restart hook.

DPU identities remain separate controller-derived static entries parented to
the external `dpu_hw` agent identified by the normalized DPU serial. DPUs are
Kubernetes nodes in their Kamaji clusters. The DPU agent's Kubernetes
WorkloadAttestor identifies the IMDS Pod, and SPIFFE CSI exposes the agent's
Workload API socket to that Pod.

The [DSX Identity Controller](cmd/dsx-identity-controller/README.md) derives
SPIRE registration entries from DPU and Launch Layer sources.

```yaml
apiVersion: launchlayer.nvidia.com/v1alpha1
kind: VirtualInstance
metadata:
  annotations:
    launchlayer.nvidia.com/org-id: tenant-2
spec:
  instance_id: vm-1234
  node_name: host-1234
status:
  pod_uid: <virt-launcher-pod-uid>
```

`BareMetalInstance` uses the same tenant annotation, `spec.instance_id`, and
`status.primary_dpu.machine_id` plus `status.primary_dpu.serial_number`. The
configured operator tenant determines a VM entry's worker parent. The Identity
Controller owns SPIFFE IDs, selectors, hints, and class names.

## DSX Identity Keys and Event Bus

[DSX Identity Keys](cmd/dsx-identity-keys/README.md) validates
local SPIRE JWT-SVIDs and issues DSX JWTs. By default it publishes public
signing keys to the Event Bus and serves aggregate JWKS. With NATS disabled it
serves only local public signing keys. The external Gateway terminates TLS and
applies source-aware request quotas. Clients configure the Gateway HTTPS
endpoint.

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
endpoints support Prometheus discovery. First-party processes export traces
through their SDK and the sidecar collector installed by the DSX observability
stack. Structured allow, deny, and dependency events never include
credentials.

SPIRE server, controller-manager, and the optional local SPIRE agent expose
their native Prometheus metrics through chart-owned monitors. SPIRE 1.15.1
does not produce traces. SPIFFE CSI Driver, CSI node-driver-registrar, and
the `socat` proxy expose no native application telemetry. Their health checks,
container logs, and platform container metrics remain available.

The DSX sidecar collector configuration has a trace pipeline and no metrics
pipeline. `dsx-token-fetcher`, `metadata-service`, and
`dsx-imds-network-setup` export traces and do not expose application metrics.

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
    spiffe.nvidia.com/inject-dsx-token-fetcher: "true"
    spiffe.nvidia.com/token-audience: spiffe://ds9.dsx.nvid.id/openbao
    instrumentation.opentelemetry.io/inject-sdk: "dsx-obs/default-instrumentation"
    sidecar.opentelemetry.io/inject: "dsx-obs/default-sidecar"
```

The service label selects SPIRE registration. The injection annotation selects
the token fetcher through native admission policy. The audience annotation names
the relying party for the requested SPIRE JWT-SVID and exchanged DSX JWT.

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

## Local E2E

The [local E2E guide](local-e2e-tests/README.md) describes the Linux Kind setup, VM
bindings, identity checks, and install/test/clean workflow.

## Disclaimer

NOTICE AND DISCLAIMER: This software automatically retrieves, accesses or interacts with external materials.  Those retrieved materials are not distributed with this software and are governed solely by separate terms, conditions and licenses.  You are solely responsible for finding, reviewing and complying with all applicable terms, conditions, and licenses, and for verifying the security, integrity and suitability of any retrieved materials for your specific use case. This software is provided "AS IS", without warranty of any kind. The author makes no representations or warranties regarding any retrieved materials, and assumes no liability for any losses, damages, liabilities or legal consequences from your use or inability to use this software or any retrieved materials. Use this software and the retrieved materials at your own risk.
