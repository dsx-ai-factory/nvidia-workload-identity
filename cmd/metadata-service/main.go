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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"golang.org/x/oauth2"

	"dsx-identity/internal/jwtsvidutil"
	"dsx-identity/internal/tokenexchange"
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
	svidHint        string
	httpClient      *http.Client
}

func newExchangeHTTPClient(socketPath string) *http.Client {
	dialer := new(net.Dialer)
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Force every exchange request through the CSI-provided local socket.
			// The URL host cannot bypass the node-local proxy over the network.
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func newMetadataMux(server *MetadataServer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /latest/meta-data/identity", server.handleIdentity)
	mux.HandleFunc("GET /2009-04-04/meta-data/identity", server.handleIdentity)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("Health check", "remote_addr", r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "OK")
	})
	return mux
}

// fetchJWTSVID fetches a JWT-SVID from the local SPIRE Workload API.
func (s *MetadataServer) fetchJWTSVID(ctx context.Context, audiences []string) (*jwtsvid.SVID, error) {
	socketAddr := fmt.Sprintf("unix://%s", s.spireSocketPath)

	slog.Debug("Fetching SPIRE JWT-SVID",
		"socket_path", s.spireSocketPath,
		"socket_addr", socketAddr,
		"audiences", audiences)

	var extraAudiences []string
	if len(audiences) > 1 {
		extraAudiences = audiences[1:]
	}

	svids, err := workloadapi.FetchJWTSVIDs(ctx, jwtsvid.Params{
		Audience:       audiences[0],
		ExtraAudiences: extraAudiences,
	}, workloadapi.WithAddr(socketAddr))
	if err != nil {
		slog.Error("Failed to fetch SPIRE JWT-SVID",
			"audiences", audiences,
			"error", err)
		return nil, fmt.Errorf("fetch SPIRE JWT-SVID: %w", err)
	}

	selected, err := jwtsvidutil.SelectByHint(svids, s.svidHint)
	if err != nil {
		return nil, err
	}

	slog.Info("Fetched SPIRE JWT-SVID",
		"spiffe_id", selected.ID.String(),
		"audiences", audiences,
		"expiry", selected.Expiry,
		"hint", selected.Hint)

	return selected, nil
}

// exchangeSPIRESVID exchanges a SPIRE JWT-SVID over the CSI Unix socket.
func (s *MetadataServer) exchangeSPIRESVID(ctx context.Context, token string) (*oauth2.Token, error) {
	slog.Debug("Creating token exchange request",
		"exchange_socket", s.exchangeSocket,
		"token_length", len(token))

	slog.Info("Sending token exchange request", "exchange_socket", s.exchangeSocket)

	// HTTP over Unix socket uses a dummy host (the socket handles routing).
	resp, err := tokenexchange.Exchange(ctx, s.httpClient, "http://exchange/v1/exchange", token)
	if err != nil {
		slog.Error("Failed to exchange JWT",
			"exchange_socket", s.exchangeSocket,
			"error", err)
		return nil, err
	}

	slog.Info("Successfully received exchange response", "expiry", resp.Expiry)

	return resp, nil
}

func determineResponseFormat(acceptHeader string) (string, error) {
	if acceptHeader == "" {
		return "application/json", nil
	}
	selected := ""
	selectedQuality := -1.0
	for value := range strings.SplitSeq(acceptHeader, ",") {
		mediaType, params, err := mime.ParseMediaType(value)
		if err != nil {
			return "", fmt.Errorf("invalid Accept header: %w", err)
		}
		if mediaType != "application/json" && mediaType != "text/plain" {
			continue
		}
		quality := 1.0
		if rawQuality, ok := params["q"]; ok {
			quality, err = strconv.ParseFloat(rawQuality, 64)
			if err != nil || quality < 0 || quality > 1 {
				return "", fmt.Errorf("invalid Accept quality %q", rawQuality)
			}
		}
		if quality > 0 && quality > selectedQuality {
			selected = mediaType
			selectedQuality = quality
		}
	}
	if selected == "" {
		return "", fmt.Errorf("Accept header does not allow application/json or text/plain")
	}
	return selected, nil
}

// isForwardedRequest returns true if the request contains forwarding headers,
// indicating it came through a proxy rather than directly from a client.
func isForwardedRequest(h http.Header) bool {
	for _, key := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if _, present := h[key]; present {
			return true
		}
	}
	return false
}

// handleIdentity handles GET requests to /{latest|2009-04-04}/meta-data/identity?aud=<audience>
// Multiple aud parameters can be provided. The first is the primary audience,
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

	audiences := r.URL.Query()["aud"]
	if len(audiences) == 0 || audiences[0] == "" {
		slog.Warn("Missing audience in request", "remote_addr", r.RemoteAddr)
		http.Error(w, "aud query parameter is required", http.StatusBadRequest)
		return
	}
	responseFormat, err := determineResponseFormat(r.Header.Get("Accept"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotAcceptable)
		return
	}

	slog.Debug("Parsed identity request",
		"audiences", audiences,
		"remote_addr", r.RemoteAddr)

	ctx := r.Context()

	slog.Info("Fetching SPIRE JWT-SVID from local socket",
		"audiences", audiences,
		"socket_path", s.spireSocketPath)

	spireSVID, err := s.fetchJWTSVID(ctx, audiences)
	if err != nil {
		slog.Error("Failed to fetch SPIRE JWT-SVID",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to fetch SPIRE JWT-SVID: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("Exchanging SPIRE JWT-SVID",
		"original_spiffe_id", spireSVID.ID.String(),
		"audiences", audiences)

	exchangeResp, err := s.exchangeSPIRESVID(ctx, spireSVID.Marshal())
	if err != nil {
		slog.Error("Failed to exchange JWT",
			"original_spiffe_id", spireSVID.ID.String(),
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, fmt.Sprintf("Failed to exchange JWT: %v", err), http.StatusInternalServerError)
		return
	}

	// The local Exchange just minted this token over the CSI socket. Parse its
	// SPIFFE claims only to expose the subject. Relying parties verify its signature.
	dsxJWT, err := jwtsvid.ParseInsecure(exchangeResp.AccessToken, audiences)
	if err != nil {
		slog.Error("Failed to extract SPIFFE ID from exchanged JWT",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "Failed to process exchanged JWT", http.StatusInternalServerError)
		return
	}
	newSpiffeID := dsxJWT.ID.String()

	resp := IdentityResponse{
		Token:     exchangeResp.AccessToken,
		ExpiresIn: exchangeResp.ExpiresIn,
		SpiffeID:  newSpiffeID,
	}

	slog.Info("Successfully exchanged JWT",
		"original_spiffe_id", spireSVID.ID.String(),
		"new_spiffe_id", newSpiffeID,
		"expires_in", exchangeResp.ExpiresIn,
		"remote_addr", r.RemoteAddr)

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
	var level slog.Level
	flag.TextVar(&level, "log-level", slog.LevelInfo, "Log level (debug, info, warn, error)")
	var (
		listenAddr      = flag.String("listen", ":80", "HTTP server listen address")
		exchangeSocket  = flag.String("exchange-socket", "", "Path to exchange service Unix socket")
		spireSocketPath = flag.String("spire-socket", defaultSpireSocketPath, "Path to SPIRE agent socket")
		svidHint        = flag.String("svid-hint", "dsx-imds", "Exact JWT-SVID hint (dsx-imds or dsx-imds-vm)")
	)
	flag.Parse()

	if *exchangeSocket == "" {
		fmt.Fprintln(os.Stderr, "error: --exchange-socket is required")
		os.Exit(1)
	}
	if *svidHint != "dsx-imds" && *svidHint != "dsx-imds-vm" {
		fmt.Fprintln(os.Stderr, "error: --svid-hint must be dsx-imds or dsx-imds-vm")
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))

	slog.Info("Starting SPIFFE Metadata Service",
		"listen_addr", *listenAddr,
		"log_level", level.String(),
		"exchange_socket", *exchangeSocket,
		"spire_socket", *spireSocketPath,
		"svid_hint", *svidHint)

	server := &MetadataServer{
		exchangeSocket:  *exchangeSocket,
		spireSocketPath: *spireSocketPath,
		svidHint:        *svidHint,
		httpClient:      newExchangeHTTPClient(*exchangeSocket),
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		slog.Error("Failed to create listener", "error", err)
		panic(err)
	}

	// Wrap listener to set TTL=1 on all connections
	ttlLn := &ttlListener{Listener: ln}
	httpServer := &http.Server{
		Handler:           newMetadataMux(server),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	slog.Info("Starting HTTP server", "listen_addr", *listenAddr, "ip_ttl", 1)
	if err := httpServer.Serve(ttlLn); err != nil {
		slog.Error("HTTP server failed", "error", err)
		panic(err)
	}
}
