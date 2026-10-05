package keydistribution

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nats-io/nats.go"
	"golang.org/x/oauth2"
)

func TestNATSOAuthOptionsUseNativeToken(t *testing.T) {
	var requests atomic.Int32
	issuerURL := testOAuthIssuer(t, "native-nats-token", func(r *http.Request) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok || clientID != "identity" || clientSecret != "secret" || r.FormValue("grant_type") != "client_credentials" || r.FormValue("scope") != "nats" {
			t.Errorf("unexpected token request: client_id=%q grant_type=%q scope=%q", clientID, r.FormValue("grant_type"), r.FormValue("scope"))
		}
	})
	credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"client_id":"identity","client_secret":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := (AuthConfig{
		Mode: AuthOAuth2, CredentialsFile: credentialsPath, OAuthIssuerURL: issuerURL, OAuthScopes: []string{"nats"},
	}).options(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	configured := nats.GetDefaultOptions()
	for _, option := range options {
		if err := option(&configured); err != nil {
			t.Fatal(err)
		}
	}
	if configured.TokenHandler == nil || configured.User != "" || configured.Password != "" || configured.UserInfo != nil || configured.Token != "" {
		t.Fatal("OAuth2 did not configure native token-only NATS authentication")
	}
	if got := configured.TokenHandler(); got != "native-nats-token" {
		t.Fatalf("NATS token = %q, want native-nats-token", got)
	}
	if got := configured.TokenHandler(); got != "native-nats-token" || requests.Load() != 1 {
		t.Fatalf("cached NATS token = %q, requests = %d", got, requests.Load())
	}
}

func TestNATSAuthOptions(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		options, err := (AuthConfig{Mode: AuthNone}).options(t.Context())
		if err != nil || len(options) != 0 {
			t.Fatalf("none options = %d, error = %v", len(options), err)
		}
	})
	t.Run("oauth2 missing configuration", func(t *testing.T) {
		if _, err := (AuthConfig{Mode: AuthOAuth2}).options(t.Context()); err == nil {
			t.Fatal("incomplete OAuth2 configuration was accepted")
		}
	})
	t.Run("oauth2 invalid credentials", func(t *testing.T) {
		credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(credentialsPath, []byte(`{"client_id":"","client_secret":""}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (AuthConfig{
			Mode: AuthOAuth2, CredentialsFile: credentialsPath, OAuthIssuerURL: "https://example.invalid",
		}).options(t.Context()); err == nil {
			t.Fatal("empty OAuth2 credentials were accepted")
		}
	})
	t.Run("invalid", func(t *testing.T) {
		if _, err := (AuthConfig{Mode: "invalid"}).options(t.Context()); err == nil {
			t.Fatal("invalid NATS auth mode was accepted")
		}
	})
	t.Run("basic", func(t *testing.T) {
		if _, err := (AuthConfig{Mode: "basic"}).options(t.Context()); err == nil {
			t.Fatal("basic NATS authentication was accepted")
		}
	})
	t.Run("oauth2 token failure", func(t *testing.T) {
		handler := natsOAuthTokenHandler(oauth2.StaticTokenSource(&oauth2.Token{}))
		if token := handler(); token != natsOAuthRejectedToken {
			t.Fatalf("rejected NATS token = %q", token)
		}
	})
}

func testNATSOAuthConfig(t *testing.T, token string) AuthConfig {
	t.Helper()
	credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"client_id":"identity","client_secret":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return AuthConfig{Mode: AuthOAuth2, CredentialsFile: credentialsPath, OAuthIssuerURL: testOAuthIssuer(t, token, nil), OAuthScopes: []string{"nats"}}
}

func testOAuthIssuer(t *testing.T, token string, inspectTokenRequest func(*http.Request)) string {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"response_types_supported":["code"],"subject_types_supported":["public"],"id_token_signing_alg_values_supported":["RS256"]}`,
			server.URL, server.URL+"/authorize", server.URL+"/token", server.URL+"/jwks")
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if inspectTokenRequest != nil {
			inspectTokenRequest(r)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, token)
	})
	return server.URL
}

var _ oauth2.TokenSource = natsOAuthTokenSource{}
