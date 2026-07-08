package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"golang.org/x/sync/errgroup"

	"dsx-identity/internal/identity"
	"dsx-identity/internal/stricturl"
)

const (
	GrantTypeTokenExchange    = "urn:ietf:params:oauth:grant-type:token-exchange"
	TokenTypeJWT              = "urn:ietf:params:oauth:token-type:jwt"
	ContentTypeFormURLEncoded = "application/x-www-form-urlencoded"
)

type TokenExchangeResponse struct {
	AccessToken     string `json:"access_token"`
	IssuedTokenType string `json:"issued_token_type"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
}

type TokenExchangeErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

type ExchangeServer struct {
	keys      *signingKeyset
	registry  *keyRegistry
	jwtSource *workloadapi.JWTSource
	jwksURL   string
	keySync   *keySync
}

func NewExchangeServer(ctx context.Context, keys *signingKeyset, registry *keyRegistry, socketPath, jwksURL string) (*ExchangeServer, error) {
	opts := []workloadapi.JWTSourceOption{}
	if socketPath != "" {
		opts = append(opts, workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	}
	jwts, err := workloadapi.NewJWTSource(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create JWT source: %w", err)
	}
	return &ExchangeServer{keys: keys, registry: registry, jwtSource: jwts, jwksURL: jwksURL}, nil
}

func (s *ExchangeServer) resignJWT(originalClaims map[string]any, newSubject spiffeid.ID) (string, error) {
	// Preserve every validated claim except sub and iss.
	newClaims := make(map[string]any, len(originalClaims))
	maps.Copy(newClaims, originalClaims)
	newClaims["sub"] = newSubject.String()
	newClaims["iss"] = identity.Issuer

	token, err := jwt.Signed(s.keys.signer).Claims(newClaims).Serialize()
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}
	return token, nil
}

func writeTokenError(w http.ResponseWriter, statusCode int, errorCode, description string) {
	responseBody, err := json.Marshal(TokenExchangeErrorResponse{Error: errorCode, ErrorDescription: description})
	if err != nil {
		slog.Error("Failed to marshal error response", "error", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	w.WriteHeader(statusCode)
	if _, err := w.Write(responseBody); err != nil {
		slog.Error("Failed to write error response", "error", err)
	}
}

func (s *ExchangeServer) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	slog.Info("Received token exchange request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != ContentTypeFormURLEncoded {
		writeTokenError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/x-www-form-urlencoded")
		return
	}
	if r.URL.RawQuery != "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "Query parameters are not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "Failed to parse form data")
		return
	}
	if grantType := r.PostForm.Get("grant_type"); grantType != GrantTypeTokenExchange {
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant_type must be %s", GrantTypeTokenExchange))
		return
	}
	subjectToken := r.PostForm.Get("subject_token")
	if subjectToken == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "subject_token is required")
		return
	}
	if subjectTokenType := r.PostForm.Get("subject_token_type"); subjectTokenType != TokenTypeJWT {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("subject_token_type must be %s", TokenTypeJWT))
		return
	}

	// Classify malformed claims and unsupported algorithms as an RFC 8693
	// request error before the Workload API performs issuer-local verification.
	parsedJWT, err := jwt.ParseSigned(subjectToken, []jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512,
		jose.ES256, jose.ES384, jose.ES512,
	})
	if err == nil {
		var claims jwt.Claims
		err = parsedJWT.UnsafeClaimsWithoutVerification(&claims)
	}
	if err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("Invalid subject_token: %v", err))
		return
	}
	validatedSVID, err := jwtsvid.ParseAndValidate(subjectToken, s.jwtSource, nil)
	if err != nil {
		writeTokenError(w, http.StatusUnauthorized, "invalid_grant", fmt.Sprintf("JWT validation failed: %v", err))
		return
	}
	newID, err := identity.ExchangeSPIFFEID(validatedSVID.ID)
	if err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("Failed to transform SPIFFE ID: %v", err))
		return
	}
	token, err := s.resignJWT(validatedSVID.Claims, newID)
	if err != nil {
		slog.Error("Failed to sign JWT", "error", err)
		writeTokenError(w, http.StatusInternalServerError, "server_error", "Failed to sign JWT")
		return
	}

	responseBody, err := json.Marshal(TokenExchangeResponse{
		AccessToken:     token,
		IssuedTokenType: TokenTypeJWT,
		TokenType:       "Bearer",
		ExpiresIn:       max(int64(time.Until(validatedSVID.Expiry).Seconds()), 0),
	})
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error", "Failed to encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, err := w.Write(responseBody); err != nil {
		slog.Error("Failed to write response", "error", err)
	}
}

func (s *ExchangeServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	responseBody, err := json.Marshal(s.registry.keyset())
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, err := w.Write(responseBody); err != nil {
		slog.Error("Failed to write JWKS response", "error", err)
	}
}

func (s *ExchangeServer) handleOIDCDiscovery(w http.ResponseWriter, _ *http.Request) {
	responseBody, err := json.Marshal(oidc.ProviderConfig{
		IssuerURL:  identity.Issuer,
		JWKSURL:    s.jwksURL,
		Algorithms: signingAlgorithms(s.registry.keyset()),
	})
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, err := w.Write(responseBody); err != nil {
		slog.Error("Failed to write OIDC discovery response", "error", err)
	}
}

func (s *ExchangeServer) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.keySync != nil {
		if !s.keySync.isConnectedAndSubscribed() {
			http.Error(w, "MQTT is not connected and subscribed", http.StatusServiceUnavailable)
			return
		}
		if !s.keySync.isCacheReady() {
			http.Error(w, "public key cache is not ready", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func handleLive(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *ExchangeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/exchange", s.handleTokenExchange)
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleOIDCDiscovery)
	mux.HandleFunc("GET /live", handleLive)
	mux.HandleFunc("GET /ready", s.handleReady)
	return mux
}

func serveHTTP(ctx context.Context, server *http.Server) error {
	// Bind first so cancellation cannot race startup and leave an unowned server.
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	serveContext, cancel := context.WithCancel(ctx)
	defer cancel()
	group := new(errgroup.Group)
	group.Go(func() error {
		defer cancel()
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	group.Go(func() error {
		<-serveContext.Done()
		if ctx.Err() == nil {
			return nil
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return errors.Join(fmt.Errorf("shut down HTTP server: %w", err), server.Close())
		}
		return nil
	})
	return group.Wait()
}

func run() (runErr error) {
	var level slog.Level
	flag.TextVar(&level, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	var (
		listenAddr        = flag.String("listen", ":8080", "HTTP server listen address")
		socketPath        = flag.String("socket", "", "SPIFFE Workload API socket path")
		jwksURL           = flag.String("jwks-url", "", "public JWKS URL advertised through discovery")
		signingJWKS       = flag.String("signing-jwks", "/run/secrets/spiffe-exchange/signing-jwks.json", "private signing JWKS file")
		activeKID         = flag.String("active-kid", "/run/secrets/spiffe-exchange/active-kid", "file containing the active signing kid")
		shardID           = flag.String("shard-id", "", "stable Exchange shard identifier")
		mqttBroker        = flag.String("mqtt-broker", "", "MQTT 3.1.1 broker URL")
		mqttClientID      = flag.String("mqtt-client-id", "", "MQTT client identifier (defaults to spiffe-exchange-<shard-id>)")
		publicKeyLeaseTTL = flag.Duration("public-key-lease-ttl", 10*time.Minute, "public key publication lease lifetime")
		mqttTokenURL      = flag.String("mqtt-oauth-token-url", "", "OAuth2 client-credentials token endpoint for MQTT")
		mqttOAuthID       = flag.String("mqtt-oauth-client-id", "", "OAuth2 client ID for MQTT")
		mqttSecret        = flag.String("mqtt-oauth-client-secret", "", "file containing the OAuth2 client secret for MQTT")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	parsedJWKSURL, err := stricturl.ParseHTTP(*jwksURL)
	if err != nil {
		return fmt.Errorf("--jwks-url %w", err)
	}
	if *mqttClientID == "" {
		*mqttClientID = "spiffe-exchange-" + *shardID
	}

	keys, err := loadSigningKeyset(*signingJWKS, *activeKID)
	if err != nil {
		return fmt.Errorf("load signing keyset: %w", err)
	}
	if *publicKeyLeaseTTL < 2*time.Second {
		return fmt.Errorf("--public-key-lease-ttl must be at least 2s: %s", publicKeyLeaseTTL.String())
	}
	registry := newKeyRegistry()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mqttOAuth := mqttOAuthConfig{tokenURL: *mqttTokenURL, clientID: *mqttOAuthID, clientSecretFile: *mqttSecret}
	syncer, err := startKeySync(ctx, *mqttBroker, *mqttClientID, *shardID, keys.public, *publicKeyLeaseTTL, registry, mqttOAuth)
	if err != nil {
		return fmt.Errorf("start public key synchronization: %w", err)
	}
	go registry.byKID.Start()
	defer registry.byKID.Stop()
	defer syncer.Close()

	server, err := NewExchangeServer(ctx, keys, registry, *socketPath, parsedJWKSURL.String())
	if err != nil {
		return fmt.Errorf("create exchange server: %w", err)
	}
	defer func() {
		if err := server.jwtSource.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close JWT source: %w", err))
		}
	}()
	server.keySync = syncer

	httpServer := &http.Server{
		Addr:              *listenAddr,
		Handler:           server.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("Starting SPIFFE Exchange Service", "listen_addr", *listenAddr, "issuer", identity.Issuer, "shard_id", *shardID, "active_kid", keys.activeKID)
	if err := serveHTTP(ctx, httpServer); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("SPIFFE Exchange Service failed", "error", err)
		os.Exit(1)
	}
}
