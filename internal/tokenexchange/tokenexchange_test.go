package tokenexchange

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestExchangeUsesTokenExchangeGrant(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected client authentication: %q", r.Header.Get("Authorization"))
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != grantType ||
			r.Form.Get("subject_token") != "local-svid" ||
			r.Form.Get("subject_token_type") != tokenType {
			t.Errorf("unexpected token exchange form: %v", r.Form)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"exchanged","token_type":"Bearer","expires_in":60}`)),
		}, nil
	})}

	token, err := Exchange(t.Context(), client, "https://exchange.example/v1/exchange", "local-svid")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "exchanged" || !token.Expiry.After(time.Now()) {
		t.Fatalf("token = %#v", token)
	}
}
