# DSX IMDS NodeAttestor Agent Plugin

`dsx-imds-node-attestor-agent` is the agent half of [SPIRE](https://spiffe.io/docs/latest/spire-about/)'s custom `dsx_imds`
NodeAttestor. It requests a host DSX JWT-SVID through the paired DPU intercept and
[NICo](https://docs.nvidia.com/infra-controller/) IMDS passthrough, then sends that token as the SPIRE
node-attestation payload.

The external DPU establishes the first local SPIRE identities through
`dpu_hw`. DSX IMDS selects the host identity beneath that DPU agent and
exchanges it for the DSX JWT-SVID used by this plugin. The bare-metal host agent must
complete `dsx_imds` node attestation before it can serve host processes through
the Unix WorkloadAttestor or provide identities to VMs.

## Host identity trust path

The external `dpu_hw` NodeAttestor agent and server plugins attest the DPU
agent. Its runtime WorkloadAttestor makes the registered host identity
available to DSX IMDS. The repository-owned `dsx_imds` NodeAttestor agent and
server plugins then attest the host agent, whose Unix WorkloadAttestor serves
host processes.

```mermaid
flowchart TB
  DPUIdentities["DPU local SPIRE identities<br/>from shard-local SPIRE"]
  HostIdentities["Host local SPIRE identities<br/>from shard-local SPIRE"]
  IdentityKeys["Shard-local<br/>NVIDIA Workload Identity<br/>Keys"]
  DPUAgent["DPU SPIRE agent process<br/>dpu_hw agent plugin"]
  NICoIMDS["Paired DPU link-local intercept<br/>NICo IMDS passthrough"]
  DSXIMDS["DSX IMDS<br/>(metadata-service)"]
  HostAgent["Bare-metal host SPIRE agent process<br/>dsx_imds agent plugin"]
  HostProcess["Bare-metal host process"]

  DPUAgent -->|"dpu_hw node attestation"| DPUIdentities
  DPUIdentities -->|"host SPIRE JWT-SVID"| DSXIMDS
  DSXIMDS -->|"exchange for host DSX JWT-SVID"| IdentityKeys
  IdentityKeys -->|"host DSX JWT-SVID"| DSXIMDS
  HostAgent <-->|"link-local IMDS"| NICoIMDS
  NICoIMDS <-->|"identity passthrough"| DSXIMDS
  HostAgent -->|"dsx_imds node attestation"| HostIdentities
  HostIdentities -->|"Workload API"| HostProcess
```

## Node attestation exchange

```mermaid
sequenceDiagram
    participant DI as DSX IMDS process
    participant DA as External DPU SPIRE agent<br/>runtime WorkloadAttestor
    participant NI as Paired DPU intercept and NICo IMDS
    participant EX as Shard-local NVIDIA Workload Identity Keys Pod
    participant AP as dsx_imds NodeAttestor agent plugin
    participant HA as Bare-metal host SPIRE agent
    participant SS as Local SPIRE server Pod
    participant SP as dsx_imds NodeAttestor server plugin
    DI->>DA: Request host SPIRE JWT-SVID by dsx-imds hint
    DA-->>DI: Local host SPIRE JWT-SVID
    DI->>EX: NVIDIA Workload Identity Keys local SPIRE JWT-SVID
    EX-->>DI: Host DSX JWT-SVID
    HA->>AP: Begin node attestation
    AP->>NI: GET link-local identity with Metadata: true
    NI->>DI: Pass through identity request
    DI-->>NI: Host DSX JWT-SVID
    NI-->>AP: Host DSX JWT-SVID
    AP-->>HA: Attestation payload
    HA->>SS: Send attestation payload
    SS->>SP: Verify host DSX JWT-SVID
    SP-->>SS: dsx_imds agent SPIFFE ID
```

SPIRE starts the binary through the plugin SDK. The plugin has no application
CLI flags. Configure it in the SPIRE agent HCL:

```hcl
plugin_data {
  endpoint = "http://169.254.169.254"
  audience = "spiffe://k2.sjc-1.nvidia.com/spire/server"
}
```

`endpoint` defaults to `http://169.254.169.254`. `audience` is required. The
HTTP client connects directly, ignores ambient proxy settings, and rejects
redirects. `Dockerfile.spire` embeds the plugin in the DSX SPIRE agent image.
The local E2E fixture selects that image through
`local-e2e-tests/dsx-imds-host-agent-values.yaml` plus its shard override.
