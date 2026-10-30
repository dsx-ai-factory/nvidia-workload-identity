# NVIDIA Workload Identity user guide

- Kubernetes and DPUService Pods use the
  [token-fetcher sidecar](token-fetcher/README.md).
- Paired bare-metal hosts and KubeVirt VMs use DSX IMDS.
- Relying parties validate DSX JWTs with the NVIDIA Workload Identity Keys JWKS endpoint.

## Acquire a token from DSX IMDS

| Consumer | Endpoint |
| --- | --- |
| Paired bare-metal host | `http://169.254.169.254/latest/meta-data/identity` |
| KubeVirt VM over IPv4 | `http://169.254.169.254/latest/meta-data/identity` |
| KubeVirt VM over IPv6 | `http://[fd00:76b9::254]/latest/meta-data/identity` |

Paired bare-metal hosts reach DPU-hosted DSX IMDS through NICo. The host
`dsx_imds` NodeAttestor uses the returned host token to attest its SPIRE agent.
KubeVirt VMs reach a DSX IMDS sidecar in the virt-launcher Pod and receive their
VM identity. The VM network setup sidecar enables IPv4, IPv6, or both from the
primary OVN allocation. See
[VM network requirements](../cmd/dsx-imds-network-setup/README.md#vm-bindings).

Request a token over IPv4:

```bash
curl --fail-with-body --silent --show-error --noproxy '*' --get \
  --header 'Metadata: true' \
  --header 'Accept: application/json' \
  --data-urlencode 'aud=spiffe://ds9.dsx.nvid.id/openbao' \
  http://169.254.169.254/latest/meta-data/identity
```

For a VM with IPv6, use the same request with the IPv6 endpoint:

```bash
curl --fail-with-body --silent --show-error --noproxy '*' --globoff --get \
  --header 'Metadata: true' \
  --header 'Accept: application/json' \
  --data-urlencode 'aud=spiffe://ds9.dsx.nvid.id/openbao' \
  'http://[fd00:76b9::254]/latest/meta-data/identity'
```

Example response:

```json
{
  "token": "<dsx-jwt>",
  "expires_in": 300,
  "spiffe_id": "spiffe://tenant-2.ds9.dsx.nvid.id/vm/example-vm"
}
```

Plain-text request:

```bash
curl --fail-with-body --silent --show-error --noproxy '*' --get \
  --header 'Metadata: true' \
  --header 'Accept: text/plain' \
  --data-urlencode 'aud=spiffe://ds9.dsx.nvid.id/openbao' \
  http://169.254.169.254/latest/meta-data/identity
```

Plain-text response:

```text
<dsx-jwt>
```

The bare-metal host path is a node-attestation contract, not a generic
host-process identity contract. See the [DSX IMDS
API](../cmd/metadata-service/README.md#http-api) for the versioned endpoint and
plain-text response option.

## Consume a DSX JWT

Use the returned `token` value as the relying party bearer credential:

```bash
curl --fail-with-body \
  --header 'Authorization: Bearer <dsx-jwt>' \
  https://relying-party.example/v1/resource
```

Do not log or persist the token.

## Validate a DSX JWT

Example request:

```bash
curl --fail-with-body \
  https://identity-keys.shard.example.com/.well-known/jwks.json
```

Example response:

```json
{
  "keys": [
    {
      "use": "sig",
      "kty": "EC",
      "kid": "<key-id>",
      "crv": "P-256",
      "alg": "ES256",
      "x": "<x-coordinate>",
      "y": "<y-coordinate>"
    }
  ]
}
```

Example verifier settings:

```yaml
issuer: https://ds9.dsx.nvid.id
audience: spiffe://ds9.dsx.nvid.id/openbao
jwks_url: https://identity-keys.shard.example.com/.well-known/jwks.json
```

With NATS enabled, every shard serves the aggregate JWKS. With NATS disabled,
use the issuing shard's endpoint.
