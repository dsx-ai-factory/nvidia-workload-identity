package keydistribution

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	AuthNone               = "none"
	AuthOAuth2             = "oauth2"
	natsOAuthRejectedToken = "invalid-oauth-token"
)

type AuthConfig struct {
	Mode            string
	CredentialsFile string
	OAuthIssuerURL  string
	OAuthScopes     []string
}

func (c AuthConfig) options(ctx context.Context) ([]nats.Option, error) {
	switch c.Mode {
	case AuthNone:
		return nil, nil
	case AuthOAuth2:
		source, err := c.oauthTokenSource(ctx)
		if err != nil {
			return nil, err
		}
		return []nats.Option{nats.TokenHandler(natsOAuthTokenHandler(source))}, nil
	default:
		return nil, fmt.Errorf("NATS auth mode %q must be none or oauth2", c.Mode)
	}
}

func (c AuthConfig) oauthTokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	if c.OAuthIssuerURL == "" || c.CredentialsFile == "" {
		return nil, fmt.Errorf("NATS OAuth2 issuer URL and credentials file are required")
	}
	if _, err := readNATSOAuthCredentials(c.CredentialsFile); err != nil {
		return nil, err
	}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	})
	provider, err := oidc.NewProvider(tokenContext, c.OAuthIssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover NATS OAuth2 issuer: %w", err)
	}
	return oauth2.ReuseTokenSource(nil, natsOAuthTokenSource{
		ctx: tokenContext, tokenURL: provider.Endpoint().TokenURL, scopes: slices.Clone(c.OAuthScopes),
		credentialsFile: c.CredentialsFile,
	}), nil
}

type natsOAuthTokenSource struct {
	ctx             context.Context
	tokenURL        string
	scopes          []string
	credentialsFile string
}

func (s natsOAuthTokenSource) Token() (*oauth2.Token, error) {
	credentials, err := readNATSOAuthCredentials(s.credentialsFile)
	if err != nil {
		return nil, err
	}
	return (&clientcredentials.Config{
		ClientID: credentials.ClientID, ClientSecret: credentials.ClientSecret,
		TokenURL: s.tokenURL, Scopes: slices.Clone(s.scopes),
	}).Token(s.ctx)
}

func natsOAuthTokenHandler(source oauth2.TokenSource) nats.AuthTokenHandler {
	return func() string {
		token, err := source.Token()
		if err != nil || !token.Valid() {
			slog.Error("Failed to obtain NATS OAuth2 token", "error", err)
			return natsOAuthRejectedToken
		}
		return token.AccessToken
	}
}

type natsOAuthCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func readNATSOAuthCredentials(path string) (natsOAuthCredentials, error) {
	credentialBytes, err := os.ReadFile(path)
	if err != nil {
		return natsOAuthCredentials{}, fmt.Errorf("read NATS OAuth2 credentials: %w", err)
	}
	var credentials natsOAuthCredentials
	if err := json.Unmarshal(credentialBytes, &credentials); err != nil {
		return natsOAuthCredentials{}, fmt.Errorf("parse NATS OAuth2 credentials: %w", err)
	}
	if credentials.ClientID == "" || credentials.ClientSecret == "" {
		return natsOAuthCredentials{}, fmt.Errorf("NATS OAuth2 client_id and client_secret must be non-empty")
	}
	return credentials, nil
}
