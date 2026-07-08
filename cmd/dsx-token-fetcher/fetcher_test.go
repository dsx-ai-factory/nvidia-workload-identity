package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

type fakeJWTSource struct {
	defaultSVID *jwtsvid.SVID
	svids       []*jwtsvid.SVID
	err         error
	fetch       func(context.Context, jwtsvid.Params) ([]*jwtsvid.SVID, error)
}

func TestLiveFetchSPIREJWTSVID(t *testing.T) {
	endpoint := os.Getenv("DSX_E2E_SPIFFE_ENDPOINT_SOCKET")
	if endpoint == "" {
		t.Skip("DSX_E2E_SPIFFE_ENDPOINT_SOCKET is not set")
	}
	audience := os.Getenv("DSX_E2E_SPIRE_JWT_AUDIENCE")
	expectedID := os.Getenv("DSX_E2E_SPIRE_JWT_ID")
	if audience == "" || expectedID == "" {
		t.Fatal("DSX_E2E_SPIRE_JWT_AUDIENCE and DSX_E2E_SPIRE_JWT_ID are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := workloadapi.New(ctx, workloadapi.WithAddr(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	svids, err := client.FetchJWTSVIDs(ctx, jwtsvid.Params{Audience: audience})
	if err != nil {
		t.Fatal(err)
	}
	var match *jwtsvid.SVID
	for _, svid := range svids {
		if svid != nil && svid.ID.String() == expectedID {
			if match != nil {
				t.Fatalf("multiple JWT-SVIDs matched %s", expectedID)
			}
			match = svid
		}
	}
	if match == nil {
		t.Fatalf("no JWT-SVID matched %s", expectedID)
	}
	fmt.Printf("SPIRE_JWT=%s\n", match.Marshal())
}

func (s fakeJWTSource) FetchJWTSVIDs(ctx context.Context, params jwtsvid.Params) ([]*jwtsvid.SVID, error) {
	if s.fetch != nil {
		return s.fetch(ctx, params)
	}
	return s.svids, s.err
}

func (s fakeJWTSource) FetchJWTSVID(context.Context, jwtsvid.Params) (*jwtsvid.SVID, error) {
	return s.defaultSVID, s.err
}

func TestParseSocketPath(t *testing.T) {
	tests := []struct {
		name       string
		socketPath string
		wantPath   string
		wantErr    bool
	}{
		{
			name:       "valid unix socket",
			socketPath: "unix:///var/run/spire.sock",
			wantPath:   "/var/run/spire.sock",
			wantErr:    false,
		},
		{
			name:       "valid unix socket nested path",
			socketPath: "unix:///var/run/secrets/spire.io/spire-agent.sock",
			wantPath:   "/var/run/secrets/spire.io/spire-agent.sock",
			wantErr:    false,
		},
		{
			name:       "tcp scheme not supported",
			socketPath: "tcp://127.0.0.1:8080",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "http scheme not supported",
			socketPath: "http://localhost",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "empty path",
			socketPath: "unix://",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "invalid url",
			socketPath: "://invalid",
			wantPath:   "",
			wantErr:    true,
		},
		{name: "authority is rejected", socketPath: "unix://host/var/run/spire.sock", wantErr: true},
		{name: "query is rejected", socketPath: "unix:///var/run/spire.sock?x=1", wantErr: true},
		{name: "unclean path is rejected", socketPath: "unix:///var/run/../spire.sock", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := parseSocketPath(tt.socketPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestCalculateRefreshTime(t *testing.T) {
	tests := []struct {
		name      string
		issuedAt  time.Time
		expiresAt time.Time
		wantMin   time.Duration
		wantMax   time.Duration
	}{
		{
			name:      "1 hour lifetime",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(1 * time.Hour),
			wantMin:   47 * time.Minute, // ~80% of 1 hour
			wantMax:   49 * time.Minute,
		},
		{
			name:      "5 minute lifetime",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(5 * time.Minute),
			wantMin:   3*time.Minute + 50*time.Second, // ~80% of 5 min
			wantMax:   4*time.Minute + 10*time.Second,
		},
		{
			name:      "short lifetime refreshes before expiry",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(20 * time.Second),
			wantMin:   15 * time.Second,
			wantMax:   17 * time.Second,
		},
		{
			name:      "very short lifetime refreshes before expiry",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(10 * time.Second),
			wantMin:   7 * time.Second,
			wantMax:   9 * time.Second,
		},
		{
			name:      "24 hour lifetime",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(24 * time.Hour),
			wantMin:   19*time.Hour + 10*time.Minute, // ~80% of 24 hours
			wantMax:   19*time.Hour + 20*time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateRefreshTime(tt.issuedAt, tt.expiresAt)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("got = %v, want between %v and %v", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCalculateRefreshTime_Exact80Percent(t *testing.T) {
	issuedAt := time.Now()
	expiresAt := issuedAt.Add(100 * time.Second)

	got := calculateRefreshTime(issuedAt, expiresAt)

	// 80% of 100s = 80s, which is > 30s minimum
	expected := 80 * time.Second
	if got != expected {
		t.Errorf("got = %v, want %v", got, expected)
	}
}

func TestCalculateRefreshTime_MinimumEnforced(t *testing.T) {
	issuedAt := time.Now()
	expiresAt := issuedAt.Add(35 * time.Second)

	got := calculateRefreshTime(issuedAt, expiresAt)

	// 80% of 35s = 28s, but minimum is 30s
	expected := 30 * time.Second
	if got != expected {
		t.Errorf("got = %v, want %v (minimum enforced)", got, expected)
	}
}

func TestFetchJWTSVIDWithRetrySelectsExactHint(t *testing.T) {
	t.Parallel()

	want := &jwtsvid.SVID{Hint: "dsx-dpu"}
	got, err := fetchJWTSVIDWithRetry(context.Background(), fakeJWTSource{
		svids: []*jwtsvid.SVID{{Hint: "other"}, want},
	}, "audience", "dsx-dpu", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %p, want %p", got, want)
	}
}

func TestFetchJWTSVIDWithRetryUsesSourceDefaultWithoutHint(t *testing.T) {
	t.Parallel()
	want := &jwtsvid.SVID{Hint: "default"}
	got, err := fetchJWTSVIDWithRetry(context.Background(), fakeJWTSource{
		defaultSVID: want,
		svids:       []*jwtsvid.SVID{{Hint: "other"}},
	}, "audience", "", 1)
	if err != nil || got != want {
		t.Fatalf("selection = %p, %v; want %p", got, err, want)
	}
}

func TestFetchJWTSVIDWithRetryHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := fetchJWTSVIDWithRetry(ctx, fakeJWTSource{err: errors.New("unavailable")}, "audience", "", 3)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("canceled retry blocked")
	}
}

func TestWriteTokenAtomicallyAndSecurely(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "tokens")
	path := filepath.Join(dir, "token")
	if err := writeToken(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeToken(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("token = %q, want second", data)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
}

func TestRefreshDeadlineInvalidatesToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "token")
	if err := writeToken(path, []byte("stale")); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	ready.Store(true)

	expiry := time.Now().Add(50 * time.Millisecond)
	ctx, cancel := refreshContext(context.Background(), expiry)
	defer cancel()
	_, err := fetchJWTSVIDWithRetry(ctx, fakeJWTSource{fetch: func(ctx context.Context, _ jwtsvid.Params) ([]*jwtsvid.SVID, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}, "audience", "dsx-imds", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want deadline exceeded", err)
	}
	retry, err := retryRefreshOrInvalidateToken(context.Background(), &ready, path, expiry, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if retry {
		t.Fatal("expired token requested another refresh attempt")
	}
	if ready.Load() {
		t.Fatal("readiness remained true after refresh deadline")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale token still exists: %v", err)
	}
}

func TestRefreshFailureKeepsValidToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "token")
	if err := writeToken(path, []byte("still-valid")); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	ready.Store(true)

	retry, err := retryRefreshOrInvalidateToken(
		context.Background(),
		&ready,
		path,
		time.Now().Add(time.Second),
		time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !retry {
		t.Fatal("valid token did not request another refresh attempt")
	}
	if !ready.Load() {
		t.Fatal("readiness was cleared while the token remained valid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "still-valid" {
		t.Fatalf("token = %q, want still-valid", data)
	}
}

func TestHealthHandler(t *testing.T) {
	var ready atomic.Bool
	handler := healthHandler(&ready)

	t.Run("not ready returns 503", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
	})

	ready.Store(true)

	t.Run("ready returns 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

func TestStartHealthServerFailsWhenAddressIsInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server, err := startHealthServer(listener.Addr().String(), http.NotFoundHandler())
	if err == nil {
		_ = server.Close()
		t.Fatal("expected occupied health address to fail synchronously")
	}
}
