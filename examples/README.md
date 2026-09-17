# DSX Identity user guide

- Kubernetes and DPUService Pods use the
  [token-fetcher sidecar](token-fetcher/README.md).
- Paired bare-metal hosts and KubeVirt VMs use the link-local DSX IMDS
  endpoint.
- Relying parties validate DSX JWTs with the DSX Identity Keys JWKS endpoint.

## Acquire a token from link-local IMDS

```text
http://169.254.169.254/latest/meta-data/identity
```

Paired bare-metal hosts reach DPU-hosted DSX IMDS through NICo. The host
`dsx_imds` NodeAttestor uses the returned host token to attest its SPIRE agent.
KubeVirt VMs reach a DSX IMDS sidecar in the virt-launcher Pod and receive their
VM identity.

Both paths use the same request:

```bash
audience=spiffe://ds9.dsx.nvid.id/openbao
response=$(curl --fail-with-body --silent --show-error --noproxy '*' --get \
  --header 'Metadata: true' \
  --header 'Accept: application/json' \
  --data-urlencode "aud=${audience}" \
  http://169.254.169.254/latest/meta-data/identity)
token=$(jq -er '.token' <<<"${response}")
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
  --data-urlencode "aud=${audience}" \
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

Present the token to the relying party as a bearer credential:

```bash
curl --fail-with-body \
  --header "Authorization: Bearer ${token}" \
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
