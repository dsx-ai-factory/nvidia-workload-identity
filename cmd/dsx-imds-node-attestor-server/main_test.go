package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	serverv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testIssuer = "https://ds9.dsx.nvid.id"

func TestAttest(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.ES256), Use: "sig"}
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	}))
	t.Cleanup(jwksServer.Close)

	plugin := new(Plugin)
	if err := plugin.Attest(&fakeAttestStream{ctx: context.Background()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unconfigured error = %v", err)
	}
	if _, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{
		HclConfiguration:  "jwks_url = \"" + jwksServer.URL + "\"\nissuer = \"" + testIssuer + "\"\naudience = \"spire-server\"",
		CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "k2.internal"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Attest(&fakeAttestStream{ctx: context.Background(), request: &serverv1.AttestRequest{}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing payload error = %v", err)
	}
	token := signAttestationToken(t, key)
	stream := &fakeAttestStream{
		ctx: context.Background(),
		request: &serverv1.AttestRequest{Request: &serverv1.AttestRequest_Payload{
			Payload: []byte(token),
		}},
	}
	if err := plugin.Attest(stream); err != nil {
		t.Fatal(err)
	}
	if stream.sent == nil {
		t.Fatal("plugin sent no response")
	}
	attributes := stream.sent.GetAgentAttributes()
	if attributes == nil || attributes.SpiffeId != "spiffe://k2.internal/spire/agent/dsx_imds/tenant/operator-1/bm/host-7" || !attributes.CanReattest {
		t.Fatalf("agent attributes = %#v", attributes)
	}
}

func TestConfigureRejectsMissingAudience(t *testing.T) {
	t.Parallel()
	plugin := new(Plugin)
	_, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{
		HclConfiguration:  `jwks_url = "http://127.0.0.1/jwks"` + "\n" + `issuer = "` + testIssuer + `"`,
		CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "k2.internal"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

func TestConfigureRejectsMissingCoreTrustDomain(t *testing.T) {
	t.Parallel()
	plugin := new(Plugin)
	_, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{
		HclConfiguration: `jwks_url = "http://127.0.0.1/jwks"` + "\n" + `issuer = "` + testIssuer + `"` + "\n" + `audience = "spire-server"`,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

func signAttestationToken(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

type fakeAttestStream struct {
	ctx     context.Context
	request *serverv1.AttestRequest
	sent    *serverv1.AttestResponse
}

func (s *fakeAttestStream) Context() context.Context   { return s.ctx }
func (*fakeAttestStream) SetHeader(metadata.MD) error  { return nil }
func (*fakeAttestStream) SendHeader(metadata.MD) error { return nil }
func (*fakeAttestStream) SetTrailer(metadata.MD)       {}
func (*fakeAttestStream) SendMsg(any) error            { return nil }
func (*fakeAttestStream) RecvMsg(any) error            { return io.EOF }
func (s *fakeAttestStream) Recv() (*serverv1.AttestRequest, error) {
	if s.request == nil {
		return nil, io.EOF
	}
	request := s.request
	s.request = nil
	return request, nil
}
func (s *fakeAttestStream) Send(response *serverv1.AttestResponse) error {
	s.sent = response
	return nil
}
