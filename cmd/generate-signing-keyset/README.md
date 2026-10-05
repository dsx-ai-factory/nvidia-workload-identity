# Generate Signing Keyset

`generate-signing-keyset` creates and rotates local P-256 signing keysets and
derives RFC 7638 SHA-256 public JWK thumbprints from private keysets. It is the
fallback for deployments that do not source signing keys from Vault or OpenBao.

## Generate a keyset

```bash
mise exec -- go run ./cmd/generate-signing-keyset generate > generated.json
```

The command writes a combined document that can be split into the two files
consumed by DSX Identity Keys:

```json
{
  "active_kid": "<RFC 7638 base64url SHA-256 public JWK thumbprint>",
  "alg": "ES256",
  "keys": [
    "-----BEGIN EC PRIVATE KEY-----\n<SEC 1 P-256 private key>\n-----END EC PRIVATE KEY-----\n"
  ]
}
```

Create the two files consumed by DSX Identity Keys:

```bash
jq -c '{keys}' generated.json > keyset.json
jq -c '{active_kid, alg}' generated.json > active-kid.json
```

## Add a key

`add` reads a combined keyset from standard input, retains its current keys,
appends a new P-256 key, and leaves the current key active:

```bash
mise exec -- go run ./cmd/generate-signing-keyset add \
  < generated.json > rotated.json
```

The input must contain at least one valid ES256 private key and its current
active `kid`. The output uses the same combined document format as `generate`.
Use `thumbprints` to identify the added key before changing `active_kid` after
the new public key has propagated.

## Generate thumbprints

`thumbprints` reads a private keyset from standard input and writes the ordered
RFC 7638 identifiers derived from its public keys:

```bash
mise exec -- go run ./cmd/generate-signing-keyset thumbprints \
  < keyset.json > thumbprints.json
```

```json
{"thumbprints":["<kid>","<kid>"]}
```

The input may be either a combined generator document or a `keyset.json`
document containing the `keys` array. SEC 1 EC, PKCS#1 RSA, and PKCS#8 private
keys are supported.

## DSX Identity Keys files

`keyset.json`:

```json
{
  "keys": [
    "-----BEGIN EC PRIVATE KEY-----\n<SEC 1 P-256 private key>\n-----END EC PRIVATE KEY-----\n"
  ]
}
```

`active-kid.json`:

```json
{
  "active_kid": "<RFC 7638 base64url SHA-256 public JWK thumbprint>",
  "alg": "ES256"
}
```

The local E2E uses this generator because it does not deploy a Vault or OpenBao
signing-key source.

The tool has no runtime dependencies beyond a secure operating-system random
source. `generate` and `add` output private key material. `thumbprints` outputs
only derived identifiers.
