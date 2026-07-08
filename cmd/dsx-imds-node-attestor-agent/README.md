# DSX IMDS NodeAttestor Agent Plugin

`dsx-imds-node-attestor-agent` is the agent half of SPIRE's custom `dsx_imds`
NodeAttestor. It requests a host DSX JWT from the DPU metadata-service and sends
that token as the SPIRE node-attestation payload.

```mermaid
sequenceDiagram
    participant WA as Kubernetes WorkloadAttestor
    participant MS as DPU metadata-service process
    participant EX as Local Exchange Pod
    participant AP as dsx_imds NodeAttestor agent plugin
    participant HA as Bare-metal host SPIRE agent
    participant SS as Local SPIRE server Pod
    participant SP as dsx_imds NodeAttestor server plugin
    WA->>MS: Select host identity for local SPIRE JWT-SVID
    MS->>EX: Exchange local SPIRE JWT-SVID
    EX-->>MS: Host DSX JWT
    HA->>AP: Begin node attestation
    AP->>MS: GET identity with Metadata: true
    MS-->>AP: Host DSX JWT
    AP-->>HA: Attestation payload
    HA->>SS: Send attestation payload
    SS->>SP: Verify host DSX JWT
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
redirects. The external host-agent example is
`scripts/k2/spire/dsx-imds-agent-plugin.hcl`. The local E2E fixture stages the
plugin through `scripts/k2-values.yaml`.

## Testing

```bash
go test ./cmd/dsx-imds-node-attestor-agent ./internal/dsximds
```

The focused tests cover configuration, IMDS token retrieval, proxy and redirect
handling, and the complete agent-to-server attestation exchange.
