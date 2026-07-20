# DSX Token Fetcher

`dsx-token-fetcher` obtains a local SPIRE JWT-SVID from the Workload API,
submits it to the local DSX Identity Keys, and writes the exchanged DSX JWT to a shared
file. In long-running mode it refreshes the token before expiry and reports
readiness at `/healthz`.

```mermaid
flowchart LR
    Fetcher["dsx-token-fetcher<br/>process or Pod sidecar"] -->|"request local SPIRE JWT-SVID"| Agent["Local SPIRE agent process"]
    Fetcher -->|"RFC 8693 request"| IdentityKeys["Shard-local DSX Identity Keys"]
    IdentityKeys -->|"exchanged DSX JWT"| Fetcher
    Fetcher -->|"atomic file replacement"| File["DSX JWT token file"]
    Consumer["DPU or Pod consumer"] -->|"read DSX JWT"| File
```

The identity chart injects the sidecar and shared volume through
`charts/dsx-identity/templates/pod-token-fetcher.yaml`.
In an identity-enabled namespace, `spiffe.nvidia.com/service: dsx` selects both
the Pod registration and sidecar injection. The fetcher alone receives the
Workload API socket, selects the `dsx-pod` SVID, and atomically writes the DSX
JWT to a Pod-private volume mounted read-only in application containers.

An external DPU supervisor can run the same binary against its local Workload
API socket and token-file path after the DPU agent attests through `dsx_dpu`.
The base DPU registration selects the `dsx-token-fetcher` process with Unix
WorkloadAttestor selectors and exposes the `dsx-dpu` SVID hint. The fetcher is
the Workload API caller, not the consuming DPU process. The protected token file
is the capability consumed by the DPU process. That integration is outside the
identity chart. Host-process-specific integration is not part of the current
repository scope.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--socket` | `unix:///var/run/secrets/spire.io/spire-agent.sock` | SPIRE Workload API socket URL |
| `--audience` | `spiffe://example.org` | Audience requested in the local SPIRE JWT-SVID |
| `--svid-hint` | empty | Optional exact JWT-SVID hint override. Without it, use the Workload API default |
| `--identity-keys-url` | required | RFC 8693 endpoint of the reachable DSX Identity Keys |
| `--output` | `/run/secrets/nvidia.com/dsx/token` | DSX JWT output file |
| `--health-port` | `8080` | Port for `/healthz` |
| `--log-level` | `info` | Log level: debug, info, warn, or error |

The runtime owns access to the output directory. The fetcher atomically replaces
the consumer-readable token file. A failed refresh keeps an unexpired token
available. An expired token is removed and readiness returns HTTP 503.
Long-running fetchers refresh after 80 percent of the remaining lifetime. The
fetcher retries failed refreshes once per second.

## Testing

```bash
go test ./cmd/dsx-token-fetcher
```

The focused tests cover identity selection, refresh timing, atomic writes,
expiry handling, and readiness. The optional
live Workload API test uses `DSX_E2E_SPIFFE_ENDPOINT_SOCKET`,
`DSX_E2E_SPIRE_JWT_AUDIENCE`, and `DSX_E2E_SPIRE_JWT_ID`.
