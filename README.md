# SPIFFE JWT + OpenBao CEL Demo

Demonstrates SPIFFE JWT issuance and validation using OpenBao's CEL expression engine.

## Components

- **SPIFFE JWT Issuer** - Generates JWTs with NVIDIA SPIFFE ID format
- **CEL Evaluator** - Tests CEL expressions against SPIFFE JWTs  
- **OpenBao Integration** - JWT auth backend with CEL role policies

## Quick Start

```bash
# Generate SPIFFE JWTs
go run .

# Test CEL expressions
go run cmd/cel-evaluator cel-role.json

# Demo OpenBao integration
cat demo.md
```

## SPIFFE ID Format

`spiffe://<region>.<tenant>.nvidia.<domain>/<product>/vm/<resource-id>`

Examples:
- `spiffe://sjc-1.k8s2-control.nvidia.com/controlplane/vm/uuid`
- `spiffe://bjs-1.k8s3-control.nvidia.cn/vmaas/vm/fluentd`

## Files

- `main.go` - SPIFFE JWT issuer
- `examples.go` - JWT generation examples
- `cmd/cel-evaluator/` - CEL expression tester
- `cel-role.json` - SPIFFE-aware CEL role
- `config.yaml` - Configuration
- `demo.md` - OpenBao setup guide
