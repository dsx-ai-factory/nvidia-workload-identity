package tokenexchange

import (
	"context"
	"net/http"

	"golang.org/x/oauth2"
)

const (
	grantType = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenType = "urn:ietf:params:oauth:token-type:jwt"
)

// Exchange submits a JWT through the project's RFC 8693 token exchange.
func Exchange(ctx context.Context, client *http.Client, endpoint, subjectToken string) (*oauth2.Token, error) {
	config := &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: endpoint}}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	return config.Exchange(ctx, "",
		oauth2.SetAuthURLParam("grant_type", grantType),
		oauth2.SetAuthURLParam("subject_token", subjectToken),
		oauth2.SetAuthURLParam("subject_token_type", tokenType),
	)
}
