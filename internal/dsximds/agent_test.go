package dsximds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchHostToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/latest/meta-data/identity" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("aud"); got != "spire-server" {
			t.Errorf("audience = %q", got)
		}
		if got := r.Header.Get("Metadata"); got != "true" {
			t.Errorf("Metadata = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "text/plain" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = w.Write([]byte("signed-token"))
	}))
	defer server.Close()

	token, err := FetchHostToken(context.Background(), server.Client(), server.URL, "spire-server")
	if err != nil {
		t.Fatal(err)
	}
	if token != "signed-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestFetchHostTokenRejectsInvalidResponse(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "HTTP error", status: http.StatusServiceUnavailable, body: "unavailable"},
		{name: "empty", status: http.StatusOK},
		{name: "oversize", status: http.StatusOK, body: strings.Repeat("x", maxTokenSize+1)},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			if _, err := FetchHostToken(context.Background(), server.Client(), server.URL, "audience"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFetchHostTokenRejectsInvalidEndpoint(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		endpoint string
		audience string
	}{
		{endpoint: "http://169.254.169.254/", audience: "spire-server"},
		{endpoint: "http://169.254.169.254?x=1", audience: "spire-server"},
	} {
		if _, err := FetchHostToken(context.Background(), http.DefaultClient, test.endpoint, test.audience); err == nil {
			t.Fatalf("endpoint %q audience %q was accepted", test.endpoint, test.audience)
		}
	}
}
