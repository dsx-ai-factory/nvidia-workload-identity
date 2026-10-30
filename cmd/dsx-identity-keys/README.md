# NVIDIA Workload Identity Keys

`dsx-identity-keys` is the NVIDIA Workload Identity Keys process installed in
each identity shard. It validates only local SPIRE JWT-SVIDs, transforms their
SPIFFE subject to the NVIDIA Workload Identity contract, preserves the
registered `aud`, `exp`, `iat`, `jti`, and `nbf` claims, writes the subject
components as [claims](#token-claims), and signs the
exchanged DSX JWT with the shard's active key. SPIRE registration determines
the tenant in the local subject, and NVIDIA Workload Identity Keys preserves
it while validating the common identity grammar. It obtains local JWT
verification bundles through the node-local SPIFFE Workload API.

Tenant and service determine the exchanged trust domain. The resource path
contains complete key/value pairs with unique keys. Each segment starts with
an ASCII letter or digit, followed by ASCII letters, digits, `.`, `_`, or `-`.
NVIDIA Workload Identity Keys preserves valid paths exactly, including case
and order:

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
Kubernetes namespace. NVIDIA Workload Identity Keys only preserves that
suffix.

Configure the external [DPF identity templates](../../docs/identity-contract.md#dpf-identity-configuration)
to supply the local tenant and service segments and this DPUService suffix.

By default, each NVIDIA Workload Identity Keys instance writes only its local
public keys to one shared NATS KV bucket and watches the complete bucket. It
retains one aggregate keyed by globally unique `kid`, and its JWKS endpoint
serves every valid local and remote public key. Startup requires the Event Bus
connection, KV bucket, and initial history. Later connection failures leave
NVIDIA Workload Identity Keys live but not ready while it reconnects.
Instances using the same bucket converge on the same aggregate, so relying
parties can use the JWKS from any reachable NVIDIA Workload Identity Keys.

`--disable-nats` skips the Event Bus connection and serves only the local public
signing keys. Readiness then depends only on the local key registry. This mode
does not distribute keys between shards, so each relying party must use the
JWKS endpoint for the shard that issued its tokens.

## Token claims

The exchanged DSX JWT carries each component of its subject as a claim, so
relying parties can authorize on claims without parsing `sub`. Each value is
the component encoded into `sub`. For
`spiffe://operator-1.ds9.dsx.nvid.id/bm/worker-1/pod/<pod-uid>/role/web`:

```json
{
  "dsx.nvidia.com/tenant": "operator-1",
  "dsx.nvidia.com/region": "ds9",
  "dsx.nvidia.com/service": "dsx",
  "dsx.nvidia.com/partition": "nvid.id",
  "dsx.nvidia.com/resource": [
    {"resource_type": "bm", "resource_id": "worker-1"},
    {"resource_type": "pod", "resource_id": "<pod-uid>"},
    {"resource_type": "role", "resource_id": "web"}
  ]
}
```

`resource` lists every path pair in order, starting with the resource itself.
A rule on one entry, such as `{bm, worker-1}`, matches the host and every
workload on it, like a prefix match on `sub`. To match only the host, the rule
must also check the length of `resource`, like an exact match on `sub`.

## Flat key distribution

```mermaid
flowchart TB
  RP["External relying party"]
  E1["NVIDIA Workload Identity<br/>Keys Deployment<br/>shard-a"]
  E2["NVIDIA Workload Identity<br/>Keys Deployment<br/>shard-b"]
  E3["NVIDIA Workload Identity<br/>Keys Deployment<br/>shard-c"]
  BUS["External DSX Event Bus<br/>NATS KV bucket"]

  RP -->|"one aggregate JWKS"| E2
  E1 <-->|"local keys out, all public keys in"| BUS
  E2 <-->|"local keys out, all public keys in"| BUS
  E3 <-->|"local keys out, all public keys in"| BUS
```

The local E2E fixture uses two NVIDIA Workload Identity Keys instances in the
same subject space:

```mermaid
flowchart LR
    K1["k1 NVIDIA Workload Identity<br/>Keys Pod"] -->|"local public keys"| Bus["NATS KV bucket"]
    K2["k2 NVIDIA Workload Identity<br/>Keys Pod"] -->|"local public keys"| Bus
    Bus -->|"initial replay and watched updates"| K1
    Bus -->|"initial replay and watched updates"| K2
    K1 --> J1["k1 aggregate JWKS"]
    K2 --> J2["k2 aggregate JWKS"]
```

Public keys use NATS KV keys of the form `shard.<shard-id>.kid.<kid>`. Each
value is one public JWK. The bucket keeps one revision per key and applies the
configured TTL. Each instance refreshes its local entries every quarter of
that TTL, leaving three further write opportunities before expiry. Each
instance starts an updates-only watch before loading `History`, so concurrent
writes queue while current values are loaded. If the watcher closes, its
replacement uses the same ordering. Each valid entry replaces the globally
unique `kid` it addresses. NATS connections use either no authentication or
OAuth2 client credentials. OAuth2 mode reads a JSON credentials file
containing non-empty `client_id` and `client_secret` fields, discovers the
token endpoint from the configured issuer, and rereads the credentials when
the cached access token expires. OAuth scopes are configurable, and an empty
scope list sends no scope parameter. The access token is sent through native
NATS token authentication. Production deployments place NVIDIA Workload
Identity Keys clients in a dedicated NATS account, which provides the account
boundary and JetStream capacity used by the KV bucket. Basic
authentication is not supported. Private, malformed, unsupported, and
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

NVIDIA Workload Identity Keys derives each `kid` from the SHA-256 public JWK
thumbprint and accepts SEC 1 EC, PKCS#1 RSA, and PKCS#8 asymmetric private
keys. The separate `active-kid.json` Secret entry selects one loaded key and
the JOSE algorithm for the Transit key ring. Every loaded key must support
that algorithm:

```json
{"active_kid":"<kid>","alg":"ES256"}
```

Each instance watches both mounted sources with filesystem notifications and
polls for content changes every 30 seconds. `--file-watch-poll-interval` sets
the polling interval. Both watchers detect Kubernetes Secret symlink updates.
Each file change validates the keyset and active-key pair. A valid pair replaces the active
signer and local public keys without a restart and triggers NATS publication. Missing,
malformed, or mismatched sources retain the last valid signer and keys until
both sources validate. Invalid sources at startup prevent the process from
starting.

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
request bodies at 1 MiB. Audience is preserved from the validated SVID. NVIDIA
Workload Identity Keys does not accept an audience override or apply a
relying-party audience allowlist. Error responses do not expose parser or
verifier details.

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
| `--file-watch-poll-interval` | `30s` | Positive interval for checking file contents when filesystem notifications are missed |
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
