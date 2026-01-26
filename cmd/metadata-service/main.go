package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"golang.org/x/oauth2"
)

// RFC 8693 OAuth 2.0 Token Exchange constants.
const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeJWT           = "urn:ietf:params:oauth:token-type:jwt"
)

const (
	defaultSpireSocketPath = "/var/run/secrets/spire.io/spire-agent.sock"
)

type IdentityRequest struct {
	Audience string `json:"audience"`
}

type IdentityResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	SpiffeID  string    `json:"spiffe_id"`
}

type MetadataServer struct {
	exchangeSocket  string
	spireSocketPath string
	httpClient      *http.Client
}

// extractSubjectFromJWT extracts the subject claim from a JWT without validation.
func extractSubjectFromJWT(token string) (string, error) {
	parsedJWT, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512,
		jose.ES256, jose.ES384, jose.ES512,
	})
	if err != nil {
		return "", fmt.Errorf("failed to parse JWT: %w", err)
	}

	var claims jwt.Claims
	if err := parsedJWT.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return "", fmt.Errorf("failed to extract claims: %w", err)
	}

	return claims.Subject, nil
}

// fetchJWT fetches a JWT SVID from the local SPIRE workload API
func (s *MetadataServer) fetchJWT(ctx context.Context, audience string) (*jwtsvid.SVID, error) {
	socketAddr := fmt.Sprintf("unix://%s", s.spireSocketPath)

	slog.Debug("Creating JWT source",
		"socket_path", s.spireSocketPath,
		"socket_addr", socketAddr,
		"audience", audience)

	jwtSource, err := workloadapi.NewJWTSource(ctx, workloadapi.WithClientOptions(
		workloadapi.WithAddr(socketAddr),
	))
	if err != nil {
		slog.Error("Failed to create JWT source",
			"socket_path", s.spireSocketPath,
			"error", err)
		return nil, fmt.Errorf("failed to create JWT source: %w", err)
	}
	defer jwtSource.Close()

	slog.Debug("Fetching JWT SVID", "audience", audience)

	jwt, err := jwtSource.FetchJWTSVID(ctx, jwtsvid.Params{
		Audience: audience,
	})
	if err != nil {
		slog.Error("Failed to fetch JWT SVID",
			"audience", audience,
			"error", err)
		return nil, fmt.Errorf("failed to fetch JWT: %w", err)
	}

	slog.Info("Successfully fetched JWT from SPIRE",
		"spiffe_id", jwt.ID.String(),
		"audience", audience,
		"expiry", jwt.Expiry)

	return jwt, nil
}

// exchangeJWT exchanges a JWT for a new one via the exchange service (over Unix socket)
// using RFC 8693 OAuth 2.0 Token Exchange format.
func (s *MetadataServer) exchangeJWT(ctx context.Context, token string) (*oauth2.Token, error) {
	slog.Debug("Creating token exchange request",
		"exchange_socket", s.exchangeSocket,
		"token_length", len(token))

	slog.Info("Sending token exchange request", "exchange_socket", s.exchangeSocket)

	// HTTP over Unix socket uses a dummy host (the socket handles routing)
	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			TokenURL: "http://exchange/v1/exchange",
		},
	}

	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)

	resp, err := conf.Exchange(ctx, "",
		oauth2.SetAuthURLParam("grant_type", grantTypeTokenExchange),
		oauth2.SetAuthURLParam("subject_token", token),
		oauth2.SetAuthURLParam("subject_token_type", tokenTypeJWT),
	)
	if err != nil {
		slog.Error("Failed to exchange JWT",
			"exchange_socket", s.exchangeSocket,
			"error", err)
		return nil, err
	}

	// Extract the new SPIFFE ID from the JWT's subject claim
	newSpiffeID, _ := extractSubjectFromJWT(resp.AccessToken)

	slog.Info("Successfully received exchange response",
		"new_spiffe_id", newSpiffeID,
		"expires_at", resp.Expiry)

	return resp, nil
}

// handleIdentity handles POST requests to /{latest|2009-04-04}/meta-data/identity
func (s *MetadataServer) handleIdentity(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 1024*1024)
	defer body.Close()

	slog.Info("Received identity request",
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
		"content_type", r.Header.Get("Content-Type"))

	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		slog.Warn("Invalid content type",
			"content_type", r.Header.Get("Content-Type"),
			"remote_addr", r.RemoteAddr)
		http.Error(w, "Invalid content type", http.StatusUnsupportedMediaType)
		return
	}

	var req IdentityRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		slog.Warn("Failed to decode request body",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	slog.Debug("Parsed identity request",
		"audience", req.Audience,
		"remote_addr", r.RemoteAddr)

	if req.Audience == "" {
		slog.Warn("Missing audience in request", "remote_addr", r.RemoteAddr)
		http.Error(w, "Audience is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Fetch JWT from local SPIRE socket
	slog.Info("Fetching JWT from local SPIRE socket",
		"audience", req.Audience,
		"socket_path", s.spireSocketPath)

	spiffeJWT, err := s.fetchJWT(ctx, req.Audience)
	if err != nil {
		slog.Error("Failed to fetch JWT from SPIRE",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to fetch JWT: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("Fetched JWT, now exchanging",
		"original_spiffe_id", spiffeJWT.ID.String(),
		"audience", req.Audience)

	// Exchange JWT via the exchange service (RFC 8693)
	exchangeResp, err := s.exchangeJWT(ctx, spiffeJWT.Marshal())
	if err != nil {
		slog.Error("Failed to exchange JWT",
			"original_spiffe_id", spiffeJWT.ID.String(),
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to exchange JWT: %v", err), http.StatusInternalServerError)
		return
	}

	// Extract the new SPIFFE ID from the exchanged JWT's subject claim
	newSpiffeID, err := extractSubjectFromJWT(exchangeResp.AccessToken)
	if err != nil {
		slog.Error("Failed to extract SPIFFE ID from exchanged JWT",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "Failed to process exchanged JWT", http.StatusInternalServerError)
		return
	}

	resp := IdentityResponse{
		Token:     exchangeResp.AccessToken,
		ExpiresAt: exchangeResp.Expiry,
		SpiffeID:  newSpiffeID,
	}

	slog.Info("Successfully exchanged JWT",
		"original_spiffe_id", spiffeJWT.ID.String(),
		"new_spiffe_id", newSpiffeID,
		"remote_addr", r.RemoteAddr)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("Failed to encode response",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	slog.Debug("Response sent successfully", "remote_addr", r.RemoteAddr)
}

func main() {
	var (
		listenAddr      = flag.String("listen", ":80", "HTTP server listen address")
		logLevel        = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
		exchangeSocket  = flag.String("exchange-socket", "", "Path to exchange service Unix socket")
		spireSocketPath = flag.String("spire-socket", defaultSpireSocketPath, "Path to SPIRE agent socket")
	)
	flag.Parse()

	if *exchangeSocket == "" {
		fmt.Fprintln(os.Stderr, "error: --exchange-socket is required")
		os.Exit(1)
	}

	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
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

	slog.Info("Starting SPIFFE Metadata Service",
		"listen_addr", *listenAddr,
		"log_level", level.String(),
		"exchange_socket", *exchangeSocket,
		"spire_socket", *spireSocketPath)

	// Create HTTP client that connects via Unix socket
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", *exchangeSocket)
			},
		},
	}

	server := &MetadataServer{
		exchangeSocket:  *exchangeSocket,
		spireSocketPath: *spireSocketPath,
		httpClient:      httpClient,
	}

	// Handle both latest and 2009-04-04 API versions for EC2-style metadata service
	http.HandleFunc("POST /latest/meta-data/identity", server.handleIdentity)
	http.HandleFunc("POST /2009-04-04/meta-data/identity", server.handleIdentity)
	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("Health check", "remote_addr", r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	slog.Info("Starting HTTP server", "listen_addr", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, nil); err != nil {
		slog.Error("HTTP server failed", "error", err)
		panic(err)
	}
}
