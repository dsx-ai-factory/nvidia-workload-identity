# DSX IMDS NodeAttestor Server Plugin

`dsx-imds-node-attestor-server` is the server half of SPIRE's custom
`dsx_imds` NodeAttestor. It verifies the host DSX JWT received from the agent
plugin and returns the DSX subject as a reattestable SPIRE agent ID.

```mermaid
sequenceDiagram
    participant DI as DSX IMDS process
    participant DA as External DPU SPIRE agent<br/>runtime WorkloadAttestor
    participant NI as Paired DPU intercept and NICo or Carbide IMDS
    participant EX as Shard-local DSX Identity Keys Pod
    participant AP as dsx_imds NodeAttestor agent plugin
    participant HA as Bare-metal host SPIRE agent
    participant SS as Local SPIRE server Pod
    participant SP as dsx_imds NodeAttestor server plugin
    participant JWKS as Reachable DSX Identity Keys aggregate JWKS endpoint
    DI->>DA: Request host SPIRE JWT-SVID by dsx-imds hint
    DA-->>DI: Local host SPIRE JWT-SVID
    DI->>EX: DSX Identity Keys local SPIRE JWT-SVID
    EX-->>DI: Host DSX JWT
    HA->>AP: Begin node attestation
    AP->>NI: GET link-local identity with Metadata: true
    NI->>DI: Pass through identity request
    DI-->>NI: Host DSX JWT
    NI-->>AP: Host DSX JWT
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
  jwks_url = "https://identity-keys.shard.example.com/.well-known/jwks.json"
  audience = "spiffe://k2.sjc-1.nvidia.com/spire/server"
  trust_domain = "k2.sjc-1.nvidia.com"
}
```

All three fields are required. `jwks_url` may name any reachable DSX Identity Keys instance's
flat aggregate JWKS. The standard OIDC remote key set caches keys and refreshes
when it encounters an unknown `kid`. `Dockerfile.spire` embeds the plugin in
the DSX SPIRE server image. The local E2E configuration selects that image in
`scripts/identity-e2e-values.yaml` and supplies each shard's plugin data in its
shard values file.

## Testing

```bash
go test ./cmd/dsx-imds-node-attestor-server ./internal/dsximds
```

The focused tests cover configuration, signature and claim verification,
remote JWKS behavior, and the complete agent-to-server attestation exchange.
