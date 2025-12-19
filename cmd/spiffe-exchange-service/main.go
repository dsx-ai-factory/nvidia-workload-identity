package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// ExchangeRequest represents a request to exchange a SPIFFE JWT.
type ExchangeRequest struct {
	JWT string `json:"jwt"`
}

// ExchangeResponse represents the response from a JWT exchange.
type ExchangeResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	SpiffeID  string    `json:"spiffe_id"`
}

// ExchangeServer handles SPIFFE JWT exchange operations.
type ExchangeServer struct {
	keyStore   KeyStore
	jwtSource  *workloadapi.JWTSource
	region     string
	baseDomain string
}

// NewExchangeServer creates a new exchange server using the SPIFFE Workload API.
func NewExchangeServer(ctx context.Context, keyStore KeyStore, socketPath, region, baseDomain string) (*ExchangeServer, error) {
	opts := []workloadapi.JWTSourceOption{}
	if socketPath != "" {
		opts = append(opts, workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	}

	jwtSource, err := workloadapi.NewJWTSource(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT source: %w", err)
	}

	return &ExchangeServer{
		keyStore:   keyStore,
		jwtSource:  jwtSource,
		region:     region,
		baseDomain: baseDomain,
	}, nil
}

// Close releases resources held by the server.
func (s *ExchangeServer) Close() error {
	return s.jwtSource.Close()
}

// PathComponents holds extracted components from a SPIFFE ID path.
type PathComponents struct {
	Tenant       string // From /tenant/{tenant}/
	Service      string // From /service/{service}/
	ResourceType string // vm or pod
	ResourceID   string // UUID
}

// parsePathComponents extracts components from a SPIFFE ID path.
// Format: /tenant/{tenant}/service/{service}/{resource_type}/{resource_id}
func parsePathComponents(path string) (PathComponents, error) {
	const tenantPrefix = "/tenant/"
	if !strings.HasPrefix(path, tenantPrefix) {
		return PathComponents{}, fmt.Errorf("path does not contain /tenant/: %s", path)
	}

	rest := path[len(tenantPrefix):]
	parts := strings.Split(rest, "/")
	if len(parts) < 5 {
		return PathComponents{}, fmt.Errorf("path has insufficient components: %s", path)
	}

	if parts[1] != "service" {
		return PathComponents{}, fmt.Errorf("path missing /service/: %s", path)
	}

	return PathComponents{
		Tenant:       parts[0],
		Service:      parts[2],
		ResourceType: parts[3],
		ResourceID:   parts[4],
	}, nil
}

// transformSpiffeID transforms a SPIFFE ID by extracting components from the path
// and constructing a new trust domain with the pattern:
// {tenant}.{region}.{service}.{base-domain}
//
// Input:  spiffe://k2.sjc-1.nvidia.com/tenant/tenant1/service/vmaas/vm/{uuid}
// Output: spiffe://tenant1.sjc-1.vmaas.nvidia.com/vm/{uuid}
func transformSpiffeID(originalID spiffeid.ID, region, baseDomain string) (spiffeid.ID, error) {
	originalPath := originalID.Path()

	components, err := parsePathComponents(originalPath)
	if err != nil {
		return spiffeid.ID{}, err
	}

	// Construct new trust domain: {tenant}.{region}.{service}.{base-domain}
	newDomain := components.Tenant + "." + region + "." + components.Service + "." + baseDomain

	targetTD, err := spiffeid.TrustDomainFromString(newDomain)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("invalid target domain: %w", err)
	}

	// Construct new path: /{resource_type}/{resource_id}
	newPath := "/" + components.ResourceType + "/" + components.ResourceID

	newID, err := spiffeid.FromPath(targetTD, newPath)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("failed to create new SPIFFE ID: %w", err)
	}

	return newID, nil
}

// resignJWT re-signs a JWT with a new subject, preserving all other claims.
func (s *ExchangeServer) resignJWT(originalClaims map[string]any, newSubject spiffeid.ID) (string, error) {
	trustDomain := newSubject.TrustDomain().Name()
	slog.Info("resigning JWT", "trustDomain", trustDomain, "newSubject", newSubject)

	key, keyID, alg, err := s.keyStore.GetOrCreateKey(trustDomain)
	if err != nil {
		return "", fmt.Errorf("failed to get key: %w", err)
	}

	signerKey := jose.SigningKey{
		Algorithm: alg,
		Key:       key,
	}
	signerOpts := &jose.SignerOptions{}
	signerOpts.WithHeader(jose.HeaderKey("kid"), keyID)

	signer, err := jose.NewSigner(signerKey, signerOpts)
	if err != nil {
		return "", fmt.Errorf("failed to create signer: %w", err)
	}

	// Copy claims and update subject and issuer
	newClaims := make(map[string]any, len(originalClaims))
	maps.Copy(newClaims, originalClaims)
	newClaims["sub"] = newSubject.String()
	newClaims["iss"] = newSubject.TrustDomain().IDString()

	token, err := jwt.Signed(signer).Claims(newClaims).Serialize()
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return token, nil
}

// extractAudience extracts the audience claim from a JWT without full validation.
func extractAudience(token string) ([]string, error) {
	parsedJWT, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.RS384, jose.RS512, jose.ES256, jose.ES384, jose.ES512})
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT: %w", err)
	}

	var claims jwt.Claims
	if err := parsedJWT.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	return claims.Audience, nil
}

// handleExchange handles POST requests to /v1/exchange.
func (s *ExchangeServer) handleExchange(w http.ResponseWriter, r *http.Request) {
	slog.Info("Received exchange request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)

	body := http.MaxBytesReader(w, r.Body, 1024*1024)
	defer body.Close()

	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		slog.Warn("Invalid content type")
		http.Error(w, "Invalid content type", http.StatusUnsupportedMediaType)
		return
	}

	var req ExchangeRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		slog.Warn("Failed to decode request", "error", err)
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.JWT == "" {
		http.Error(w, "jwt is required", http.StatusBadRequest)
		return
	}

	audience, err := extractAudience(req.JWT)
	if err != nil {
		slog.Warn("Failed to extract audience", "error", err)
		http.Error(w, fmt.Sprintf("Invalid JWT: %v", err), http.StatusBadRequest)
		return
	}

	validatedSVID, err := jwtsvid.ParseAndValidate(req.JWT, s.jwtSource, audience)
	if err != nil {
		slog.Warn("JWT validation failed", "error", err)
		http.Error(w, fmt.Sprintf("JWT validation failed: %v", err), http.StatusUnauthorized)
		return
	}

	slog.Info("JWT validated successfully", "original_spiffe_id", validatedSVID.ID.String())

	newID, err := transformSpiffeID(validatedSVID.ID, s.region, s.baseDomain)
	if err != nil {
		slog.Warn("Failed to transform SPIFFE ID", "error", err)
		http.Error(w, fmt.Sprintf("Failed to transform SPIFFE ID: %v", err), http.StatusBadRequest)
		return
	}

	slog.Info("Transformed SPIFFE ID", "original", validatedSVID.ID.String(), "new", newID.String())

	token, err := s.resignJWT(validatedSVID.Claims, newID)
	if err != nil {
		slog.Error("Failed to sign JWT", "error", err)
		http.Error(w, fmt.Sprintf("Failed to sign JWT: %v", err), http.StatusInternalServerError)
		return
	}

	resp := ExchangeResponse{
		Token:     token,
		ExpiresAt: validatedSVID.Expiry,
		SpiffeID:  newID.String(),
	}

	slog.Info("Successfully exchanged JWT", "new_spiffe_id", newID.String())

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("Failed to encode response", "error", err)
	}
}

// handleJWKS handles GET requests to /.well-known/jwks.json.
func (s *ExchangeServer) handleJWKS(w http.ResponseWriter, r *http.Request) {
	slog.Info("Received JWKS request", "method", r.Method, "path", r.URL.Path, "host", r.Host, "remote_addr", r.RemoteAddr)

	// Extract trust domain from Host header (strip port if present)
	trustDomain := r.Host
	if idx := strings.Index(trustDomain, ":"); idx != -1 {
		trustDomain = trustDomain[:idx]
	}
	if trustDomain == "" {
		http.Error(w, "Host header required", http.StatusBadRequest)
		return
	}

	keys, err := s.keyStore.GetKeys(trustDomain)
	if err != nil {
		slog.Error("Failed to get keys for domain", "domain", trustDomain, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	jwks := jose.JSONWebKeySet{Keys: make([]jose.JSONWebKey, len(keys))}
	for i, dk := range keys {
		jwks.Keys[i] = jose.JSONWebKey{
			Key:       dk.Signer.Public(),
			KeyID:     dk.KeyID.String(),
			Algorithm: string(dk.Algorithm),
			Use:       "sig",
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jwks); err != nil {
		slog.Error("Failed to encode JWKS", "error", err)
	}
}

func main() {
	var (
		listenAddr = flag.String("listen", ":8080", "HTTP server listen address")
		socketPath = flag.String("socket", "", "SPIFFE Workload API socket path")
		region     = flag.String("region", "", "Region identifier (e.g., sjc-1)")
		baseDomain = flag.String("base-domain", "", "Base trust domain (e.g., nvidia.com)")
		logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

	if *region == "" {
		fmt.Fprintln(os.Stderr, "error: --region is required")
		os.Exit(1)
	}
	if *baseDomain == "" {
		fmt.Fprintln(os.Stderr, "error: --base-domain is required")
		os.Exit(1)
	}

	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))

	slog.Info("Starting SPIFFE Exchange Service",
		"listen_addr", *listenAddr,
		"socket_path", *socketPath,
		"region", *region,
		"base_domain", *baseDomain,
		"log_level", level.String())

	ctx := context.Background()
	keyStore := NewInMemoryKeyStore(ES256KeyGenerator{})

	server, err := NewExchangeServer(ctx, keyStore, *socketPath, *region, *baseDomain)
	if err != nil {
		slog.Error("Failed to create exchange server", "error", err)
		os.Exit(1)
	}
	defer server.Close()

	http.HandleFunc("POST /v1/exchange", server.handleExchange)
	http.HandleFunc("GET /.well-known/jwks.json", server.handleJWKS)
	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	slog.Info("Starting HTTP server", "listen_addr", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, nil); err != nil {
		slog.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
