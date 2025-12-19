package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/common/types/ref"
	celhelper "github.com/openbao/openbao/sdk/v2/helper/cel"
	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/openbao/openbao/sdk/v2/plugin/pb"
)

// CELRole represents the structure of a CEL role from the JSON file
type CELRole struct {
	Name             string               `json:"name"`
	CelProgram       celhelper.CelProgram `json:"cel_program"`
	ExpirationLeeway int                  `json:"expiration_leeway"`
	NotBeforeLeeway  int                  `json:"not_before_leeway"`
	ClockSkewLeeway  int                  `json:"clock_skew_leeway"`
	BoundAudiences   []string             `json:"bound_audiences"`
}

// celEvalProgram mimics the OpenBao JWT auth backend CEL evaluation
func celEvalProgram(program celhelper.CelProgram, operation logical.Operation, jwtClaims map[string]any) (any, error) {
	env, err := celEnv(program)
	if err != nil {
		return nil, err
	}

	// Initialize the evaluation context for CEL expressions with the raw request data.
	// The "claims" key allows CEL expressions to access and evaluate against JWT claims.
	// Additional variables and evaluated results will be added dynamically during processing.
	evaluationData := map[string]any{
		"claims":    jwtClaims,
		"now":       time.Now(),
		"operation": string(operation),
	}

	// Evaluate all variables
	for _, variable := range program.Variables {
		result, err := celhelper.ParseCompileAndEvaluateVariable(env, variable, evaluationData)
		if err != nil {
			return nil, fmt.Errorf("error evaluating variable '%s': %w", variable.Name, err)
		}

		// Add the evaluated result for subsequent CEL evaluations.
		// This ensures variables can reference each other and build a cumulative evaluation context.
		evaluationData[variable.Name] = result.Value()
	}

	// Evaluate the CEL Role success expression
	return celhelper.ParseCompileAndEvaluateExpression(env, program.Expression, evaluationData)
}

// runCelProgram mimics the OpenBao JWT auth backend runCelProgram function
func runCelProgram(ctx context.Context, operation logical.Operation, roleName string, program celhelper.CelProgram, allClaims map[string]any) (*pb.Auth, error) {
	result, err := celEvalProgram(program, operation, allClaims)
	if err != nil {
		return nil, fmt.Errorf("Cel role auth program failed: %w", err)
	}

	refVal := result.(ref.Val)

	// process result from CEL program
	switch v := refVal.Value().(type) {
	// if boolean false return auth failed
	case bool:
		if !v {
			return nil, fmt.Errorf("Cel role '%s' blocked authorization with boolean false return", roleName)
		}
	// if string, return this as auth failed message
	case string:
		return nil, fmt.Errorf("Cel role '%s' blocked authorization with message: %s", roleName, v)
	}

	// handle protobuf Auth return type
	if msg, err := refVal.ConvertToNative(reflect.TypeOf(&pb.Auth{})); err == nil {
		pbAuth, ok := msg.(*pb.Auth)
		if ok {
			return pbAuth, nil
		}
	}

	return nil, fmt.Errorf("Cel program '%s' returned unexpected type: %T", roleName, result)
}

// celEnv creates a CEL environment similar to OpenBao's implementation
func celEnv(program celhelper.CelProgram) (*cel.Env, error) {
	envOptions := []cel.EnvOption{}

	// Add all variable declarations to the CEL environment.
	for _, variable := range program.Variables {
		envOptions = append(envOptions, cel.Declarations(decls.NewVar(variable.Name, decls.Dyn)))
	}

	// Add "pb.Auth" return type to environment
	envOptions = append(envOptions, cel.Types(&pb.Auth{}))

	// Add standard CEL declarations for claims, now, and operation
	envOptions = append(envOptions, cel.Declarations(
		decls.NewVar("claims", decls.NewMapType(decls.String, decls.Dyn)),
		decls.NewVar("now", decls.Timestamp),
		decls.NewVar("operation", decls.String),
	))
	// Add "pb.Auth" return type to environment
	envOptions = append(envOptions, cel.Types(&pb.Auth{}))

	env, err := cel.NewEnv(envOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	return celhelper.RegisterAllCelFunctions(env)
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: go run main.go <cel-role.json>")
	}

	// Read the CEL role file
	roleFile := os.Args[1]
	data, err := os.ReadFile(roleFile)
	if err != nil {
		log.Fatalf("Failed to read role file %s: %v", roleFile, err)
	}

	// Parse the CEL role
	var role CELRole
	if err := json.Unmarshal(data, &role); err != nil {
		log.Fatalf("Failed to parse CEL role: %v", err)
	}

	fmt.Printf("Evaluating CEL Role: %s\n", role.Name)
	fmt.Printf("Expression: %s\n", role.CelProgram.Expression)
	fmt.Printf("Variables: %d\n", len(role.CelProgram.Variables))

	// Create sample JWT claims data for testing (matching OpenBao's format)
	// Example 1: Pod SPIFFE ID
	podClaims := map[string]any{
		"sub": "spiffe://tenant1.sjc-1.internal.nvidia.com/pod/12345678-1234-5678-9abc-123456789abc",
		"aud": []string{"spiffe://tenant1.sjc-1.internal.nvidia.com"},
		"iss": "spiffe://tenant1.sjc-1.internal.nvidia.com",
		"exp": 1678886400,
		"iat": 1678882800,
	}

	// Example 2: VM SPIFFE ID
	vmClaims := map[string]any{
		"sub": "spiffe://tenant1.sjc-1.vmaas.nvidia.com/vm/87654321-4321-8765-cba9-987654321098",
		"aud": []string{"spiffe://tenant1.sjc-1.vmaas.nvidia.com"},
		"iss": "spiffe://tenant1.sjc-1.vmaas.nvidia.com",
		"exp": 1678886400,
		"iat": 1678882800,
	}

	fmt.Println("\n=== Sample Claims (Pod) ===")
	podClaimsJSON, _ := json.MarshalIndent(podClaims, "", "  ")
	fmt.Println(string(podClaimsJSON))

	fmt.Println("\n=== Sample Claims (VM) ===")
	vmClaimsJSON, _ := json.MarshalIndent(vmClaims, "", "  ")
	fmt.Println(string(vmClaimsJSON))

	fmt.Println("\n=== Evaluating CEL Program (Pod) ===")

	// Use the OpenBao-style evaluation with runCelProgram
	ctx := context.Background()
	pbAuth, err := runCelProgram(ctx, logical.UpdateOperation, role.Name, role.CelProgram, podClaims)
	if err != nil {
		fmt.Printf("CEL Program Failed: %v\n", err)
	} else {
		fmt.Printf("CEL Program Success\n")
		fmt.Printf("pb.Auth Result: %+v\n", pbAuth)

		if pbAuth.Policies != nil {
			fmt.Printf("Assigned Policies: %v\n", pbAuth.Policies)
		}
		if pbAuth.LeaseOptions != nil {
			fmt.Printf("Lease Duration: %d seconds\n", pbAuth.LeaseOptions.TTL)
			fmt.Printf("Renewable: %v\n", pbAuth.LeaseOptions.Renewable)
		}
	}

	fmt.Println("\n=== Evaluating CEL Program (VM) ===")
	pbAuth, err = runCelProgram(ctx, logical.UpdateOperation, role.Name, role.CelProgram, vmClaims)
	if err != nil {
		fmt.Printf("CEL Program Failed: %v\n", err)
	} else {
		fmt.Printf("CEL Program Success\n")
		fmt.Printf("pb.Auth Result: %+v\n", pbAuth)

		if pbAuth.Policies != nil {
			fmt.Printf("Assigned Policies: %v\n", pbAuth.Policies)
		}
		if pbAuth.LeaseOptions != nil {
			fmt.Printf("Lease Duration: %d seconds\n", pbAuth.LeaseOptions.TTL)
			fmt.Printf("Renewable: %v\n", pbAuth.LeaseOptions.Renewable)
		}
	}
}
