package main

import (
	"fmt"
	"log"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// ExampleConfigurations demonstrates different SPIFFE ID patterns
func ExampleConfigurations() {
	fmt.Println("=== SPIFFE ID Format Examples ===")

	// Example 1: US domain (.com)
	vmUUID1 := uuid.New().String()
	spiffeID1, err := GenerateSPIFFEID("SJC-1", "k8s2-control", "com", "controlplane", vmUUID1)
	if err != nil {
		log.Printf("Error generating SPIFFE ID 1: %v", err)
		return
	}
	fmt.Printf("US Domain Example: %s\n", spiffeID1.String())

	// Example 2: China domain (.cn)
	vmUUID2 := uuid.New().String()
	spiffeID2, err := GenerateSPIFFEID("BJS-1", "k8s3-control", "cn", "vmaas", vmUUID2)
	if err != nil {
		log.Printf("Error generating SPIFFE ID 2: %v", err)
		return
	}
	fmt.Printf("China Domain Example: %s\n", spiffeID2.String())

	// Example 3: Different product
	vmUUID3 := uuid.New().String()
	spiffeID3, err := GenerateSPIFFEID("NYC-1", "prod-cluster", "com", "storage-service", vmUUID3)
	if err != nil {
		log.Printf("Error generating SPIFFE ID 3: %v", err)
		return
	}
	fmt.Printf("Storage Service Example: %s\n", spiffeID3.String())

	fmt.Println()
}

// DemoJWTIssuance shows complete JWT issuance flow
func DemoJWTIssuance() error {
	fmt.Println("=== Complete JWT Issuance Demo ===")

	// Create issuer
	issuerID, err := spiffeid.FromString("spiffe://sjc-1.k8s2-control.ncp.com/spire/server")
	if err != nil {
		return fmt.Errorf("failed to create issuer ID: %w", err)
	}

	issuer, err := NewSPIFFEJWTIssuer(issuerID)
	if err != nil {
		return fmt.Errorf("failed to create issuer: %w", err)
	}

	// Scenario 1: FluentBit requesting access to FluentD
	fmt.Println("--- Scenario 1: FluentBit to FluentD ---")
	vmUUID := uuid.New().String()
	subjectID, err := GenerateSPIFFEID("SJC-1", "k8s2-control", "com", "controlplane", vmUUID)
	if err != nil {
		return fmt.Errorf("failed to generate subject ID: %w", err)
	}

	audiences := []string{
		"spiffe://sjc-1.k8s2-control.ncp.com/controlplane/vm/fluentd",
	}

	claims := map[string]interface{}{
		"service_type": "logging",
		"role":         "log-forwarder",
		"region":       "SJC-1",
		"tenant":       "k8s2-control",
		"vm_uuid":      vmUUID,
	}

	token, err := issuer.IssueJWT(subjectID, audiences, time.Hour, claims)
	if err != nil {
		return fmt.Errorf("failed to issue JWT: %w", err)
	}

	fmt.Printf("Subject: %s\n", subjectID.String())
	fmt.Printf("Audience: %s\n", audiences[0])
	fmt.Printf("JWT: %s\n\n", token)

	// Scenario 2: Multi-audience JWT for service mesh
	fmt.Println("--- Scenario 2: Multi-Audience Service Mesh ---")
	vmUUID2 := uuid.New().String()
	subjectID2, err := GenerateSPIFFEID("SJC-1", "k8s2-control", "com", "vmaas", vmUUID2)
	if err != nil {
		return fmt.Errorf("failed to generate subject ID 2: %w", err)
	}

	multiAudiences := []string{
		"spiffe://sjc-1.k8s2-control.ncp.com/vmaas/vm/compute-service",
		"spiffe://sjc-1.k8s2-control.ncp.com/vmaas/vm/storage-service",
		"ssa://compute_cluster_id",
		"sf://starfleet_deployment_id",
	}

	meshClaims := map[string]interface{}{
		"service_mesh":   true,
		"capabilities":   []string{"compute", "storage", "network"},
		"security_level": "high",
		"region":         "SJC-1",
		"tenant":         "k8s2-control",
		"vm_uuid":        vmUUID2,
	}

	token2, err := issuer.IssueJWT(subjectID2, multiAudiences, 30*time.Minute, meshClaims)
	if err != nil {
		return fmt.Errorf("failed to issue multi-audience JWT: %w", err)
	}

	fmt.Printf("Subject: %s\n", subjectID2.String())
	fmt.Printf("Audiences: %v\n", multiAudiences)
	fmt.Printf("JWT: %s\n\n", token2)

	return nil
}

// ValidateJWTExample shows how to validate the issued JWTs
func ValidateJWTExample(tokenString string, issuer *SPIFFEJWTIssuer) error {
	fmt.Println("=== JWT Validation Example ===")

	// Parse the token
	token, err := jwt.ParseWithClaims(tokenString, &SPIFFEJWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// Return the public key for validation
		return &issuer.privateKey.PublicKey, nil
	})

	if err != nil {
		return fmt.Errorf("failed to parse token: %w", err)
	}

	if claims, ok := token.Claims.(*SPIFFEJWTClaims); ok && token.Valid {
		fmt.Printf("Valid JWT!\n")
		fmt.Printf("Issuer: %s\n", claims.Issuer)
		fmt.Printf("Subject: %s\n", claims.Subject)
		fmt.Printf("Audiences: %v\n", claims.Audience)
		fmt.Printf("Expires: %s\n", claims.ExpiresAt.Time.Format(time.RFC3339))
		fmt.Printf("Custom Claims: %+v\n", claims.Claims)
	} else {
		return fmt.Errorf("invalid token or claims")
	}

	return nil
}
