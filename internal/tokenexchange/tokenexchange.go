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

func Exchange(ctx context.Context, client *http.Client, endpoint, subjectToken string) (*oauth2.Token, error) {
	config := &oauth2.Config{Endpoint: oauth2.Endpoint{
		TokenURL:  endpoint,
		AuthStyle: oauth2.AuthStyleInParams,
	}}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	// Use Config.Exchange for RFC 8693 while retaining its HTTP and response handling.
	return config.Exchange(ctx, "",
		oauth2.SetAuthURLParam("grant_type", grantType),
		oauth2.SetAuthURLParam("subject_token", subjectToken),
		oauth2.SetAuthURLParam("subject_token_type", tokenType),
	)
}
