package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp/filters"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"dsx-identity/internal/dsxidentity"
	"dsx-identity/internal/filewatch"
	"dsx-identity/internal/keydistribution"
	"dsx-identity/internal/signingkey"
	"dsx-identity/internal/spireidentity"
	"dsx-identity/internal/telemetry"
	"dsx-identity/internal/watch"
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

type publicKeyRegistry interface {
	Keyset() jose.JSONWebKeySet
	Ready() bool
}

type signingSource interface {
	Get() *signingkey.Snapshot
}

type identityKeysServer struct {
	signingKeys signingSource
	registry    publicKeyRegistry
	jwtSource   *workloadapi.JWTSource
	jwksURL     string
	issuer      string
	region      string
	partition   string
}

func newIdentityKeysServer(ctx context.Context, issuer, region, partition string, signingKeys signingSource, registry publicKeyRegistry, socketPath, jwksURL string) (*identityKeysServer, error) {
	if err := dsxidentity.ValidateIssuer(issuer); err != nil {
		return nil, err
	}
	if err := dsxidentity.ValidateDomain(region, partition); err != nil {
		return nil, err
	}
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
	return &identityKeysServer{
		signingKeys: signingKeys, registry: registry, jwtSource: jwts, jwksURL: jwksURL,
		issuer: issuer, region: region, partition: partition,
	}, nil
}

func (s *identityKeysServer) Close() error {
	return s.jwtSource.Close()
}

func (s *identityKeysServer) resignJWT(originalClaims map[string]any, newSubject spiffeid.ID) (string, error) {
	// Preserve only registered JWT-SVID claims across the shard boundary.
	newClaims := make(map[string]any, 7)
	for _, name := range []string{"aud", "exp", "iat", "jti", "nbf"} {
		if value, ok := originalClaims[name]; ok {
			newClaims[name] = value
		}
	}
	newClaims["sub"] = newSubject.String()
	newClaims["iss"] = s.issuer

	signer := s.signingKeys.Get().Signer()
	token, err := jwt.Signed(signer).Claims(newClaims).Serialize()
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
	newID, err := exchangeSubject(s.region, s.partition, validatedSVID.ID)
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

func exchangeSubject(region, partition string, id spiffeid.ID) (spiffeid.ID, error) {
	local, err := spireidentity.Parse(id)
	if err != nil {
		return spiffeid.ID{}, err
	}

	return dsxidentity.ID(local, region, partition)
}

func (s *identityKeysServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	responseBody, err := json.Marshal(s.registry.Keyset())
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
		IssuerURL:  s.issuer,
		JWKSURL:    s.jwksURL,
		Algorithms: keydistribution.SigningAlgorithms(s.registry.Keyset()),
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
	if !s.registry.Ready() {
		http.Error(w, "public key registry is not ready", http.StatusServiceUnavailable)
		return
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
		listenAddr            = flag.String("listen", ":8080", "HTTP server listen address")
		socketPath            = flag.String("socket", "", "SPIFFE Workload API socket path")
		jwksURL               = flag.String("jwks-url", "", "public JWKS URL advertised through discovery")
		issuer                = flag.String("issuer", "", "HTTPS issuer for exchanged DSX JWTs")
		region                = flag.String("region", "", "region encoded in exchanged DSX identities")
		partition             = flag.String("partition", "", "partition encoded in exchanged DSX identities")
		signingKeyset         = flag.String("signing-keyset", "/run/secrets/dsx-identity-keys/keyset.json", "private signing keyset file")
		fileWatchPollInterval = flag.Duration("file-watch-poll-interval", 30*time.Second, "file watcher polling interval")
		activeKID             = flag.String("active-kid", "/run/secrets/dsx-identity-keys-active-kid/active-kid.json", "JSON file containing the active signing kid and algorithm")
		shardID               = flag.String("shard-id", "", "stable DSX Identity Keys shard identifier")
		disableNATS           = flag.Bool("disable-nats", false, "disable NATS public key synchronization")
		natsServer            = flag.String("nats-server", "", "NATS server URL")
		natsClientName        = flag.String("nats-client-name", "", "NATS client name (defaults to dsx-identity-keys-<shard-id>)")
		publicKeyTTL          = flag.Duration("public-key-ttl", 10*time.Minute, "public key NATS KV entry TTL")
		natsKVBucket          = flag.String("nats-kv-bucket", keydistribution.DefaultBucket, "NATS KV bucket for public keys")
		natsKVReplicas        = flag.Int("nats-kv-replicas", 1, "NATS KV stream replica count")
		natsAuthMode          = flag.String("nats-auth-mode", keydistribution.AuthNone, "NATS authentication mode: none or oauth2")
		natsCredentials       = flag.String("nats-credentials-file", "", "JSON file containing OAuth2 client credentials")
		natsIssuerURL         = flag.String("nats-oauth-issuer-url", "", "OAuth2 issuer for NATS token authentication")
		natsScopes            = flag.String("nats-oauth-scopes", "nats", "comma-separated OAuth2 scopes for NATS; empty requests no scopes")
	)
	flag.Parse()
	if *fileWatchPollInterval <= 0 {
		return fmt.Errorf("file watcher polling interval must be positive")
	}
	if *issuer == "" {
		return fmt.Errorf("issuer is required")
	}
	if err := dsxidentity.ValidateDomain(*region, *partition); err != nil {
		return err
	}

	sdk, err := telemetry.Setup(context.Background(), "dsx-identity-keys", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()

	signingReloader, err := signingkey.NewReloader(*signingKeyset, *activeKID)
	if err != nil {
		return fmt.Errorf("load signing keyset: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	signingKeys := signingReloader.Source()
	registry := keydistribution.NewRegistry(signingKeys)
	var publicRegistry publicKeyRegistry = registry
	if *disableNATS {
		defer registry.Close()
	} else {
		if *natsClientName == "" {
			*natsClientName = "dsx-identity-keys-" + *shardID
		}
		var scopes []string
		if *natsScopes != "" {
			scopes = strings.Split(*natsScopes, ",")
		}
		natsAuth := keydistribution.AuthConfig{
			Mode:            *natsAuthMode,
			CredentialsFile: *natsCredentials,
			OAuthIssuerURL:  *natsIssuerURL,
			OAuthScopes:     scopes,
		}
		synchronizer, err := keydistribution.NewSynchronizer(ctx, *natsServer, *natsClientName, *shardID, signingKeys, *publicKeyTTL, keydistribution.KVConfig{
			Bucket: *natsKVBucket, Replicas: *natsKVReplicas,
		}, registry, natsAuth)
		if err != nil {
			registry.Close()
			return fmt.Errorf("start public key synchronization: %w", err)
		}
		registryWithSync := &keydistribution.SynchronizedRegistry{Registry: registry, Synchronizer: synchronizer}
		defer registryWithSync.Close()
		publicRegistry = registryWithSync
	}

	server, err := newIdentityKeysServer(ctx, *issuer, *region, *partition, signingKeys, publicRegistry, *socketPath, *jwksURL)
	if err != nil {
		return fmt.Errorf("create DSX Identity Keys server: %w", err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close JWT source: %w", err))
		}
	}()
	httpServer := &http.Server{
		Addr: *listenAddr,
		Handler: otelhttp.NewHandler(server.handler(), "dsx-identity-keys",
			otelhttp.WithFilter(filters.None(filters.Path("/live"), filters.Path("/ready")))),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("Starting DSX Identity Keys",
		"listen_addr", *listenAddr,
		"issuer", *issuer,
		"shard_id", *shardID,
		"active_kid", signingKeys.Get().KeyID(),
		"nats_enabled", !*disableNATS,
	)
	group, runContext := errgroup.WithContext(ctx)
	watchers := make([]watch.Watcher[filewatch.Change], 0, 2)
	for _, path := range []string{*signingKeyset, *activeKID} {
		watchers = append(watchers, filewatch.Contents(path,
			watch.Composite(filewatch.FSNotify(path), watch.Polling(*fileWatchPollInterval))))
	}
	group.Go(func() error { return signingReloader.Run(runContext, watch.Composite(watchers...)) })
	group.Go(func() error {
		return serveHTTP(runContext, httpServer)
	})
	if err := group.Wait(); err != nil {
		return fmt.Errorf("run DSX Identity Keys: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("DSX Identity Keys failed", "error", err)
		os.Exit(1)
	}
}
