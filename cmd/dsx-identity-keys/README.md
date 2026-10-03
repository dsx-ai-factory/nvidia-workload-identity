# DSX Identity Keys

`dsx-identity-keys` is the DSX Identity Keys process installed in each identity
shard. It validates only local SPIRE JWT-SVIDs, transforms their SPIFFE subject
to the DSX identity contract, preserves the registered `aud`, `exp`, `iat`,
`jti`, and `nbf` claims, and signs the exchanged DSX JWT with the shard's active
key. SPIRE registration determines the tenant in the local subject, and DSX
Identity Keys preserves it while validating the common identity grammar. It obtains local
JWT verification bundles through the node-local SPIFFE Workload API.

Tenant and service determine the exchanged trust domain. The resource path
contains complete key/value pairs with unique keys. Each segment starts with
an ASCII letter or digit, followed by ASCII letters, digits, `.`, `_`, or `-`.
DSX Identity Keys preserves valid paths exactly, including case and order:

```text
local:     spiffe://<local-domain>/tenant/<tenant>/service/dsx/cluster/cluster-1/namespace/team-a/workload/api
exchanged: spiffe://<tenant>.<region>.dsx.<partition>/cluster/cluster-1/namespace/team-a/workload/api
```

For example, DPF can use this explicitly labelled DPUService suffix:

```text
local:     spiffe://<local-domain>/tenant/<tenant>/service/dsx/dpu/<serial>/dpuservice/<service-id>/namespace/<namespace>
exchanged: spiffe://<tenant>.<region>.dsx.<partition>/dpu/<serial>/dpuservice/<service-id>/namespace/<namespace>
```

DPF includes the namespace because DPU service IDs are unique only within a
Kubernetes namespace. DSX Identity Keys only preserves that suffix.

By default, each DSX Identity Keys instance writes only its local public keys to
one shared NATS KV bucket and watches the complete bucket. It retains one
aggregate keyed by globally unique `kid`, and its JWKS endpoint serves every
valid local and remote public key. Startup requires the Event Bus connection,
KV bucket, and initial history. Later connection failures leave DSX Identity
Keys live but not ready while it reconnects. Instances using the same bucket
converge on the same aggregate, so relying parties can use the JWKS from any
reachable DSX Identity Keys.

`--disable-nats` skips the Event Bus connection and serves only the local public
signing keys. Readiness then depends only on the local key registry. This mode
does not distribute keys between shards, so each relying party must use the
JWKS endpoint for the shard that issued its tokens.

## Flat key distribution

```mermaid
flowchart TB
  RP["External relying party"]
  E1["DSX Identity Keys Deployment<br/>shard-a"]
  E2["DSX Identity Keys Deployment<br/>shard-b"]
  E3["DSX Identity Keys Deployment<br/>shard-c"]
  BUS["External DSX Event Bus<br/>NATS KV bucket"]

  RP -->|"one aggregate JWKS"| E2
  E1 <-->|"local keys out, all public keys in"| BUS
  E2 <-->|"local keys out, all public keys in"| BUS
  E3 <-->|"local keys out, all public keys in"| BUS
```

The local E2E fixture uses two DSX Identity Keys instances in the same subject space:

```mermaid
flowchart LR
    K1["k1 DSX Identity Keys Pod"] -->|"local public keys"| Bus["NATS KV bucket"]
    K2["k2 DSX Identity Keys Pod"] -->|"local public keys"| Bus
    Bus -->|"initial replay and watched updates"| K1
    Bus -->|"initial replay and watched updates"| K2
    K1 --> J1["k1 aggregate JWKS"]
    K2 --> J2["k2 aggregate JWKS"]
```

Public keys use NATS KV keys of the form
`shard.<shard-id>.kid.<kid>`. Each value is one public JWK. The bucket keeps one
revision per key and applies the configured TTL. Each instance refreshes its
local entries every quarter of that TTL, leaving three further write
opportunities before expiry. Each instance starts an updates-only watch before
loading `History`, so concurrent writes queue while current values are loaded.
If the watcher closes, its replacement uses the same ordering. Each valid entry
replaces the globally unique `kid` it addresses. NATS connections use either no
authentication or OAuth2 client credentials. OAuth2 mode reads a
JSON credentials file containing non-empty `client_id` and `client_secret`
fields, discovers the token endpoint from the configured issuer, and rereads
the credentials when the cached access token expires. OAuth scopes are
configurable, and an empty scope list sends no scope parameter. The access
token is sent through native NATS token authentication. Production DSX Exchange
deployments place DSX Identity Keys clients in a dedicated NATS account, which
provides the account boundary and JetStream capacity used by the KV bucket.
Basic authentication is not supported. Private, malformed, unsupported, and
mismatched keys do not replace a valid aggregate entry.

The identity chart deploys two active replicas per shard through
`charts/dsx-identity/templates/identity-keys-deployment.yaml`. Both replicas
load the same shard-local private
keyset and active-kid Secret. Only public keys are published to the Event Bus.

External secret automation requests
`/v1/transit/export/signing-key/<name>?format=pem` and renders the exported
`data.keys` values into the private keyset file:

```json
{"keys":["-----BEGIN EC PRIVATE KEY-----\n...\n-----END EC PRIVATE KEY-----\n"]}
```

DSX Identity Keys derives each `kid` from the SHA-256 public JWK thumbprint and
accepts SEC 1 EC, PKCS#1 RSA, and PKCS#8 asymmetric private keys. The separate
`active-kid.json` Secret entry selects one loaded key and the JOSE algorithm
for the Transit key ring. Every loaded key must support that algorithm:

```json
{"active_kid":"<kid>","alg":"ES256"}
```

Rotation first adds and publishes the new key while the old kid remains active.
Activation changes only the active-kid Secret. Removing an expired old key
stops its lease refresh, after which it disappears from aggregate JWKS
responses.

## HTTP API

| Endpoint | Purpose |
| --- | --- |
| `POST /v1/exchange` | Unauthenticated RFC 8693 exchange of a local SPIRE JWT-SVID |
| `GET /.well-known/jwks.json` | Public JWKS. Aggregate with NATS enabled, local-only with NATS disabled |
| `GET /.well-known/openid-configuration` | Issuer, JWKS URL, and advertised signing algorithms |
| `GET /live` | Process liveness |
| `GET /ready` | Public-key registry readiness. Includes NATS connection and KV replay when enabled |

The exchange endpoint is intentionally unauthenticated because the submitted
local SPIRE JWT-SVID is the credential being exchanged.

The exchange form requires `grant_type`, `subject_token`, and
`subject_token_type`. It accepts the optional `requested_token_type`, caps
request bodies at 1 MiB.
Audience is preserved from the validated SVID. DSX Identity Keys does not accept an
audience override or apply a relying-party audience allowlist. Error responses
do not expose parser or verifier details.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen` | `:8080` | HTTP listen address |
| `--socket` | Workload API default | Local SPIFFE Workload API socket URL |
| `--jwks-url` | required | Absolute public JWKS URL advertised by discovery |
| `--issuer` | required | HTTPS DSX issuer with a DNS hostname |
| `--region` | required | Region encoded in exchanged DSX identities |
| `--partition` | required | Partition encoded in exchanged DSX identities |
| `--signing-keyset` | `/run/secrets/dsx-identity-keys/keyset.json` | JSON file containing private PEM keys |
| `--active-kid` | `/run/secrets/dsx-identity-keys-active-kid/active-kid.json` | JSON file containing the active signing `kid` and algorithm |
| `--shard-id` | NATS enabled | Globally unique shard ID in the Event Bus subject space |
| `--disable-nats` | `false` | Serve local public keys without NATS synchronization |
| `--nats-server` | NATS enabled | NATS server URL |
| `--nats-client-name` | `dsx-identity-keys-<shard-id>` | NATS connection name |
| `--public-key-ttl` | `10m` | NATS KV entry TTL |
| `--nats-kv-bucket` | `DSX_IDENTITY_KEYS` | Shared NATS KV bucket |
| `--nats-kv-replicas` | `1` | KV bucket replica count |
| `--nats-auth-mode` | `none` | NATS authentication mode: `none` or `oauth2` |
| `--nats-credentials-file` | OAuth2 mode | JSON file containing OAuth2 client credentials |
| `--nats-oauth-issuer-url` | OAuth2 mode | OAuth2 issuer whose discovery document advertises the token endpoint |
| `--nats-oauth-scopes` | `nats` | Comma-separated scopes; empty requests none |
| `--log-level` | `info` | JSON log level: `debug`, `info`, `warn`, or `error` |

## Testing

```bash
go test ./cmd/dsx-identity-keys
```

The focused tests cover signing configuration, RFC 8693 behavior, flat `kid`
aggregation, deterministic KV expiry, rotation overlap, replay after restart,
watched updates, and every supported authentication mode. Live NATS and
deployed aggregate checks use the
`DSX_E2E_NATS_*` and
`DSX_E2E_VERIFY_*` environment variables used by the E2E scripts.
