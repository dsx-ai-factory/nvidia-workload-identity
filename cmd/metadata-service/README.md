# DSX IMDS

`metadata-service` implements DSX IMDS. It serves DSX identity through the
link-local IMDS v1 API, selects a local SPIRE JWT-SVID by hint, exchanges it,
and returns the DSX JWT. KubeVirt uses Exchange Proxy CSI. Paired-DPU placement
uses a direct URL to its reachable shard-local Exchange.

```mermaid
sequenceDiagram
    participant VM as KubeVirt VM external client
    participant MS as virt-launcher DSX IMDS sidecar
    participant SA as dsx_imds-attested host SPIRE agent process
    participant CSI as Exchange Proxy CSI socket
    participant EX as Shard-local Exchange Pod
    VM->>MS: GET identity with Metadata: true and aud
    MS->>SA: Request local SPIRE JWT-SVID by hint
    SA-->>MS: Local SPIRE JWT-SVID
    MS->>CSI: RFC 8693 request over Unix socket
    CSI->>EX: Forward request over verified TLS
    EX-->>MS: Exchanged DSX JWT
    MS-->>VM: JSON object or plain DSX JWT
```

The same binary serves the `dsx-imds` host identity behind the paired DPU
intercept and NICo or Carbide IMDS passthrough, and serves the `dsx-imds-vm`
identity as a KubeVirt sidecar. The identity chart injects that sidecar through
`charts/dsx-identity/templates/vm-support.yaml`.

## HTTP API

The supported identity endpoints are:

```text
GET /latest/meta-data/identity?aud=<audience>
GET /2009-04-04/meta-data/identity?aud=<audience>
```

Requests require `Metadata: true`, reject proxy forwarding headers, and may
repeat `aud`. `Accept` supports `application/json` and `text/plain`. An absent
header defaults to JSON. Plain text returns only the DSX JWT. JSON returns
`token`, `expires_in`, and `spiffe_id`. Successful identity responses use
`Cache-Control: no-store`. Response connections use IPv4 TTL 1 or IPv6 hop
limit 1. Dependency failures return sanitized HTTP errors. `GET /health`
reports process health.

The API is intentionally tokenless. The paired DPU intercept and NICo or
Carbide enforce physical source binding, network isolation, and response hop
limits. `Metadata: true` and proxy-header rejection are additional request
checks, not a replacement for that network boundary.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen` | `:80` | HTTP listen address |
| `--exchange-service-url` | required | RFC 8693 HTTP endpoint, or `unix:///path/to/socket` for Exchange Proxy CSI |
| `--spire-socket` | `/var/run/secrets/spire.io/spire-agent.sock` | SPIRE Workload API socket path |
| `--svid-hint` | `dsx-imds` | Exact identity hint, either `dsx-imds` or `dsx-imds-vm` |
| `--log-level` | `info` | JSON log level: `debug`, `info`, `warn`, or `error` |

## Testing

```bash
go test ./cmd/metadata-service
```

The focused tests cover request validation, response format, response TTL,
identity selection, and Exchange transport.
