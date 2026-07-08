package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"golang.org/x/oauth2"

	"dsx-identity/internal/jwtsvidutil"
	"dsx-identity/internal/stricturl"
	"dsx-identity/internal/tokenexchange"
)

// parseSocketPath accepts one canonical spelling for a Workload API Unix socket
// so equivalent but ambiguous URL forms cannot reach different parsers later.
func parseSocketPath(socketPath string) (string, error) {
	u, err := url.Parse(socketPath)
	if err != nil {
		return "", fmt.Errorf("invalid socket URL: %w", err)
	}

	if u.Scheme != "unix" {
		return "", fmt.Errorf("unsupported scheme '%s', only 'unix' is supported", u.Scheme)
	}

	if u.Host != "" || u.User != nil || u.Opaque != "" || u.RawPath != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.String() != socketPath {
		return "", fmt.Errorf("socket URL must contain only an absolute path")
	}
	if u.Path == "" || !filepath.IsAbs(u.Path) || filepath.Clean(u.Path) != u.Path {
		return "", fmt.Errorf("socket path must be absolute and clean")
	}

	return u.Path, nil
}

// calculateRefreshTime calculates when to refresh the JWT (80% of lifetime)
func calculateRefreshTime(issuedAt, expiresAt time.Time) time.Duration {
	lifetime := expiresAt.Sub(issuedAt)
	refreshIn := time.Duration(float64(lifetime) * 0.8)

	// Avoid a hot refresh loop, but never delay refresh past token expiry.
	minRefresh := 30 * time.Second
	if refreshIn < minRefresh && lifetime > minRefresh {
		refreshIn = minRefresh
	}

	return refreshIn
}

type jwtSVIDSource interface {
	FetchJWTSVID(context.Context, jwtsvid.Params) (*jwtsvid.SVID, error)
	FetchJWTSVIDs(context.Context, jwtsvid.Params) ([]*jwtsvid.SVID, error)
}

// refreshContext bounds a refresh attempt by the current token expiry. This
// prevents a blocked refresh from keeping an expired token available to readers.
func refreshContext(ctx context.Context, expiry time.Time) (context.Context, context.CancelFunc) {
	if expiry.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, expiry)
}

func invalidateToken(ready *atomic.Bool, path string) error {
	ready.Store(false)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale token: %w", err)
	}
	return nil
}

func retryRefreshOrInvalidateToken(ctx context.Context, ready *atomic.Bool, path string, expiry time.Time, delay time.Duration) (bool, error) {
	// Keep serving the last complete token while it remains valid. Once it
	// expires, remove it before reporting not-ready so consumers cannot reuse it.
	if !expiry.IsZero() && expiry.After(time.Now()) {
		retryCtx, cancel := refreshContext(ctx, expiry)
		err := wait(retryCtx, delay)
		cancel()
		if err == nil {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, nil
		}
	}
	if expiry.IsZero() {
		return false, nil
	}
	return false, invalidateToken(ready, path)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// fetchJWTSVIDWithRetry fetches a SPIRE JWT-SVID with retry logic.
func fetchJWTSVIDWithRetry(ctx context.Context, jwtSource jwtSVIDSource, audience, hint string, maxRetries int) (*jwtsvid.SVID, error) {
	var lastErr error
	params := jwtsvid.Params{Audience: audience}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var selected *jwtsvid.SVID
		var err error
		if hint == "" {
			// FetchJWTSVID owns default selection. go-spiffe honors a configured
			// default picker and otherwise uses the first SVID returned by SPIRE.
			selected, err = jwtSource.FetchJWTSVID(ctx, params)
			if err == nil && selected == nil {
				err = fmt.Errorf("no default JWT-SVID returned")
			}
		} else {
			var svids []*jwtsvid.SVID
			svids, err = jwtSource.FetchJWTSVIDs(ctx, params)
			if err == nil {
				selected, err = jwtsvidutil.SelectByHint(svids, hint)
			}
		}
		if err == nil {
			return selected, nil
		}

		lastErr = err
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			log.Printf("Attempt %d failed: %v. Retrying in %v...", attempt, err, backoff)
			if err := wait(ctx, backoff); err != nil {
				return nil, err
			}
		}
	}

	return nil, fmt.Errorf("failed to fetch SPIRE JWT-SVID after %d attempts: %w", maxRetries, lastErr)
}

func writeToken(path string, token []byte) error {
	// Readers share this file with the refresh loop. Write and sync a private file
	// first, then rename it so they observe either the old token or the new token.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure output directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("create temporary token: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure temporary token: %w", err)
	}
	if _, err := tmp.Write(token); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary token: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace token: %w", err)
	}
	return nil
}

// healthHandler reads an atomic flag because the refresh loop and HTTP server
// update and observe readiness from different goroutines.
func healthHandler(ready *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
}

func startHealthServer(address string, handler http.Handler) (*http.Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Addr: address, Handler: handler}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("Health server failed: %v", err)
		}
	}()
	return server, nil
}

// exchangeSPIRESVIDWithRetry exchanges a SPIRE JWT-SVID with retry logic.
func exchangeSPIRESVIDWithRetry(ctx context.Context, httpClient *http.Client, exchangeServiceURL, token string, maxRetries int) (*oauth2.Token, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := tokenexchange.Exchange(ctx, httpClient, exchangeServiceURL+"/v1/exchange", token)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			log.Printf("Exchange attempt %d failed: %v. Retrying in %v...", attempt, err, backoff)
			if err := wait(ctx, backoff); err != nil {
				return nil, err
			}
		}
	}

	return nil, fmt.Errorf("failed to exchange JWT after %d attempts: %w", maxRetries, lastErr)
}

func run() error {
	var (
		socketPath         = flag.String("socket", "unix:///var/run/secrets/spire.io/spire-agent.sock", "Path to SPIRE Workload API socket")
		audience           = flag.String("audience", "spiffe://example.org", "JWT audience")
		outputPath         = flag.String("output", "/var/run/secrets/nvidia.com/dsx/token", "output file path for the DSX JWT")
		maxRetries         = flag.Int("retries", 3, "Maximum number of retry attempts")
		verbose            = flag.Bool("verbose", true, "Enable verbose logging")
		exitAfterFetch     = flag.Bool("exit-after-fetch", false, "Exit after fetching the initial JWT (for init container use)")
		exchangeServiceURL = flag.String("exchange-service-url", "", "URL of the SPIFFE exchange service")
		healthPort         = flag.Int("health-port", 8080, "Port for the /healthz endpoint")
		svidHint           = flag.String("svid-hint", "", "Optional exact JWT-SVID hint override")
	)
	flag.Parse()

	if *exchangeServiceURL == "" {
		return errors.New("--exchange-service-url is required")
	}
	// The fixed exchange route is appended below, so accept only a service origin.
	if _, err := stricturl.ParseHTTPOrigin(*exchangeServiceURL); err != nil {
		return fmt.Errorf("--exchange-service-url %w", err)
	}
	if *maxRetries < 1 {
		return errors.New("--retries must be at least 1")
	}
	var ready atomic.Bool

	if *verbose {
		log.Printf("Starting DSX token-fetcher")
		log.Printf("Socket: %s", *socketPath)
		log.Printf("Audience: %s", *audience)
		log.Printf("Output: %s", *outputPath)
		log.Printf("Max retries: %d", *maxRetries)
		log.Printf("Exit after fetch: %t", *exitAfterFetch)
		log.Printf("Exchange service URL: %s", *exchangeServiceURL)
		log.Printf("Health port: %d", *healthPort)
		log.Printf("SVID hint: %s", *svidHint)
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", healthHandler(&ready))
	healthServer, err := startHealthServer(fmt.Sprintf(":%d", *healthPort), mux)
	if err != nil {
		return fmt.Errorf("start health server: %w", err)
	}
	defer healthServer.Close()

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Parse and validate socket path
	socketFilePath, err := parseSocketPath(*socketPath)
	if err != nil {
		return fmt.Errorf("invalid socket path: %w", err)
	}
	if err := os.Remove(*outputPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale token: %w", err)
	}
	if !*exitAfterFetch {
		defer os.Remove(*outputPath)
	}

	// Wait for socket to be available
	var currentExpiry time.Time
	for {
		if _, err := os.Stat(socketFilePath); err == nil {
			break
		}
		if *verbose {
			log.Println("Waiting for SPIRE agent socket...")
		}
		if err := wait(ctx, 2*time.Second); err != nil {
			return nil
		}
	}

	if *verbose {
		log.Println("SPIRE agent socket found, connecting...")
	}

	jwtSource, err := workloadapi.New(ctx, workloadapi.WithAddr(*socketPath))
	if err != nil {
		return fmt.Errorf("create Workload API client: %w", err)
	}
	defer jwtSource.Close()

	refresh := func(refreshCtx context.Context) (time.Time, error) {
		if *verbose {
			log.Println("Fetching SPIRE JWT-SVID...")
		}
		spireSVID, err := fetchJWTSVIDWithRetry(refreshCtx, jwtSource, *audience, *svidHint, *maxRetries)
		if err != nil {
			return time.Time{}, fmt.Errorf("fetch SPIRE JWT-SVID: %w", err)
		}

		if *verbose {
			log.Printf("Exchanging JWT via %s", *exchangeServiceURL)
		}
		exchangeResp, err := exchangeSPIRESVIDWithRetry(refreshCtx, httpClient, *exchangeServiceURL, spireSVID.Marshal(), *maxRetries)
		if err != nil {
			return time.Time{}, fmt.Errorf("exchange SPIRE JWT-SVID: %w", err)
		}
		if !exchangeResp.Expiry.After(time.Now()) {
			return time.Time{}, fmt.Errorf("exchange returned a token without a future expiry")
		}
		if err := writeToken(*outputPath, []byte(exchangeResp.AccessToken)); err != nil {
			return time.Time{}, fmt.Errorf("write DSX JWT: %w", err)
		}

		if *verbose {
			log.Printf("Exchanged SPIRE JWT-SVID for identity: %s", spireSVID.ID)
			log.Printf("DSX JWT written to %s (%d characters)", *outputPath, len(exchangeResp.AccessToken))
		}
		return exchangeResp.Expiry, nil
	}

	for {
		attemptCtx, cancelAttempt := refreshContext(ctx, currentExpiry)
		expiryTime, refreshErr := refresh(attemptCtx)
		cancelAttempt()
		if refreshErr != nil {
			retry, invalidateErr := retryRefreshOrInvalidateToken(ctx, &ready, *outputPath, currentExpiry, time.Second)
			if ctx.Err() != nil {
				return nil
			}
			if retry {
				log.Printf("Unable to refresh JWT: %v; existing token remains valid, retrying", refreshErr)
				continue
			}
			return errors.Join(fmt.Errorf("refresh JWT: %w", refreshErr), invalidateErr)
		}

		if !ready.Swap(true) {
			if *verbose {
				log.Println("Health check is now ready")
			}
		}
		currentExpiry = expiryTime

		// Exit after initial fetch if running as init container
		if *exitAfterFetch {
			if *verbose {
				log.Println("Initial JWT fetch complete, exiting (init container mode)")
			}
			return nil
		}

		// Calculate refresh time (80% of JWT lifetime)
		issuedAt := time.Now()
		refreshInterval := calculateRefreshTime(issuedAt, expiryTime)

		if *verbose {
			log.Printf("JWT expires at: %s", expiryTime.Format(time.RFC3339))
			log.Printf("Next refresh in: %v (80%% of lifetime)", refreshInterval)
		}

		// Wait until it's time to refresh
		if err := wait(ctx, refreshInterval); err != nil {
			return nil
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
