# DSX IMDS NodeAttestor Server Plugin

`dsx-imds-node-attestor-server` is the server half of SPIRE's custom
`dsx_imds` NodeAttestor. It verifies the host DSX JWT received from the agent
plugin and returns the DSX subject as a reattestable SPIRE agent ID.

```mermaid
sequenceDiagram
    participant WA as Kubernetes WorkloadAttestor
    participant MS as DPU metadata-service process
    participant EX as Local Exchange Pod
    participant AP as dsx_imds NodeAttestor agent plugin
    participant HA as Bare-metal host SPIRE agent
    participant SS as Local SPIRE server Pod
    participant SP as dsx_imds NodeAttestor server plugin
    participant JWKS as Reachable Exchange aggregate JWKS endpoint
    WA->>MS: Select host identity for local SPIRE JWT-SVID
    MS->>EX: Exchange local SPIRE JWT-SVID
    EX-->>MS: Host DSX JWT
    HA->>AP: Begin node attestation
    AP->>MS: GET identity with Metadata: true
    MS-->>AP: Host DSX JWT
    AP-->>HA: Host DSX JWT attestation payload
    HA->>SS: Send attestation payload
    SS->>SP: Verify host DSX JWT
    SP->>JWKS: Load flat aggregate public keys
    SP->>SP: Verify signature, audience, issuer, and subject
    SP-->>SS: dsx_imds agent SPIFFE ID
```

SPIRE starts the binary through the plugin SDK. The plugin has no application
CLI flags. Configure it in the SPIRE server HCL:

```hcl
plugin_data {
  jwks_url = "http://spiffe-exchange.dsx-identity.svc.cluster.local:8080/.well-known/jwks.json"
  audience = "spiffe://k2.sjc-1.nvidia.com/spire/server"
  trust_domain = "k2.sjc-1.nvidia.com"
}
```

All three fields are required. `jwks_url` may name any reachable Exchange's
flat aggregate JWKS. The umbrella E2E configuration stages the plugin and
configures SPIRE in `scripts/k2-values.yaml`.

## Testing

```bash
go test ./cmd/dsx-imds-node-attestor-server ./internal/dsximds
```

The focused tests cover configuration, signature and claim verification,
remote JWKS behavior, and the complete agent-to-server attestation exchange.
