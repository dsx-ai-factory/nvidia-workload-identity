# Generate Signing Keyset

`generate-signing-keyset` creates one P-256 ES256 private JWK and derives its
globally unique `kid` from the SHA-256 public JWK thumbprint. It writes a JSON
object containing `active_kid` and `jwks` to standard output.

```bash
go run ./cmd/generate-signing-keyset > generated.json
```

The output contains private key material. Deployment scripts split it into the
`signing-jwks.json` and `active-kid` entries used by an Exchange signing-keyset
Secret. The local deployment path calls the command from
`scripts/deploy-identity.sh`.

The command has no flags and no runtime dependencies beyond a secure operating
system random source.

## Testing

```bash
go test ./cmd/generate-signing-keyset
```

The focused test verifies the key type, algorithm, private material, active
`kid`, and thumbprint-derived identifier.
