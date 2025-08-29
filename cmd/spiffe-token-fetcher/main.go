package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// parseSocketPath validates and parses the socket URL
func parseSocketPath(socketPath string) (string, error) {
	u, err := url.Parse(socketPath)
	if err != nil {
		return "", fmt.Errorf("invalid socket URL: %w", err)
	}

	if u.Scheme != "unix" {
		return "", fmt.Errorf("unsupported scheme '%s', only 'unix' is supported", u.Scheme)
	}

	if u.Path == "" {
		return "", fmt.Errorf("socket path cannot be empty")
	}

	return u.Path, nil
}

// calculateRefreshTime calculates when to refresh the JWT (80% of lifetime)
func calculateRefreshTime(issuedAt, expiresAt time.Time) time.Duration {
	lifetime := expiresAt.Sub(issuedAt)
	refreshIn := time.Duration(float64(lifetime) * 0.8)

	// Ensure minimum refresh interval of 30 seconds
	minRefresh := 30 * time.Second
	if refreshIn < minRefresh {
		refreshIn = minRefresh
	}

	return refreshIn
}

// fetchJWTWithRetry fetches a JWT SVID with retry logic
func fetchJWTWithRetry(ctx context.Context, jwtSource *workloadapi.JWTSource, audience string, maxRetries int) (*jwtsvid.SVID, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		jwt, err := jwtSource.FetchJWTSVID(ctx, jwtsvid.Params{
			Audience: audience,
		})
		if err == nil {
			return jwt, nil
		}

		lastErr = err
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			log.Printf("Attempt %d failed: %v. Retrying in %v...", attempt, err, backoff)
			time.Sleep(backoff)
		}
	}

	return nil, fmt.Errorf("failed to fetch JWT after %d attempts: %w", maxRetries, lastErr)
}

func main() {
	var (
		socketPath     = flag.String("socket", "unix:///var/run/secrets/spire.io/spire-agent.sock", "Path to SPIRE Workload API socket")
		audience       = flag.String("audience", "spiffe://example.org", "JWT audience")
		outputPath     = flag.String("output", "/var/run/secrets/nvidia.com/spiffe/token", "Output file path for JWT token")
		maxRetries     = flag.Int("retries", 3, "Maximum number of retry attempts")
		verbose        = flag.Bool("verbose", true, "Enable verbose logging")
		exitAfterFetch = flag.Bool("exit-after-fetch", false, "Exit after fetching the initial JWT (for init container use)")
	)
	flag.Parse()

	if *verbose {
		log.Printf("Starting SPIFFE JWT token fetcher")
		log.Printf("Socket: %s", *socketPath)
		log.Printf("Audience: %s", *audience)
		log.Printf("Output: %s", *outputPath)
		log.Printf("Max retries: %d", *maxRetries)
		log.Printf("Exit after fetch: %t", *exitAfterFetch)
	}

	ctx := context.Background()

	// Parse and validate socket path
	socketFilePath, err := parseSocketPath(*socketPath)
	if err != nil {
		log.Fatalf("Invalid socket path: %v", err)
	}

	// Wait for socket to be available
	for {
		if _, err := os.Stat(socketFilePath); err == nil {
			break
		}
		if *verbose {
			log.Println("Waiting for SPIRE agent socket...")
		}
		time.Sleep(2 * time.Second)
	}

	if *verbose {
		log.Println("SPIRE agent socket found, connecting...")
	}

	// Create JWT source for JWT-SVID
	jwtSource, err := workloadapi.NewJWTSource(ctx, workloadapi.WithClientOptions(workloadapi.WithAddr(*socketPath)))
	if err != nil {
		log.Fatalf("Unable to create JWT source: %v", err)
	}
	defer jwtSource.Close()

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0755); err != nil {
		log.Fatalf("Unable to create output directory: %v", err)
	}

	for {
		if *verbose {
			log.Println("Fetching SPIFFE JWT...")
		}

		// Fetch JWT SVID with retry logic
		jwt, err := fetchJWTWithRetry(ctx, jwtSource, *audience, *maxRetries)
		if err != nil {
			log.Fatalf("Unable to fetch JWT SVID after %d attempts: %v", *maxRetries, err)
		}

		// Write JWT to file
		jwtData := jwt.Marshal()
		err = os.WriteFile(*outputPath, []byte(jwtData), 0644)
		if err != nil {
			log.Fatalf("Failed to write JWT: %v", err)
		}

		if *verbose {
			log.Printf("SPIFFE JWT updated for identity: %s", jwt.ID)
			log.Printf("JWT length: %d characters", len(jwtData))
		}

		// Exit after initial fetch if running as init container
		if *exitAfterFetch {
			if *verbose {
				log.Println("Initial JWT fetch complete, exiting (init container mode)")
			}
			return
		}

		// Use the JWT's built-in Expiry method
		expiryTime := jwt.Expiry

		// Calculate refresh time (80% of JWT lifetime)
		issuedAt := time.Now()
		refreshInterval := calculateRefreshTime(issuedAt, expiryTime)

		if *verbose {
			log.Printf("JWT expires at: %s", expiryTime.Format(time.RFC3339))
			log.Printf("Next refresh in: %v (80%% of lifetime)", refreshInterval)
		}

		// Wait until it's time to refresh
		time.Sleep(refreshInterval)
	}
}
