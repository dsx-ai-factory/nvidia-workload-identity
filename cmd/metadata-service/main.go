package main

import (
	"bytes"
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

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
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

type ExchangeRequest struct {
	JWT string `json:"jwt"`
}

type ExchangeResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	SpiffeID  string    `json:"spiffe_id"`
}

type MetadataServer struct {
	exchangeSocket  string
	spireSocketPath string
	httpClient      *http.Client
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
func (s *MetadataServer) exchangeJWT(ctx context.Context, token string) (*ExchangeResponse, error) {
	reqBody := ExchangeRequest{JWT: token}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("Failed to marshal exchange request", "error", err)
		return nil, fmt.Errorf("failed to marshal exchange request: %w", err)
	}

	// HTTP over Unix socket uses a dummy host (the socket handles routing)
	exchangeURL := "http://exchange/v1/exchange"
	slog.Debug("Creating exchange request",
		"exchange_socket", s.exchangeSocket,
		"token_length", len(token))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, bytes.NewReader(reqBytes))
	if err != nil {
		slog.Error("Failed to create exchange request", "error", err)
		return nil, fmt.Errorf("failed to create exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Info("Sending exchange request", "exchange_socket", s.exchangeSocket)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		slog.Error("Failed to call exchange service",
			"exchange_socket", s.exchangeSocket,
			"error", err)
		return nil, fmt.Errorf("failed to call exchange service: %w", err)
	}
	defer resp.Body.Close()

	slog.Debug("Received exchange response",
		"status_code", resp.StatusCode,
		"status", resp.Status)

	if resp.StatusCode != http.StatusOK {
		slog.Error("Exchange service returned non-OK status",
			"status_code", resp.StatusCode,
			"status", resp.Status)
		return nil, fmt.Errorf("exchange service returned status %d", resp.StatusCode)
	}

	var exchangeResp ExchangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&exchangeResp); err != nil {
		slog.Error("Failed to decode exchange response", "error", err)
		return nil, fmt.Errorf("failed to decode exchange response: %w", err)
	}

	slog.Info("Successfully received exchange response",
		"new_spiffe_id", exchangeResp.SpiffeID,
		"expires_at", exchangeResp.ExpiresAt)

	return &exchangeResp, nil
}

// handleIdentity handles POST requests to /v1/metadata/identity
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

	jwt, err := s.fetchJWT(ctx, req.Audience)
	if err != nil {
		slog.Error("Failed to fetch JWT from SPIRE",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to fetch JWT: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("Fetched JWT, now exchanging",
		"original_spiffe_id", jwt.ID.String(),
		"audience", req.Audience)

	// Exchange JWT via the exchange service
	exchangeResp, err := s.exchangeJWT(ctx, jwt.Marshal())
	if err != nil {
		slog.Error("Failed to exchange JWT",
			"original_spiffe_id", jwt.ID.String(),
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to exchange JWT: %v", err), http.StatusInternalServerError)
		return
	}

	resp := IdentityResponse{
		Token:     exchangeResp.Token,
		ExpiresAt: exchangeResp.ExpiresAt,
		SpiffeID:  exchangeResp.SpiffeID,
	}

	slog.Info("Successfully exchanged JWT",
		"original_spiffe_id", jwt.ID.String(),
		"new_spiffe_id", exchangeResp.SpiffeID,
		"expires_at", exchangeResp.ExpiresAt,
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

	http.HandleFunc("POST /v1/metadata/identity", server.handleIdentity)
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
