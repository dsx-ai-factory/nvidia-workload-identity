// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"dsx-identity/internal/jwtsvidutil"
	"dsx-identity/internal/telemetry"
	"dsx-identity/internal/tokenexchange"
)

// calculateRefreshTime calculates when to refresh the JWT (80% of lifetime).
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

func invalidateToken(ready *atomic.Bool, path string) error {
	ready.Store(false)
	if err := removeToken(path); err != nil {
		return fmt.Errorf("remove stale token: %w", err)
	}
	return nil
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

func fetchJWTSVID(ctx context.Context, jwtSource jwtSVIDSource, audience, hint string) (*jwtsvid.SVID, error) {
	params := jwtsvid.Params{Audience: audience}
	if hint == "" {
		selected, err := jwtSource.FetchJWTSVID(ctx, params)
		if err != nil {
			return nil, err
		}
		if selected == nil {
			return nil, fmt.Errorf("no default JWT-SVID returned")
		}
		return selected, nil
	}
	svids, err := jwtSource.FetchJWTSVIDs(ctx, params)
	if err != nil {
		return nil, err
	}
	return jwtsvidutil.SelectByHint(svids, hint)
}

func writeToken(path string, token []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("create temporary token: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("make token readable: %w", err)
	}
	if _, err := tmp.Write(token); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace token: %w", err)
	}
	return nil
}

func removeToken(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove token: %w", err)
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
			slog.Error("Health server failed", "error", err)
		}
	}()
	return server, nil
}

func run() (runErr error) {
	var (
		socketPath      = flag.String("socket", "unix:///var/run/secrets/spire.io/spire-agent.sock", "Path to SPIRE Workload API socket")
		audience        = flag.String("audience", "spiffe://example.org", "JWT audience")
		outputPath      = flag.String("output", "/run/secrets/nvidia.com/dsx/token", "output file path for the DSX JWT")
		logLevel        slog.Level
		identityKeysURL = flag.String("identity-keys-url", "", "RFC 8693 endpoint of DSX Identity Keys")
		healthPort      = flag.Int("health-port", 8080, "Port for the /healthz endpoint")
		svidHint        = flag.String("svid-hint", "", "Optional exact JWT-SVID hint override")
	)
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	flag.Parse()
	sdk, err := telemetry.Setup(context.Background(), "dsx-token-fetcher", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()

	if *identityKeysURL == "" {
		return errors.New("--identity-keys-url is required")
	}
	var ready atomic.Bool

	slog.Info("Starting DSX token fetcher",
		"socket", *socketPath,
		"audience", *audience,
		"output", *outputPath,
		"identity_keys_url", *identityKeysURL,
		"health_port", *healthPort,
		"svid_hint", *svidHint)

	mux := http.NewServeMux()
	mux.Handle("/healthz", healthHandler(&ready))
	healthServer, err := startHealthServer(fmt.Sprintf(":%d", *healthPort), mux)
	if err != nil {
		return fmt.Errorf("start health server: %w", err)
	}
	defer healthServer.Close()

	httpClient := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := removeToken(*outputPath); err != nil {
		return fmt.Errorf("remove stale token: %w", err)
	}
	defer func() {
		if err := removeToken(*outputPath); err != nil {
			slog.Warn("Failed to remove token during shutdown", "error", err)
		}
	}()

	var currentExpiry time.Time
	jwtSource, err := workloadapi.New(ctx,
		workloadapi.WithAddr(*socketPath),
		workloadapi.WithDialOptions(grpc.WithStatsHandler(otelgrpc.NewClientHandler())),
	)
	if err != nil {
		return fmt.Errorf("create Workload API client: %w", err)
	}
	defer jwtSource.Close()

	refresh := func(refreshCtx context.Context) (time.Time, error) {
		slog.InfoContext(refreshCtx, "Fetching SPIRE JWT-SVID")
		spireSVID, err := fetchJWTSVID(refreshCtx, jwtSource, *audience, *svidHint)
		if err != nil {
			return time.Time{}, fmt.Errorf("fetch SPIRE JWT-SVID: %w", err)
		}

		slog.InfoContext(refreshCtx, "Exchanging SPIRE JWT-SVID", "identity_keys_url", *identityKeysURL)
		exchangeResp, err := tokenexchange.Exchange(refreshCtx, httpClient, *identityKeysURL, spireSVID.Marshal())
		if err != nil {
			return time.Time{}, fmt.Errorf("exchange SPIRE JWT-SVID: %w", err)
		}
		if !exchangeResp.Expiry.After(time.Now()) {
			return time.Time{}, fmt.Errorf("exchange returned a token without a future expiry")
		}
		if err := writeToken(*outputPath, []byte(exchangeResp.AccessToken)); err != nil {
			return time.Time{}, fmt.Errorf("write DSX JWT: %w", err)
		}

		slog.InfoContext(refreshCtx, "Stored exchanged DSX JWT",
			"spiffe_id", spireSVID.ID.String(),
			"output", *outputPath,
			"token_length", len(exchangeResp.AccessToken))
		return exchangeResp.Expiry, nil
	}

	for {
		expiryTime, refreshErr := refresh(ctx)
		if refreshErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			if currentExpiry.IsZero() || !currentExpiry.After(time.Now()) {
				if err := invalidateToken(&ready, *outputPath); err != nil {
					return errors.Join(fmt.Errorf("refresh JWT: %w", refreshErr), err)
				}
			}
			slog.WarnContext(ctx, "Unable to refresh DSX JWT", "error", refreshErr)
			if err := wait(ctx, time.Second); err != nil {
				return nil
			}
			continue
		}

		if !ready.Swap(true) {
			slog.InfoContext(ctx, "Health check is ready")
		}
		currentExpiry = expiryTime

		// Refresh after 80% of the remaining DSX JWT lifetime.
		issuedAt := time.Now()
		refreshInterval := calculateRefreshTime(issuedAt, expiryTime)

		slog.InfoContext(ctx, "Scheduled DSX JWT refresh", "expires_at", expiryTime, "refresh_in", refreshInterval)

		// Wait until it's time to refresh
		if err := wait(ctx, refreshInterval); err != nil {
			return nil
		}
	}
}

func main() {
	if err := run(); err != nil {
		slog.Error("DSX token fetcher failed", "error", err)
		os.Exit(1)
	}
}
