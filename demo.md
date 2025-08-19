# SPIFFE JWT + OpenBao CEL Demo

This demo shows how to use the SPIFFE JWT issuer service with OpenBao's CEL-enabled JWT authentication.

## Step 1: Start Services

```bash
# Terminal 1: Start OpenBao server
cd openbao
go run . server -dev -dev-root-token-id="dev-only-token"

# Terminal 2: Start SPIFFE JWT issuer service  
go run .
```

## Step 2: Configure OpenBao

```bash
export VAULT_ADDR=http://localhost:8200
export VAULT_TOKEN=dev-only-token

# Setup JWT auth engine with our JWKS endpoint
bao auth enable jwt
bao write auth/jwt/config \
  jwks_url=http://localhost:8080/.well-known/jwks.json \
  default_role=basic-spiffe

# Create SPIFFE-aware CEL role
bao write auth/jwt/cel/role/basic-spiffe @cel-role.json

# Create policy for testing
bao write sys/policies/acl/spiffe-authenticated policy='
path "secret/*" { 
  capabilities = ["read", "list"] 
}
path "identity/*" { 
  capabilities = ["read"] 
}'
```

## Step 3: Generate and Test SPIFFE JWTs

```bash
# Generate a SPIFFE JWT using our issuer service
export SPIFFE_JWT=$(curl -s -X POST http://localhost:8080/token \
  -H "Content-Type: application/json" \
  -d '{
    "issuer_id": "spiffe://sjc-1.k8s2-control.ncp.com/spire/server",
    "region": "sjc-1",
    "cloud_tenant_id": "k8s2-control",
    "domain": "com", 
    "product_name": "controlplane",
    "resource_name": "demo-vm-001",
    "audiences": ["spiffe://sjc-1.k8s2-control.ncp.com/controlplane/vm/fluentd"],
    "ttl": "1h",
    "custom_claims": {
      "role": "log-forwarder",
      "environment": "production"
    }
  }' | jq -r '.access_token')

# Optional: Verify the JWT with our validation endpoint
echo "Verifying JWT with our service..."
curl -X POST http://localhost:8080/validate \
  -H "Content-Type: application/json" \
  -d "{\"token\": \"$SPIFFE_JWT\"}" | jq .

# Test authentication with OpenBao using our SPIFFE JWT
echo "Testing SPIFFE JWT authentication with CEL..."
bao write auth/jwt/cel/login role=basic-spiffe jwt=$SPIFFE_JWT
```

Expected authentication output:
```
Key                  Value
---                  -----
token                s.Y2uJO5FC8WOMoRVMgZ5pnw2H
token_accessor       bu8HpQbueHv8Ln911hRBOjlL
token_duration       768h
token_renewable      false
token_policies       ["default" "product-controlplane" "region-sjc-1" "resource-id-demo-vm-001" "resource-type-vm" "spiffe-authenticated" "tenant-k8s2-control"]
identity_policies    []
policies             ["default" "product-controlplane" "region-sjc-1" "resource-id-demo-vm-001" "resource-type-vm" "spiffe-authenticated" "tenant-k8s2-control"]
```

## What This Demonstrates

1. **External JWT Issuer**: OpenBao validates JWTs issued by our external service
2. **JWKS Integration**: OpenBao fetches public keys from our `/.well-known/jwks.json` endpoint
3. **SPIFFE ID Validation**: CEL expressions can evaluate SPIFFE-specific claims
4. **Custom Claims**: Additional claims like `role` and `environment` are available in CEL
