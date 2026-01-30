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
	"strings"
	"syscall"
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

// ttlListener wraps a net.Listener and sets IP TTL=1 on accepted connections.
// This ensures response packets cannot traverse more than one network hop.
type ttlListener struct {
	net.Listener
}

func (l *ttlListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if rawConn, err := tcpConn.SyscallConn(); err == nil {
			rawConn.Control(func(fd uintptr) {
				// Set IP_TTL for IPv4
				if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, 1); err != nil {
					slog.Debug("Failed to set IP_TTL", "error", err)
				}
				// Set IPV6_UNICAST_HOPS for IPv6
				if err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, 1); err != nil {
					slog.Debug("Failed to set IPV6_UNICAST_HOPS", "error", err)
				}
			})
		}
	}

	return conn, nil
}

type IdentityResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"` // Seconds until expiration (OAuth2 RFC 6749)
	SpiffeID  string `json:"spiffe_id"`
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
func (s *MetadataServer) fetchJWT(ctx context.Context, audiences []string) (*jwtsvid.SVID, error) {
	socketAddr := fmt.Sprintf("unix://%s", s.spireSocketPath)

	slog.Debug("Creating JWT source",
		"socket_path", s.spireSocketPath,
		"socket_addr", socketAddr,
		"audiences", audiences)

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

	slog.Debug("Fetching JWT SVID", "audiences", audiences)

	var extraAudiences []string
	if len(audiences) > 1 {
		extraAudiences = audiences[1:]
	}

	jwt, err := jwtSource.FetchJWTSVID(ctx, jwtsvid.Params{
		Audience:       audiences[0],
		ExtraAudiences: extraAudiences,
	})
	if err != nil {
		slog.Error("Failed to fetch JWT SVID",
			"audiences", audiences,
			"error", err)
		return nil, fmt.Errorf("failed to fetch JWT: %w", err)
	}

	slog.Info("Successfully fetched JWT from SPIRE",
		"spiffe_id", jwt.ID.String(),
		"audiences", audiences,
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
		"expiry", resp.Expiry)

	return resp, nil
}

// determineResponseFormat parses the Accept header and returns the appropriate content type.
// Defaults to "application/json" if no Accept header is provided or no supported type is found.
func determineResponseFormat(acceptHeader string) string {
	// Parse Accept header - it may be a comma-separated list with quality values
	// e.g., "text/plain, application/json" or "text/plain;q=0.9, application/json"
	acceptTypes := strings.SplitSeq(acceptHeader, ",")
	for acceptType := range acceptTypes {
		mediaType, _, err := mime.ParseMediaType(acceptType)
		if err != nil {
			// Skip invalid media types
			continue
		}

		if mediaType == "text/plain" {
			return "text/plain"
		}
		if mediaType == "application/json" {
			return "application/json"
		}
	}
	// Default to JSON if no supported type found or only JSON was found
	return "application/json"
}

// isForwardedRequest returns true if the request contains forwarding headers,
// indicating it came through a proxy rather than directly from a client.
func isForwardedRequest(h http.Header) bool {
	return h.Get("Forwarded") != "" ||
		h.Get("X-Forwarded-For") != "" ||
		h.Get("X-Forwarded-Host") != "" ||
		h.Get("X-Forwarded-Proto") != ""
}

// handleIdentity handles GET requests to /{latest|2009-04-04}/meta-data/identity?aud=<audience>
// Multiple aud parameters can be provided; the first is the primary audience,
// and additional ones are included as extra audiences in the JWT.
func (s *MetadataServer) handleIdentity(w http.ResponseWriter, r *http.Request) {
	slog.Info("Received identity request",
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
		"accept", r.Header.Get("Accept"))

	// Reject forwarded requests - metadata service must only accept direct connections
	if isForwardedRequest(r.Header) {
		slog.Warn("Rejecting forwarded request", "remote_addr", r.RemoteAddr)
		http.Error(w, "Forwarded requests not allowed", http.StatusForbidden)
		return
	}

	// Require Metadata: true header to prevent SSRF attacks
	if r.Header.Get("Metadata") != "true" {
		slog.Warn("Missing or invalid Metadata header", "remote_addr", r.RemoteAddr)
		http.Error(w, "Metadata: true header is required", http.StatusBadRequest)
		return
	}

	// Parse audiences from query parameters (URL decoding is handled automatically)
	// First aud is primary, additional ones are extra audiences
	audiences := r.URL.Query()["aud"]
	if len(audiences) == 0 || audiences[0] == "" {
		slog.Warn("Missing audience in request", "remote_addr", r.RemoteAddr)
		http.Error(w, "aud query parameter is required", http.StatusBadRequest)
		return
	}

	slog.Debug("Parsed identity request",
		"audiences", audiences,
		"remote_addr", r.RemoteAddr)

	ctx := r.Context()

	// Fetch JWT from local SPIRE socket
	slog.Info("Fetching JWT from local SPIRE socket",
		"audiences", audiences,
		"socket_path", s.spireSocketPath)

	spiffeJWT, err := s.fetchJWT(ctx, audiences)
	if err != nil {
		slog.Error("Failed to fetch JWT from SPIRE",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to fetch JWT: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("Fetched JWT, now exchanging",
		"original_spiffe_id", spiffeJWT.ID.String(),
		"audiences", audiences)

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
		ExpiresIn: exchangeResp.ExpiresIn,
		SpiffeID:  newSpiffeID,
	}

	slog.Info("Successfully exchanged JWT",
		"original_spiffe_id", spiffeJWT.ID.String(),
		"new_spiffe_id", newSpiffeID,
		"expires_in", exchangeResp.ExpiresIn,
		"remote_addr", r.RemoteAddr)

	// Determine response format based on Accept header
	responseFormat := determineResponseFormat(r.Header.Get("Accept"))
	w.Header().Set("Content-Type", responseFormat)

	// Preserialize response body so we can send the Content-Length header
	var responseBody []byte
	if responseFormat == "text/plain" {
		responseBody = []byte(exchangeResp.AccessToken)
	} else {
		var marshalErr error
		responseBody, marshalErr = json.Marshal(resp)
		if marshalErr != nil {
			slog.Error("Failed to marshal JSON response",
				"error", marshalErr,
				"remote_addr", r.RemoteAddr)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, writeErr := w.Write(responseBody); writeErr != nil {
		slog.Error("Failed to write response",
			"error", writeErr,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	slog.Debug("Response sent successfully",
		"format", responseFormat,
		"remote_addr", r.RemoteAddr)
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
	http.HandleFunc("GET /latest/meta-data/identity", server.handleIdentity)
	http.HandleFunc("GET /2009-04-04/meta-data/identity", server.handleIdentity)
	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("Health check", "remote_addr", r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		slog.Error("Failed to create listener", "error", err)
		panic(err)
	}

	// Wrap listener to set TTL=1 on all connections
	ttlLn := &ttlListener{Listener: ln}

	slog.Info("Starting HTTP server", "listen_addr", *listenAddr, "ip_ttl", 1)
	if err := http.Serve(ttlLn, nil); err != nil {
		slog.Error("HTTP server failed", "error", err)
		panic(err)
	}
}
