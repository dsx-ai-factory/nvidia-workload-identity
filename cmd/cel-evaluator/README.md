# CEL Role Evaluator

This program evaluates CEL roles using the OpenBao CEL helper package to test and debug CEL expressions before deploying them to OpenBao.

## Usage

```bash
# From the project root, install dependencies
go mod tidy

# Run the evaluator with your CEL role
go run ./cmd/cel-evaluator cel-role.json

# Or build and run
go build -o cel-evaluator ./cmd/cel-evaluator
./cel-evaluator cel-role.json
```

## Features

- **Validates CEL Programs**: Uses `openbaocel.ValidateProgram()` to check syntax
- **Evaluates Variables**: Tests each variable in the CEL program sequentially
- **Evaluates Main Expression**: Tests the final authentication expression
- **Multiple Test Cases**: Tests with different SPIFFE ID scenarios
- **Detailed Output**: Shows variable results and final policy assignments

## Sample Output

```
Evaluating CEL Role: spiffe-example-role
Expression: pb.Auth{policies: is_spiffe ? ['spiffe-authenticated', 'region-' + region, 'product-' + product] : ['example']}
Variables: 4
CEL program validation passed

=== Sample Claims ===
{
  "claims": {
    "sub": "spiffe://sjc-1.k8s2-control.nvidia.com/controlplane/vm/12345678-1234-5678-9abc-123456789abc",
    "aud": ["spiffe://sjc-1.k8s2-control.ncp.com/controlplane/vm/fluentd", "ssa://SSA_service_id"],
    ...
  }
}

=== Evaluating Variables ===
Variable 1: subject_parts
  Expression: jwt.claims.sub.split('/')
  Error: no such overload

Variable 2: is_spiffe  
  Expression: size(jwt.claims.sub) > 0 && jwt.claims.sub.startsWith('spiffe://')
  Result: true (type: bool)
...
```

## OpenBao CEL Functions

The program uses the OpenBao CEL helper package which provides:

- `ValidateProgram()` - Validates CEL program syntax
- `RegisterAllCelFunctions()` - Registers OpenBao-specific CEL functions  
- `CelVarsToEvalData()` - Converts CEL variables to evaluation data
- `ParseCompileAndEvaluateVariable()` - Evaluates individual variables
- `ParseCompileAndEvaluateExpression()` - Evaluates the main expression

## Test Data

The evaluator includes sample JWT claims matching the pattern:
```
spiffe://<tenant>.<region>.<domain>/<product>/<resource_type>/<resource_id>
```

**Pod Example**:
```
spiffe://tenant1.sjc-1.k8s2-control.nvidia.com/default/pod/abc123-def456
```
- Trust domain: `tenant1.sjc-1.k8s2-control.nvidia.com` → tenant=tenant1, region=sjc-1
- Path: `/default/pod/abc123-def456` → product=default, resource_type=pod, resource_id=abc123-def456

**VM Example**:
```
spiffe://tenant1.sjc-1.k8s2-control.nvidia.com/vmaas/vm/123abc-456def
```
- Trust domain: `tenant1.sjc-1.k8s2-control.nvidia.com` → tenant=tenant1, region=sjc-1
- Path: `/vmaas/vm/123abc-456def` → product=vmaas, resource_type=vm, resource_id=123abc-456def

This validates CEL expressions parse the pattern correctly before deploying to OpenBao.
