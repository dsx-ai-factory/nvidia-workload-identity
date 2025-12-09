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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
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
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	SpiffeID  string `json:"spiffe_id"`
}

// ExchangeServer handles SPIFFE JWT exchange operations.
type ExchangeServer struct {
	keyStore  KeyStore
	jwtSource *workloadapi.JWTSource
}

// NewExchangeServer creates a new exchange server using the SPIFFE Workload API.
func NewExchangeServer(ctx context.Context, keyStore KeyStore, socketPath string) (*ExchangeServer, error) {
	opts := []workloadapi.JWTSourceOption{}
	if socketPath != "" {
		opts = append(opts, workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	}

	jwtSource, err := workloadapi.NewJWTSource(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT source: %w", err)
	}

	return &ExchangeServer{
		keyStore:  keyStore,
		jwtSource: jwtSource,
	}, nil
}

// Close releases resources held by the server.
func (s *ExchangeServer) Close() error {
	return s.jwtSource.Close()
}

// parseTargetDomain extracts the subdomain from a SPIFFE ID path.
// Expected format: /domain/{subdomain}/rest/of/path
// Returns the subdomain and remaining path, or empty string if no domain component.
func parseTargetDomain(path string) (subdomain string, remainingPath string, ok bool) {
	const prefix = "/domain/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}

	rest := path[len(prefix):]
	idx := strings.Index(rest, "/")
	if idx == -1 {
		// No remaining path after domain
		return rest, "", true
	}

	return rest[:idx], rest[idx:], true
}

// transformSpiffeID transforms a SPIFFE ID by extracting the subdomain from the path
// and prepending it to the original trust domain.
// Input: spiffe://original.domain/domain/subdomain/rest/of/path
// Output: spiffe://subdomain.original.domain/rest/of/path
func transformSpiffeID(originalID spiffeid.ID) (spiffeid.ID, error) {
	originalDomain := originalID.TrustDomain().Name()
	originalPath := originalID.Path()

	subdomain, remainingPath, ok := parseTargetDomain(originalPath)
	if !ok {
		return spiffeid.ID{}, fmt.Errorf("SPIFFE ID path does not contain /domain/{subdomain}/: %s", originalPath)
	}

	// Construct new trust domain: {subdomain}.{original-domain}
	newDomain := subdomain + "." + originalDomain

	targetTD, err := spiffeid.TrustDomainFromString(newDomain)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("invalid target domain: %w", err)
	}

	newID, err := spiffeid.FromPath(targetTD, remainingPath)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("failed to create new SPIFFE ID: %w", err)
	}

	return newID, nil
}

// resignJWT re-signs a JWT with a new subject, preserving all other claims.
func (s *ExchangeServer) resignJWT(originalClaims map[string]any, newSubject spiffeid.ID) (string, error) {
	trustDomain := newSubject.TrustDomain().Name()

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

	// Copy claims and update subject
	newClaims := make(map[string]any, len(originalClaims))
	maps.Copy(newClaims, originalClaims)
	newClaims["sub"] = newSubject.String()

	token, err := jwt.Signed(signer).Claims(newClaims).Serialize()
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return token, nil
}

// extractAudience extracts the audience claim from a JWT without full validation.
// This is only used to obtain the audience for subsequent validation.
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
// Target domain is extracted from the SPIFFE ID path pattern /domain/{targetDomain}/...
func (s *ExchangeServer) handleExchange(w http.ResponseWriter, r *http.Request) {
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

	// Extract audience for validation.
	// We don't care about the audience so we'll extract what was given and pass it
	// to the validator which insists on an audience.
	audience, err := extractAudience(req.JWT)
	if err != nil {
		slog.Warn("Failed to extract audience", "error", err)
		http.Error(w, fmt.Sprintf("Invalid JWT: %v", err), http.StatusBadRequest)
		return
	}

	// Validate the JWT using go-spiffe SDK
	validatedSVID, err := jwtsvid.ParseAndValidate(req.JWT, s.jwtSource, audience)
	if err != nil {
		slog.Warn("JWT validation failed", "error", err)
		http.Error(w, fmt.Sprintf("JWT validation failed: %v", err), http.StatusUnauthorized)
		return
	}

	slog.Info("JWT validated successfully", "original_spiffe_id", validatedSVID.ID.String())

	// Transform the SPIFFE ID (target domain is parsed from the path)
	newID, err := transformSpiffeID(validatedSVID.ID)
	if err != nil {
		slog.Warn("Failed to transform SPIFFE ID", "error", err)
		http.Error(w, fmt.Sprintf("Failed to transform SPIFFE ID: %v", err), http.StatusBadRequest)
		return
	}

	slog.Info("Transformed SPIFFE ID", "original", validatedSVID.ID.String(), "new", newID.String())

	// Re-sign JWT with new subject, preserving all other claims
	token, err := s.resignJWT(validatedSVID.Claims, newID)
	if err != nil {
		slog.Error("Failed to sign JWT", "error", err)
		http.Error(w, fmt.Sprintf("Failed to sign JWT: %v", err), http.StatusInternalServerError)
		return
	}

	resp := ExchangeResponse{
		Token:     token,
		ExpiresAt: validatedSVID.Expiry.Format(time.RFC3339),
		SpiffeID:  newID.String(),
	}

	slog.Info("Successfully exchanged JWT", "new_spiffe_id", newID.String())

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("Failed to encode response", "error", err)
	}
}

// handleOIDCConfig handles GET requests to /.well-known/openid-configuration.
// Trust domain is determined from the Host header.
func (s *ExchangeServer) handleOIDCConfig(w http.ResponseWriter, r *http.Request) {
	trustDomain := extractTrustDomain(r.Host)
	if trustDomain == "" {
		http.Error(w, "Host header required", http.StatusBadRequest)
		return
	}

	_, _, alg, err := s.keyStore.GetOrCreateKey(trustDomain)
	if err != nil {
		slog.Error("Failed to get key for domain", "domain", trustDomain, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	issuer := "spiffe://" + trustDomain
	jwksURI := (&url.URL{
		Scheme: "https",
		Host:   r.Host,
		Path:   "/keys",
	}).String()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(oidc.ProviderConfig{
		IssuerURL:  issuer,
		JWKSURL:    jwksURI,
		Algorithms: []string{string(alg)},
	}); err != nil {
		slog.Error("Failed to encode OIDC config", "error", err)
	}
}

// handleJWKS handles GET requests to /keys.
// Trust domain is determined from the Host header.
func (s *ExchangeServer) handleJWKS(w http.ResponseWriter, r *http.Request) {
	trustDomain := extractTrustDomain(r.Host)
	if trustDomain == "" {
		http.Error(w, "Host header required", http.StatusBadRequest)
		return
	}

	signer, keyID, alg, err := s.keyStore.GetOrCreateKey(trustDomain)
	if err != nil {
		slog.Error("Failed to get key for domain", "domain", trustDomain, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	jwk := jose.JSONWebKey{
		Key:       signer.Public(),
		KeyID:     keyID.String(),
		Algorithm: string(alg),
		Use:       "sig",
	}

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jwks); err != nil {
		slog.Error("Failed to encode JWKS", "error", err)
	}
}

// extractTrustDomain extracts the trust domain from the Host header, stripping any port.
func extractTrustDomain(host string) string {
	if idx := strings.Index(host, ":"); idx != -1 {
		return host[:idx]
	}
	return host
}

func main() {
	var (
		listenAddr = flag.String("listen", ":8080", "HTTP server listen address")
		socketPath = flag.String("socket", "", "SPIFFE Workload API socket path (uses default if empty)")
		logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

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
		"log_level", level.String())

	ctx := context.Background()
	keyStore := NewInMemoryKeyStore(ES256KeyGenerator{})

	server, err := NewExchangeServer(ctx, keyStore, *socketPath)
	if err != nil {
		slog.Error("Failed to create exchange server", "error", err)
		os.Exit(1)
	}
	defer server.Close()

	http.HandleFunc("POST /v1/exchange", server.handleExchange)
	http.HandleFunc("GET /.well-known/openid-configuration", server.handleOIDCConfig)
	http.HandleFunc("GET /keys", server.handleJWKS)
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
