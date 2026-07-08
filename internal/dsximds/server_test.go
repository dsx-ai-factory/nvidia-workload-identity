package dsximds

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/identity"
)

func TestAgentServerIntegration(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newRemoteVerifier(context.Background(), jwksServer(t, key).URL, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signedHostToken := signToken(t, key, jwt.Claims{
		Issuer:   identity.Issuer,
		Subject:  "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/latest/meta-data/identity" || r.URL.Query().Get("aud") != "spire-server" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Metadata") != "true" {
			t.Errorf("Metadata = %q", r.Header.Get("Metadata"))
		}
		_, _ = w.Write([]byte(signedHostToken))
	}))
	t.Cleanup(imds.Close)

	attestationData, err := FetchHostToken(context.Background(), imds.Client(), imds.URL, "spire-server")
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := verifier.Verify(context.Background(), attestationData)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := agentID.String(), "spiffe://k2.internal/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/host-7"; got != want {
		t.Fatalf("agent ID = %q, want %q", got, want)
	}
}

func TestVerifyHostAttestation(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		issuer   string
		subject  string
		audience jwt.Audience
		expiry   time.Time
		want     string
		wantErr  bool
	}{
		{
			name:     "base host",
			issuer:   identity.Issuer,
			subject:  "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7",
			audience: jwt.Audience{"spire-server"},
			expiry:   time.Now().Add(time.Minute),
			want:     "spiffe://k2.internal/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/host-7",
		},
		{name: "wrong issuer", issuer: "spiffe://wrong.example", subject: "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "wrong audience", issuer: identity.Issuer, subject: "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7", audience: jwt.Audience{"wrong"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "extra audience", issuer: identity.Issuer, subject: "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7", audience: jwt.Audience{"spire-server", "extra"}, expiry: time.Now().Add(time.Minute), want: "spiffe://k2.internal/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/host-7"},
		{name: "expired", issuer: identity.Issuer, subject: "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(-time.Minute), wantErr: true},
		{name: "VM", issuer: identity.Issuer, subject: "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/vm/vm-7", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "host process", issuer: identity.Issuer, subject: "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7/process/agent", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			token := signToken(t, key, jwt.Claims{
				Issuer:   test.issuer,
				Subject:  test.subject,
				Audience: test.audience,
				Expiry:   jwt.NewNumericDate(test.expiry),
			})
			got, err := verifier.Verify(context.Background(), token)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got.String() != test.want {
				t.Fatalf("agent ID = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRemoteVerifierRejectsWrongSignature(t *testing.T) {
	t.Parallel()

	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := jwksServer(t, wrongKey)
	verifier, err := newRemoteVerifier(context.Background(), server.URL, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	token := signToken(t, signingKey, jwt.Claims{
		Issuer:   identity.Issuer,
		Subject:  "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("expected wrong signature to fail")
	}
}

func TestRemoteVerifierRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	_, err := newRemoteVerifier(context.Background(), "file:///tmp/jwks", "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), time.Second)
	if err == nil {
		t.Fatal("expected non-HTTP JWKS URL to fail")
	}
}

func TestRemoteHTTPClientIgnoresAmbientProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyHits.Add(1)
	}))
	t.Cleanup(proxy.Close)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := newRemoteHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("transport = %#v, want proxy-free *http.Transport", client.Transport)
	}
	dialer := new(net.Dialer)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		switch address {
		case "jwks.test:80":
			return dialer.DialContext(ctx, network, target.Listener.Addr().String())
		case proxy.Listener.Addr().String():
			return dialer.DialContext(ctx, network, address)
		default:
			return nil, fmt.Errorf("unexpected dial address %q", address)
		}
	}
	t.Cleanup(transport.CloseIdleConnections)

	response, err := client.Get("http://jwks.test")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %s", response.Status)
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("ambient proxy received %d requests", proxyHits.Load())
	}
}

func TestRemoteVerifierTimeoutRecovers(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	firstRequestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			<-r.Context().Done()
			close(firstRequestDone)
			return
		}
		writeJWKS(t, w, key)
	}))
	t.Cleanup(server.Close)

	verifier, err := newRemoteVerifier(context.Background(), server.URL, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	token := signToken(t, key, jwt.Claims{
		Issuer:   identity.Issuer,
		Subject:  "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("expected initial JWKS request to time out")
	}
	select {
	case <-firstRequestDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the canceled JWKS request")
	}
	got, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("remote verifier did not recover: %v", err)
	}
	if got.String() != "spiffe://k2.internal/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/host-7" {
		t.Fatalf("agent ID = %q", got)
	}
	if requests.Load() < 2 {
		t.Fatalf("JWKS requests = %d, want at least 2", requests.Load())
	}
}

func jwksServer(t *testing.T, key *ecdsa.PrivateKey) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJWKS(t, w, key)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeJWKS(t *testing.T, w http.ResponseWriter, key *ecdsa.PrivateKey) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &key.PublicKey,
		KeyID:     "test-key",
		Algorithm: string(testSignatureAlgorithm(t, key)),
		Use:       "sig",
	}}}); err != nil {
		t.Error(err)
	}
}

func signToken(t *testing.T, key *ecdsa.PrivateKey, claims jwt.Claims) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: testSignatureAlgorithm(t, key), Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func testSignatureAlgorithm(t *testing.T, key *ecdsa.PrivateKey) jose.SignatureAlgorithm {
	t.Helper()
	switch key.Curve {
	case elliptic.P256():
		return jose.ES256
	case elliptic.P384():
		return jose.ES384
	case elliptic.P521():
		return jose.ES512
	default:
		t.Fatalf("unsupported test curve %q", key.Curve.Params().Name)
		return ""
	}
}
