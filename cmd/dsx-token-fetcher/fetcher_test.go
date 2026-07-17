package main

import (
	"context"
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
}

func TestLiveFetchSPIREJWTSVID(t *testing.T) {
	endpoint := os.Getenv("DSX_E2E_SPIFFE_ENDPOINT_SOCKET")
	if endpoint == "" {
		t.Skip("DSX_E2E_SPIFFE_ENDPOINT_SOCKET is not set")
	}
	audience := os.Getenv("DSX_E2E_SPIRE_JWT_AUDIENCE")
	expectedID := os.Getenv("DSX_E2E_SPIRE_JWT_ID")
	expectedHint := os.Getenv("DSX_E2E_SPIRE_JWT_HINT")
	if audience == "" || expectedID == "" || expectedHint == "" {
		t.Fatal("DSX_E2E_SPIRE_JWT_AUDIENCE, DSX_E2E_SPIRE_JWT_ID, and DSX_E2E_SPIRE_JWT_HINT are required")
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
	if match.Hint != expectedHint {
		t.Fatalf("JWT-SVID hint = %q, want %q", match.Hint, expectedHint)
	}
	fmt.Printf("SPIRE_JWT=%s\n", match.Marshal())
}

func (s fakeJWTSource) FetchJWTSVIDs(context.Context, jwtsvid.Params) ([]*jwtsvid.SVID, error) {
	return s.svids, s.err
}

func (s fakeJWTSource) FetchJWTSVID(context.Context, jwtsvid.Params) (*jwtsvid.SVID, error) {
	return s.defaultSVID, s.err
}

func TestCalculateRefreshTime(t *testing.T) {
	t.Parallel()
	issuedAt := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name     string
		lifetime time.Duration
		want     time.Duration
	}{
		{name: "one hour", lifetime: time.Hour, want: 48 * time.Minute},
		{name: "five minutes", lifetime: 5 * time.Minute, want: 4 * time.Minute},
		{name: "minimum", lifetime: 35 * time.Second, want: 30 * time.Second},
		{name: "below minimum", lifetime: 20 * time.Second, want: 16 * time.Second},
		{name: "one second", lifetime: time.Second, want: 800 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateRefreshTime(issuedAt, issuedAt.Add(tt.lifetime))
			if got != tt.want {
				t.Errorf("got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFetchJWTSVIDSelectsExactHint(t *testing.T) {
	t.Parallel()

	want := &jwtsvid.SVID{Hint: "dsx-dpu"}
	got, err := fetchJWTSVID(context.Background(), fakeJWTSource{
		svids: []*jwtsvid.SVID{{Hint: "other"}, want},
	}, "audience", "dsx-dpu")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %p, want %p", got, want)
	}
}

func TestFetchJWTSVIDUsesSourceDefaultWithoutHint(t *testing.T) {
	t.Parallel()
	want := &jwtsvid.SVID{Hint: "default"}
	got, err := fetchJWTSVID(context.Background(), fakeJWTSource{
		defaultSVID: want,
		svids:       []*jwtsvid.SVID{{Hint: "other"}},
	}, "audience", "")
	if err != nil || got != want {
		t.Fatalf("selection = %p, %v; want %p", got, err, want)
	}
}

func TestWriteTokenAtomically(t *testing.T) {
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
