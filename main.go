package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// SPIFFEJWTClaims represents the custom claims for SPIFFE JWT
type SPIFFEJWTClaims struct {
	jwt.RegisteredClaims
	Claims map[string]any `json:"claims"`
}

// SPIFFEJWTIssuer handles SPIFFE JWT creation
type SPIFFEJWTIssuer struct {
	privateKey *rsa.PrivateKey
	issuerID   spiffeid.ID
}

// API request and response structs
type TokenRequest struct {
	IssuerID      string         `json:"issuer_id"`
	Region        string         `json:"region"`
	CloudTenantID string         `json:"cloud_tenant_id"`
	Domain        string         `json:"domain"`
	ProductName   string         `json:"product_name"`
	ResourceName  string         `json:"resource_name,omitempty"`
	Audiences     []string       `json:"audiences"`
	CustomClaims  map[string]any `json:"custom_claims,omitempty"`
	TTL           string         `json:"ttl"`
}

type TokenResponse struct {
	AccessToken string   `json:"access_token"`
	TokenType   string   `json:"token_type"`
	ExpiresIn   int      `json:"expires_in"`
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	Audiences   []string `json:"audiences"`
}

type ValidateRequest struct {
	Token string `json:"token"`
}

type ValidateResponse struct {
	Valid    bool           `json:"valid"`
	Error    string         `json:"error,omitempty"`
	Issuer   string         `json:"issuer,omitempty"`
	Subject  string         `json:"subject,omitempty"`
	Audience []string       `json:"audience,omitempty"`
	Expires  int64          `json:"expires,omitempty"`
	Claims   map[string]any `json:"claims,omitempty"`
}

// Server represents the HTTP server with SPIFFE JWT issuer
type Server struct {
	issuer  *SPIFFEJWTIssuer
	keyfunc keyfunc.Keyfunc
	mux     *http.ServeMux
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
func (issuer *SPIFFEJWTIssuer) IssueJWT(subject spiffeid.ID, audiences []string, ttl time.Duration, customClaims map[string]any) (string, error) {
	return issuer.IssueJWTWithIssuer(subject, audiences, ttl, customClaims, issuer.issuerID)
}

// IssueJWTWithIssuer creates a SPIFFE JWT with a custom issuer ID
func (issuer *SPIFFEJWTIssuer) IssueJWTWithIssuer(subject spiffeid.ID, audiences []string, ttl time.Duration, customClaims map[string]any, issuerID spiffeid.ID) (string, error) {
	now := time.Now()

	claims := SPIFFEJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuerID.String(),
			Subject:   subject.String(),
			Audience:  audiences,
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
		Claims: customClaims,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)

	// Set the key ID header to match the JWKS
	token.Header["kid"] = "spiffe-jwt-key"

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

// NewServer creates a new server instance
func NewServer() (*Server, error) {
	// Create a dummy issuer SPIFFE ID (will be overridden per request)
	issuerID, err := spiffeid.FromString("spiffe://localhost/server")
	if err != nil {
		return nil, fmt.Errorf("failed to create issuer SPIFFE ID: %w", err)
	}

	// Create JWT issuer
	issuer, err := NewSPIFFEJWTIssuer(issuerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT issuer: %w", err)
	}

	// Create a JWK from the RSA public key
	jwk, err := jwkset.NewJWKFromKey(&issuer.privateKey.PublicKey, jwkset.JWKOptions{
		Metadata: jwkset.JWKMetadataOptions{
			KID: "spiffe-jwt-key",
			ALG: jwkset.AlgRS256,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWK: %w", err)
	}

	// Create a storage and add the JWK
	storage := jwkset.NewMemoryStorage()
	err = storage.KeyWrite(context.Background(), jwk)
	if err != nil {
		return nil, fmt.Errorf("failed to write JWK to storage: %w", err)
	}

	// Create keyfunc with the storage
	keyFunc, err := keyfunc.New(keyfunc.Options{
		Storage: storage,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create keyfunc: %w", err)
	}

	server := &Server{
		issuer:  issuer,
		keyfunc: keyFunc,
		mux:     http.NewServeMux(),
	}

	// Setup routes
	server.mux.HandleFunc("GET /.well-known/jwks.json", server.jwksHandler)
	server.mux.HandleFunc("POST /token", server.tokenHandler)
	server.mux.HandleFunc("POST /validate", server.validateHandler)
	server.mux.HandleFunc("GET /health", server.healthHandler)

	return server, nil
}

// ServeHTTP implements http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// jwksHandler serves the JSON Web Key Set
func (s *Server) jwksHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	// Get the JWKS from keyfunc's storage
	storage := s.keyfunc.Storage()
	jwksJSON, err := storage.JSONPublic(context.Background())
	if err != nil {
		log.Printf("Error getting JWKS JSON: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Write(jwksJSON)
}

// tokenHandler issues JWT tokens
func (s *Server) tokenHandler(w http.ResponseWriter, r *http.Request) {
	// Parse request
	var req TokenRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON request", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.IssuerID == "" {
		http.Error(w, "issuer_id is required", http.StatusBadRequest)
		return
	}
	if req.Region == "" {
		http.Error(w, "region is required", http.StatusBadRequest)
		return
	}
	if req.CloudTenantID == "" {
		http.Error(w, "cloud_tenant_id is required", http.StatusBadRequest)
		return
	}
	if req.Domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	if req.ProductName == "" {
		http.Error(w, "product_name is required", http.StatusBadRequest)
		return
	}
	if len(req.Audiences) == 0 {
		http.Error(w, "audiences is required", http.StatusBadRequest)
		return
	}
	if req.TTL == "" {
		http.Error(w, "ttl is required", http.StatusBadRequest)
		return
	}

	// Generate resource name if not provided
	if req.ResourceName == "" {
		req.ResourceName = uuid.New().String()
	}

	// Parse and validate issuer SPIFFE ID
	issuerID, err := spiffeid.FromString(req.IssuerID)
	if err != nil {
		log.Printf("Invalid issuer SPIFFE ID: %v", err)
		http.Error(w, "Invalid issuer SPIFFE ID", http.StatusBadRequest)
		return
	}

	// Generate subject SPIFFE ID
	subjectID, err := GenerateSPIFFEID(req.Region, req.CloudTenantID, req.Domain, req.ProductName, req.ResourceName)
	if err != nil {
		log.Printf("Failed to generate subject SPIFFE ID: %v", err)
		http.Error(w, "Failed to generate SPIFFE ID", http.StatusInternalServerError)
		return
	}

	// Parse TTL
	ttl, err := time.ParseDuration(req.TTL)
	if err != nil {
		log.Printf("Invalid TTL format: %v", err)
		http.Error(w, "Invalid TTL format", http.StatusBadRequest)
		return
	}

	// Issue JWT using the server's issuer with the requested issuer ID
	token, err := s.issuer.IssueJWTWithIssuer(subjectID, req.Audiences, ttl, req.CustomClaims, issuerID)
	if err != nil {
		log.Printf("Failed to issue JWT: %v", err)
		http.Error(w, "Failed to issue JWT", http.StatusInternalServerError)
		return
	}

	// Response
	response := TokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(ttl.Seconds()),
		Subject:     subjectID.String(),
		Issuer:      issuerID.String(),
		Audiences:   req.Audiences,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding token response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// validateHandler validates JWT tokens using keyfunc
func (s *Server) validateHandler(w http.ResponseWriter, r *http.Request) {
	var req ValidateRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON request", http.StatusBadRequest)
		return
	}

	if req.Token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	// Parse and validate the JWT using keyfunc
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		return s.keyfunc.Keyfunc(token)
	}
	token, err := jwt.ParseWithClaims(req.Token, &SPIFFEJWTClaims{}, keyFunc)
	if err != nil {
		log.Printf("JWT validation failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(ValidateResponse{
			Valid: false,
			Error: err.Error(),
		})
		return
	}

	if !token.Valid {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(ValidateResponse{
			Valid: false,
			Error: "token is not valid",
		})
		return
	}

	// Extract claims
	if claims, ok := token.Claims.(*SPIFFEJWTClaims); ok {
		w.Header().Set("Content-Type", "application/json")
		resp := ValidateResponse{
			Valid:    true,
			Issuer:   claims.Issuer,
			Subject:  claims.Subject,
			Audience: claims.Audience,
			Expires:  claims.ExpiresAt.Time.Unix(),
			Claims:   claims.Claims,
		}
		json.NewEncoder(w).Encode(resp)
	} else {
		http.Error(w, "Failed to extract claims", http.StatusInternalServerError)
	}
}

// healthHandler provides health check endpoint
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func main() {
	// Create server
	server, err := NewServer()
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Create HTTP server
	httpServer := &http.Server{
		Addr:    ":8080",
		Handler: server,
	}

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		log.Printf("Starting SPIFFE JWT server on %s", httpServer.Addr)
		log.Printf("JWKS endpoint: http://localhost%s/.well-known/jwks.json", httpServer.Addr)
		log.Printf("Token endpoint: http://localhost%s/token", httpServer.Addr)
		log.Printf("Validate endpoint: http://localhost%s/validate", httpServer.Addr)
		log.Printf("Health endpoint: http://localhost%s/health", httpServer.Addr)

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	log.Println("Shutting down server...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown server gracefully
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	} else {
		log.Println("Server shut down gracefully")
	}
}
