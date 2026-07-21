# Generate Signing Keyset

`generate-signing-keyset` is the fallback for deployments that do not source
signing keys from Vault or OpenBao. It creates one P-256 private key and derives
its `kid` from the SHA-256 public JWK thumbprint.

```bash
mise exec -- go run ./cmd/generate-signing-keyset > generated.json
```

## Output

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

The command has no runtime dependencies beyond a secure operating-system
random source. Its output contains private key material.

## Testing

```bash
mise exec -- go test ./cmd/generate-signing-keyset
```

The focused test verifies the PEM type, curve, private material, active
`kid`, and thumbprint-derived identifier.
