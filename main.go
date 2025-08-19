package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// SPIFFEJWTClaims represents the custom claims for SPIFFE JWT
type SPIFFEJWTClaims struct {
	jwt.RegisteredClaims
	Claims map[string]interface{} `json:"claims"`
}

// SPIFFEJWTIssuer handles SPIFFE JWT creation
type SPIFFEJWTIssuer struct {
	privateKey *rsa.PrivateKey
	issuerID   spiffeid.ID
}

// NewSPIFFEJWTIssuer creates a new SPIFFE JWT issuer
func NewSPIFFEJWTIssuer(issuerID spiffeid.ID) (*SPIFFEJWTIssuer, error) {
	// Generate RSA private key for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	return &SPIFFEJWTIssuer{
		privateKey: privateKey,
		issuerID:   issuerID,
	}, nil
}

// IssueJWT creates a SPIFFE JWT with the specified parameters
func (issuer *SPIFFEJWTIssuer) IssueJWT(subject spiffeid.ID, audiences []string, ttl time.Duration, customClaims map[string]interface{}) (string, error) {
	now := time.Now()

	claims := SPIFFEJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer.issuerID.String(),
			Subject:   subject.String(),
			Audience:  audiences,
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
		Claims: customClaims,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)

	return token.SignedString(issuer.privateKey)
}

// GenerateSPIFFEID creates a SPIFFE ID based on the specified format
func GenerateSPIFFEID(region, cloudTenantID, domain, productName, vmUUID string) (spiffeid.ID, error) {
	// Format: spiffe://<region>.<cloud_tenant_id>.nvidia.<com|cn>/<product_name>/vm/<uuid>
	// Note: SPIFFE trust domains must be lowercase
	trustDomain := fmt.Sprintf("%s.%s.nvidia.%s",
		strings.ToLower(region),
		strings.ToLower(cloudTenantID),
		strings.ToLower(domain))
	path := fmt.Sprintf("/%s/vm/%s", strings.ToLower(productName), vmUUID)

	return spiffeid.FromString(fmt.Sprintf("spiffe://%s%s", trustDomain, path))
}

// WithWorkloadAPI demonstrates using the actual SPIFFE Workload API
func WithWorkloadAPI(ctx context.Context) error {
	// Create JWT source for JWT-SVID
	jwtSource, err := workloadapi.NewJWTSource(ctx)
	if err != nil {
		return fmt.Errorf("failed to create JWT source: %w", err)
	}
	defer jwtSource.Close()

	// Fetch JWT-SVID
	jwtSVID, err := jwtSource.FetchJWTSVID(ctx, jwtsvid.Params{
		Audience: "spiffe://example.com/audience",
	})
	if err != nil {
		return fmt.Errorf("failed to fetch JWT-SVID: %w", err)
	}

	fmt.Printf("JWT-SVID Token: %s\n", jwtSVID.Marshal())

	return nil
}

func main() {

	// Run SPIFFE ID format examples
	ExampleConfigurations()

	// Run complete JWT issuance demo
	if err := DemoJWTIssuance(); err != nil {
		log.Printf("Demo JWT issuance failed: %v", err)
	}

	// Example 1: Manual JWT creation (for demonstration)
	fmt.Println("=== Manual JWT Creation Example ===")

	// Create issuer SPIFFE ID
	issuerID, err := spiffeid.FromString("spiffe://sjc-1.k8s2-control.ncp.com/spire/server")
	if err != nil {
		log.Fatalf("Failed to create issuer SPIFFE ID: %v", err)
	}

	// Create JWT issuer
	issuer, err := NewSPIFFEJWTIssuer(issuerID)
	if err != nil {
		log.Fatalf("Failed to create JWT issuer: %v", err)
	}

	// Generate subject SPIFFE ID using the specified format
	vmUUID := uuid.New().String()
	subjectID, err := GenerateSPIFFEID("SJC-1", "k8s2-control", "com", "controlplane", vmUUID)
	if err != nil {
		log.Fatalf("Failed to generate subject SPIFFE ID: %v", err)
	}

	// Define audiences
	audiences := []string{
		"spiffe://sjc-1.k8s2-control.ncp.com/controlplane/vm/fluentd",
		"ssa://SSA_service_id",
		"sf://Starfleet_service_id",
	}

	// Custom claims
	customClaims := map[string]interface{}{
		"region":     "SJC-1",
		"tenant":     "k8s2-control",
		"product":    "controlplane",
		"vm_uuid":    vmUUID,
		"deployment": "production",
	}

	// Issue JWT with 1 hour TTL
	token, err := issuer.IssueJWT(subjectID, audiences, time.Hour, customClaims)
	if err != nil {
		log.Fatalf("Failed to issue JWT: %v", err)
	}

	fmt.Printf("Issued JWT Token:\n%s\n\n", token)
	fmt.Printf("Subject SPIFFE ID: %s\n", subjectID.String())
	fmt.Printf("Issuer SPIFFE ID: %s\n", issuerID.String())
	fmt.Printf("Audiences: %v\n", audiences)
	fmt.Printf("Custom Claims: %+v\n\n", customClaims)

	// Validate the JWT we just created
	fmt.Println("=== JWT Validation Example ===")
	if err := ValidateJWTExample(token, issuer); err != nil {
		log.Printf("JWT validation failed: %v", err)
	}

	// Example 2: Using actual SPIFFE Workload API (requires SPIRE agent)
	fmt.Println("\n=== SPIFFE Workload API Example ===")
	fmt.Println("Note: This requires a running SPIRE agent and proper workload registration")

	// Uncomment the following line to test with actual SPIFFE Workload API
	if err := WithWorkloadAPI(context.Background()); err != nil {
	    log.Printf("Workload API example failed (this is expected without SPIRE agent): %v", err)
	}

	// Example 3: TLS configuration with SPIFFE
	fmt.Println("\n=== SPIFFE TLS Configuration Example ===")

	// This shows how to create TLS config using SPIFFE (requires SPIRE agent)
	// import "github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	// tlsConfig := tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeAny())
	fmt.Println("TLS configuration would use SPIFFE X.509 certificates for mTLS")

	fmt.Println("\nTo use with actual SPIRE:")
	fmt.Println("1. Start SPIRE server and agent")
	fmt.Println("2. Register this workload with appropriate SPIFFE ID")
	fmt.Println("3. Uncomment the WorkloadAPI example code")
	fmt.Println("4. Set SPIFFE_ENDPOINT_SOCKET environment variable")
}
