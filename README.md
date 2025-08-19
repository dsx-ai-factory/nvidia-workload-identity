# SPIFFE JWT + OpenBao CEL Demo

Demonstrates SPIFFE JWT issuance and validation using OpenBao's CEL expression engine.

## Components

- **SPIFFE JWT Issuer** - HTTP service that issues JWTs with NVIDIA SPIFFE ID format
- **CEL Evaluator** - Tests CEL expressions against SPIFFE JWTs  
- **OpenBao Integration** - JWT auth backend with CEL role policies

## Quick Start

```bash
# Start JWT issuer service
go run .

# Test CEL expressions
go run cmd/cel-evaluator cel-role.json

# Demo OpenBao integration
cat demo.md
```

## API Examples

```bash
# Issue JWT
curl -X POST http://localhost:8080/token \
  -H "Content-Type: application/json" \
  -d '{
    "issuer_id": "spiffe://sjc-1.k8s2-control.ncp.com/spire/server",
    "region": "sjc-1",
    "cloud_tenant_id": "k8s2-control", 
    "domain": "com",
    "product_name": "controlplane",
    "resource_name": "fluentbit-vm-001",
    "audiences": ["spiffe://sjc-1.k8s2-control.ncp.com/controlplane/vm/fluentd"],
    "ttl": "1h",
    "custom_claims": {"role": "log-forwarder"}
  }'

# Validate JWT
curl -X POST http://localhost:8080/validate \
  -H "Content-Type: application/json" \
  -d '{"token": "your_jwt_token_here"}'

# Get JWKS
curl http://localhost:8080/.well-known/jwks.json
```

## SPIFFE ID Format

`spiffe://<region>.<tenant>.nvidia.<domain>/<product>/vm/<resource-id>`

Examples:
- `spiffe://sjc-1.k8s2-control.nvidia.com/controlplane/vm/uuid`
- `spiffe://bjs-1.k8s3-control.nvidia.cn/vmaas/vm/fluentd`

## Files

- `main.go` - SPIFFE JWT issuer service
- `examples.go` - JWT generation examples
- `cmd/cel-evaluator/` - CEL expression tester
- `cel-role.json` - SPIFFE-aware CEL role
- `demo.md` - OpenBao setup guide
