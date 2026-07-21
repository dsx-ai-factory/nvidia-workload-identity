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
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp/filters"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"dsx-identity/internal/identity"
	"dsx-identity/internal/telemetry"
)

const (
	GrantTypeTokenExchange    = "urn:ietf:params:oauth:grant-type:token-exchange"
	TokenTypeJWT              = "urn:ietf:params:oauth:token-type:jwt"
	ContentTypeFormURLEncoded = "application/x-www-form-urlencoded"
	maxExchangeRequestBytes   = 1 << 20
	discoveryCacheControl     = "public, max-age=60"
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

type identityKeysServer struct {
	keys      *signingKeyset
	registry  *keyRegistry
	jwtSource *workloadapi.JWTSource
	jwksURL   string
	keySync   *keySync
}

func newIdentityKeysServer(ctx context.Context, keys *signingKeyset, registry *keyRegistry, socketPath, jwksURL string) (*identityKeysServer, error) {
	clientOptions := []workloadapi.ClientOption{
		workloadapi.WithDialOptions(grpc.WithStatsHandler(otelgrpc.NewClientHandler())),
	}
	if socketPath != "" {
		clientOptions = append(clientOptions, workloadapi.WithAddr(socketPath))
	}
	jwts, err := workloadapi.NewJWTSource(ctx, workloadapi.WithClientOptions(clientOptions...))
	if err != nil {
		return nil, fmt.Errorf("create JWT source: %w", err)
	}
	return &identityKeysServer{keys: keys, registry: registry, jwtSource: jwts, jwksURL: jwksURL}, nil
}

func (s *identityKeysServer) Close() error {
	return s.jwtSource.Close()
}

func (s *identityKeysServer) resignJWT(originalClaims map[string]any, newSubject spiffeid.ID) (string, error) {
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

func (s *identityKeysServer) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	slog.InfoContext(r.Context(), "Received token exchange request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != ContentTypeFormURLEncoded {
		writeTokenError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/x-www-form-urlencoded")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxExchangeRequestBytes)
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
	if requestedTokenType := r.PostForm.Get("requested_token_type"); requestedTokenType != "" && requestedTokenType != TokenTypeJWT {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("requested_token_type must be %s", TokenTypeJWT))
		return
	}

	validatedSVID, err := jwtsvid.ParseAndValidate(subjectToken, s.jwtSource, nil)
	if err != nil {
		slog.WarnContext(r.Context(), "Rejected token exchange", "reason", "local_svid_verification", "error", err)
		writeTokenError(w, http.StatusUnauthorized, "invalid_grant", "subject_token validation failed")
		return
	}
	newID, err := identity.ToDSXSPIFFEID(validatedSVID.ID)
	if err != nil {
		slog.WarnContext(r.Context(), "Rejected token exchange", "reason", "unsupported_subject", "error", err)
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "subject_token subject is not supported")
		return
	}
	token, err := s.resignJWT(validatedSVID.Claims, newID)
	if err != nil {
		slog.ErrorContext(r.Context(), "Failed to sign JWT", "error", err)
		writeTokenError(w, http.StatusInternalServerError, "server_error", "Failed to sign JWT")
		return
	}
	slog.InfoContext(r.Context(), "Accepted token exchange", "decision", "allow", "subject", newID.String(), "audience_count", len(validatedSVID.Audience))

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
		slog.ErrorContext(r.Context(), "Failed to write response", "error", err)
	}
}

func (s *identityKeysServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	responseBody, err := json.Marshal(s.registry.keyset())
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", discoveryCacheControl)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, err := w.Write(responseBody); err != nil {
		slog.Error("Failed to write JWKS response", "error", err)
	}
}

func (s *identityKeysServer) handleOIDCDiscovery(w http.ResponseWriter, _ *http.Request) {
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
	w.Header().Set("Cache-Control", discoveryCacheControl)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, err := w.Write(responseBody); err != nil {
		slog.Error("Failed to write OIDC discovery response", "error", err)
	}
}

func (s *identityKeysServer) handleReady(w http.ResponseWriter, _ *http.Request) {
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

func (s *identityKeysServer) handler() http.Handler {
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
	var logLevel slog.Level
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	var (
		listenAddr        = flag.String("listen", ":8080", "HTTP server listen address")
		socketPath        = flag.String("socket", "", "SPIFFE Workload API socket path")
		jwksURL           = flag.String("jwks-url", "", "public JWKS URL advertised through discovery")
		signingKeyset     = flag.String("signing-keyset", "/run/secrets/dsx-identity-keys/keyset.json", "private signing keyset file")
		activeKID         = flag.String("active-kid", "/run/secrets/dsx-identity-keys-active-kid/active-kid.json", "JSON file containing the active signing kid and algorithm")
		shardID           = flag.String("shard-id", "", "stable DSX Identity Keys shard identifier")
		mqttBroker        = flag.String("mqtt-broker", "", "MQTT 3.1.1 broker URL")
		mqttClientID      = flag.String("mqtt-client-id", "", "MQTT client identifier (defaults to dsx-identity-keys-<shard-id>)")
		publicKeyLeaseTTL = flag.Duration("public-key-lease-ttl", 10*time.Minute, "public key publication lease lifetime")
		mqttAuthModeFlag  = flag.String("mqtt-auth-mode", "anonymous", "MQTT authentication mode: oauth2, basic, or anonymous")
		mqttCredentials   = flag.String("mqtt-credentials-file", "", "JSON file containing MQTT credentials")
		mqttTokenURL      = flag.String("mqtt-oauth-token-url", "", "OAuth2 client-credentials token endpoint for MQTT")
		mqttScopes        = flag.String("mqtt-oauth-scopes", "mqtt", "comma-separated OAuth2 scopes for MQTT; empty requests no scopes")
		mqttOAuthUsername = flag.String("mqtt-oauth-username", "oauthtoken", "MQTT username sent with the OAuth2 access token")
	)
	flag.Parse()

	sdk, err := telemetry.Setup(context.Background(), "dsx-identity-keys", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()

	if *mqttClientID == "" {
		*mqttClientID = "dsx-identity-keys-" + *shardID
	}

	keys, err := loadSigningKeyset(*signingKeyset, *activeKID)
	if err != nil {
		return fmt.Errorf("load signing keyset: %w", err)
	}
	registry := newKeyRegistry()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var scopes []string
	if *mqttScopes != "" {
		scopes = strings.Split(*mqttScopes, ",")
	}
	mqttAuth := mqttAuthConfig{
		mode:            *mqttAuthModeFlag,
		credentialsFile: *mqttCredentials,
		oauthTokenURL:   *mqttTokenURL,
		oauthScopes:     scopes,
		oauthUsername:   *mqttOAuthUsername,
	}
	syncer, err := startKeySync(ctx, *mqttBroker, *mqttClientID, *shardID, keys.public, *publicKeyLeaseTTL, registry, mqttAuth)
	if err != nil {
		return fmt.Errorf("start public key synchronization: %w", err)
	}
	registry.start()
	defer registry.stop()
	defer syncer.Close()

	server, err := newIdentityKeysServer(ctx, keys, registry, *socketPath, *jwksURL)
	if err != nil {
		return fmt.Errorf("create DSX Identity Keys server: %w", err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close JWT source: %w", err))
		}
	}()
	server.keySync = syncer

	httpServer := &http.Server{
		Addr: *listenAddr,
		Handler: otelhttp.NewHandler(server.handler(), "dsx-identity-keys",
			otelhttp.WithFilter(filters.None(filters.Path("/live"), filters.Path("/ready")))),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("Starting DSX Identity Keys", "listen_addr", *listenAddr, "issuer", identity.Issuer, "shard_id", *shardID, "active_kid", keys.activeKID)
	if err := serveHTTP(ctx, httpServer); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("DSX Identity Keys failed", "error", err)
		os.Exit(1)
	}
}
