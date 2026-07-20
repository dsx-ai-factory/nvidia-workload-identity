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
	"net/url"
	"os"
	"syscall"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp/filters"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	"dsx-identity/internal/jwtsvidutil"
	"dsx-identity/internal/telemetry"
	"dsx-identity/internal/tokenexchange"
)

const (
	defaultSpireSocketPath = "/var/run/secrets/spire.io/spire-agent.sock"
)

type IdentityResponse struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"` // Seconds until expiration (OAuth2 RFC 6749)
	SpiffeID  string `json:"spiffe_id"`
}

type ttlListener struct {
	net.Listener
}

func (l ttlListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tcpConn := conn.(*net.TCPConn)
	rawConn, err := tcpConn.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, err
	}
	var socketErr error
	err = rawConn.Control(func(fd uintptr) {
		if tcpConn.RemoteAddr().(*net.TCPAddr).IP.To4() != nil {
			socketErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, 1)
		} else {
			socketErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, 1)
		}
	})
	if err == nil {
		err = socketErr
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

type MetadataServer struct {
	svids    svidFetcher
	exchange tokenExchanger
}

type svidFetcher interface {
	Fetch(context.Context, []string) (*jwtsvid.SVID, error)
}

type tokenExchanger interface {
	Exchange(context.Context, string) (*oauth2.Token, error)
}

type workloadSVIDFetcher struct {
	socketPath string
	hint       string
}

type exchangeClient struct {
	endpoint   string
	httpClient *http.Client
}

func newExchangeClient(exchangeURL string) (*exchangeClient, error) {
	if exchangeURL == "" {
		return nil, errors.New("--identity-keys-url is required")
	}
	u, err := url.Parse(exchangeURL)
	if err != nil {
		return nil, fmt.Errorf("parse DSX Identity Keys URL: %w", err)
	}
	if u.Scheme == "unix" {
		return &exchangeClient{
			endpoint:   "http://exchange/v1/exchange",
			httpClient: newUnixHTTPClient(u.Path),
		}, nil
	}
	return &exchangeClient{
		endpoint: exchangeURL,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}, nil
}

func newUnixHTTPClient(socketPath string) *http.Client {
	dialer := new(net.Dialer)
	transport := &http.Transport{
		// Force every exchange request through the CSI-provided local socket.
		// The URL host cannot bypass the node-local proxy over the network.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: otelhttp.NewTransport(transport),
	}
}

func newMetadataMux(server *MetadataServer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /latest/meta-data/identity", server.handleIdentity)
	mux.HandleFunc("GET /2009-04-04/meta-data/identity", server.handleIdentity)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		slog.DebugContext(r.Context(), "Health check", "remote_addr", r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "OK")
	})
	return mux
}

func (f workloadSVIDFetcher) Fetch(ctx context.Context, audiences []string) (*jwtsvid.SVID, error) {
	socketAddr := fmt.Sprintf("unix://%s", f.socketPath)

	slog.DebugContext(ctx, "Fetching SPIRE JWT-SVID",
		"socket_path", f.socketPath,
		"socket_addr", socketAddr,
		"audiences", audiences)

	var extraAudiences []string
	if len(audiences) > 1 {
		extraAudiences = audiences[1:]
	}

	svids, err := workloadapi.FetchJWTSVIDs(ctx, jwtsvid.Params{
		Audience:       audiences[0],
		ExtraAudiences: extraAudiences,
	},
		workloadapi.WithAddr(socketAddr),
		workloadapi.WithDialOptions(grpc.WithStatsHandler(otelgrpc.NewClientHandler())),
	)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to fetch SPIRE JWT-SVID",
			"audiences", audiences,
			"error", err)
		return nil, fmt.Errorf("fetch SPIRE JWT-SVID: %w", err)
	}

	selected, err := jwtsvidutil.SelectByHint(svids, f.hint)
	if err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "Fetched SPIRE JWT-SVID",
		"spiffe_id", selected.ID.String(),
		"audiences", audiences,
		"expiry", selected.Expiry,
		"hint", selected.Hint)

	return selected, nil
}

func (c *exchangeClient) Exchange(ctx context.Context, token string) (*oauth2.Token, error) {
	slog.DebugContext(ctx, "Creating token exchange request",
		"exchange_endpoint", c.endpoint,
		"token_length", len(token))

	slog.InfoContext(ctx, "Sending token exchange request", "exchange_endpoint", c.endpoint)

	resp, err := tokenexchange.Exchange(ctx, c.httpClient, c.endpoint, token)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to exchange JWT",
			"exchange_endpoint", c.endpoint,
			"error", err)
		return nil, err
	}

	slog.InfoContext(ctx, "Successfully received exchange response", "expiry", resp.Expiry)

	return resp, nil
}

func determineResponseFormat(acceptHeader string) (string, error) {
	if acceptHeader == "" {
		return "application/json", nil
	}
	mediaType, _, err := mime.ParseMediaType(acceptHeader)
	if err != nil {
		return "", fmt.Errorf("parse Accept header: %w", err)
	}
	if mediaType == "application/json" || mediaType == "text/plain" {
		return mediaType, nil
	}
	return "", fmt.Errorf("Accept header does not allow application/json or text/plain")
}

// isForwardedRequest returns true if the request contains forwarding headers,
// indicating it came through a proxy rather than directly from a client.
func isForwardedRequest(h http.Header) bool {
	for _, key := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if len(h.Values(key)) > 0 {
			return true
		}
	}
	return false
}

// handleIdentity handles GET requests to /{latest|2009-04-04}/meta-data/identity?aud=<audience>
// Multiple aud parameters can be provided. The first is the primary audience,
// and additional ones are included as extra audiences in the JWT.
func (s *MetadataServer) handleIdentity(w http.ResponseWriter, r *http.Request) {
	slog.InfoContext(r.Context(), "Received identity request",
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
		"accept", r.Header.Get("Accept"))

	// Reject forwarded requests - metadata service must only accept direct connections
	if isForwardedRequest(r.Header) {
		slog.WarnContext(r.Context(), "Rejecting forwarded request", "remote_addr", r.RemoteAddr)
		http.Error(w, "Forwarded requests not allowed", http.StatusForbidden)
		return
	}

	// Require Metadata: true header to prevent SSRF attacks
	if r.Header.Get("Metadata") != "true" {
		slog.WarnContext(r.Context(), "Missing or invalid Metadata header", "remote_addr", r.RemoteAddr)
		http.Error(w, "Metadata: true header is required", http.StatusBadRequest)
		return
	}

	audiences := r.URL.Query()["aud"]
	if len(audiences) == 0 {
		slog.WarnContext(r.Context(), "Missing audience in request", "remote_addr", r.RemoteAddr)
		http.Error(w, "aud query parameter is required", http.StatusBadRequest)
		return
	}
	for _, audience := range audiences {
		if audience == "" {
			slog.WarnContext(r.Context(), "Empty audience in request", "remote_addr", r.RemoteAddr)
			http.Error(w, "aud query parameters must not be empty", http.StatusBadRequest)
			return
		}
	}
	responseFormat, err := determineResponseFormat(r.Header.Get("Accept"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotAcceptable)
		return
	}

	slog.DebugContext(r.Context(), "Parsed identity request",
		"audiences", audiences,
		"remote_addr", r.RemoteAddr)

	ctx := r.Context()

	slog.InfoContext(r.Context(), "Fetching SPIRE JWT-SVID from local socket", "audiences", audiences)

	spireSVID, err := s.svids.Fetch(ctx, audiences)
	if err != nil {
		slog.ErrorContext(r.Context(), "Failed to fetch SPIRE JWT-SVID",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}

	slog.InfoContext(r.Context(), "Exchanging SPIRE JWT-SVID",
		"original_spiffe_id", spireSVID.ID.String(),
		"audiences", audiences)

	exchangeResp, err := s.exchange.Exchange(ctx, spireSVID.Marshal())
	if err != nil {
		slog.ErrorContext(r.Context(), "Failed to exchange JWT",
			"original_spiffe_id", spireSVID.ID.String(),
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}

	// The local DSX Identity Keys just minted this token over the CSI socket. Parse its
	// SPIFFE claims only to expose the subject. Relying parties verify its signature.
	dsxJWT, err := jwtsvid.ParseInsecure(exchangeResp.AccessToken, audiences)
	if err != nil {
		slog.ErrorContext(r.Context(), "Failed to extract SPIFFE ID from exchanged JWT",
			"error", err,
			"remote_addr", r.RemoteAddr)
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	newSpiffeID := dsxJWT.ID.String()

	resp := IdentityResponse{
		Token:     exchangeResp.AccessToken,
		ExpiresIn: exchangeResp.ExpiresIn,
		SpiffeID:  newSpiffeID,
	}

	slog.InfoContext(r.Context(), "Successfully exchanged JWT",
		"original_spiffe_id", spireSVID.ID.String(),
		"new_spiffe_id", newSpiffeID,
		"expires_in", exchangeResp.ExpiresIn,
		"remote_addr", r.RemoteAddr)

	w.Header().Set("Content-Type", responseFormat)
	w.Header().Set("Cache-Control", "no-store")

	// Preserialize response body so we can send the Content-Length header
	var responseBody []byte
	if responseFormat == "text/plain" {
		responseBody = []byte(exchangeResp.AccessToken)
	} else {
		var marshalErr error
		responseBody, marshalErr = json.Marshal(resp)
		if marshalErr != nil {
			slog.ErrorContext(r.Context(), "Failed to marshal JSON response",
				"error", marshalErr,
				"remote_addr", r.RemoteAddr)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
	if _, writeErr := w.Write(responseBody); writeErr != nil {
		slog.ErrorContext(r.Context(), "Failed to write response",
			"error", writeErr,
			"remote_addr", r.RemoteAddr)
		return
	}

	slog.DebugContext(r.Context(), "Response sent successfully",
		"format", responseFormat,
		"remote_addr", r.RemoteAddr)
}

func run() (runErr error) {
	var logLevel slog.Level
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	var (
		listenAddr      = flag.String("listen", ":80", "HTTP server listen address")
		identityKeysURL = flag.String("identity-keys-url", "", "RFC 8693 endpoint of shard-local DSX Identity Keys")
		spireSocketPath = flag.String("spire-socket", defaultSpireSocketPath, "Path to SPIRE agent socket")
		svidHint        = flag.String("svid-hint", "dsx-imds", "Exact JWT-SVID hint (dsx-imds or dsx-imds-vm)")
	)
	flag.Parse()

	sdk, err := telemetry.Setup(context.Background(), "metadata-service", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()
	exchange, err := newExchangeClient(*identityKeysURL)
	if err != nil {
		return err
	}

	slog.Info("Starting SPIFFE Metadata Service",
		"listen_addr", *listenAddr,
		"log_level", logLevel.String(),
		"identity_keys_url", *identityKeysURL,
		"spire_socket", *spireSocketPath,
		"svid_hint", *svidHint)

	server := &MetadataServer{
		svids: workloadSVIDFetcher{
			socketPath: *spireSocketPath,
			hint:       *svidHint,
		},
		exchange: exchange,
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return fmt.Errorf("create listener: %w", err)
	}

	httpServer := &http.Server{
		Handler: otelhttp.NewHandler(newMetadataMux(server), "metadata-service",
			otelhttp.WithFilter(filters.None(filters.Path("/health")))),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	slog.Info("Starting HTTP server", "listen_addr", *listenAddr, "ip_ttl", 1, "ipv6_hop_limit", 1)
	if err := httpServer.Serve(ttlListener{Listener: ln}); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("Metadata service failed", "error", err)
		os.Exit(1)
	}
}
