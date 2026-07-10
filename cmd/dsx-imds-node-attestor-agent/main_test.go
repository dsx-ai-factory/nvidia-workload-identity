package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"dsx-identity/internal/dsximds"
)

func TestAidAttestation(t *testing.T) {
	t.Parallel()

	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata") != "true" || r.URL.Query().Get("aud") != "spire-server" {
			t.Errorf("unexpected IMDS request: headers=%v query=%v", r.Header, r.URL.Query())
		}
		_, _ = w.Write([]byte("signed-host-token"))
	}))
	t.Cleanup(metadataServer.Close)

	plugin := &Plugin{client: metadataServer.Client()}
	if err := plugin.AidAttestation(&fakeAgentStream{ctx: context.Background()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unconfigured error = %v", err)
	}
	if _, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{
		HclConfiguration: "endpoint = \"" + metadataServer.URL + "\"\naudience = \"spire-server\"",
	}); err != nil {
		t.Fatal(err)
	}
	stream := &fakeAgentStream{ctx: context.Background()}
	if err := plugin.AidAttestation(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || string(stream.sent[0].GetPayload()) != "signed-host-token" {
		t.Fatalf("payloads = %#v", stream.sent)
	}
}

func TestConfigureRejectsMissingAudience(t *testing.T) {
	t.Parallel()
	plugin := &Plugin{client: http.DefaultClient}
	_, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: `endpoint = "http://169.254.169.254"`})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

func TestConfigureUsesDefaultForOmittedEndpoint(t *testing.T) {
	t.Parallel()
	plugin := &Plugin{client: http.DefaultClient}
	if _, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: `audience = "spire-server"`}); err != nil {
		t.Fatal(err)
	}
	if got := plugin.config.Load().Endpoint; got != defaultEndpoint {
		t.Fatalf("endpoint = %q, want %q", got, defaultEndpoint)
	}
}

func TestConfigureRejectsInvalidEndpoint(t *testing.T) {
	t.Parallel()
	for _, configuration := range []string{
		"endpoint = \"\"\naudience = \"spire-server\"",
		"endpoint = \"http://169.254.169.254/\"\naudience = \"spire-server\"",
	} {
		plugin := &Plugin{client: http.DefaultClient}
		if _, err := plugin.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: configuration}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("configuration %q error = %v, want InvalidArgument", configuration, err)
		}
	}
}

func TestIMDSHTTPClientIgnoresAmbientProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyHits.Add(1)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	client := newIMDSHTTPClient(time.Second)
	if _, err := dsximds.FetchHostToken(context.Background(), client, "http://imds.proxy-check.invalid", "spire-server"); err == nil {
		t.Fatal("request to an unresolvable direct endpoint succeeded")
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("ambient proxy received %d requests", proxyHits.Load())
	}
}

func TestIMDSHTTPClientRejectsRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("redirected-token"))
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	_, err := dsximds.FetchHostToken(context.Background(), newIMDSHTTPClient(time.Second), redirect.URL, "spire-server")
	if err == nil {
		t.Fatal("expected redirecting IMDS endpoint to fail")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetHits.Load())
	}
}

type fakeAgentStream struct {
	ctx  context.Context
	sent []*agentv1.PayloadOrChallengeResponse
}

func (s *fakeAgentStream) Context() context.Context   { return s.ctx }
func (*fakeAgentStream) SetHeader(metadata.MD) error  { return nil }
func (*fakeAgentStream) SendHeader(metadata.MD) error { return nil }
func (*fakeAgentStream) SetTrailer(metadata.MD)       {}
func (*fakeAgentStream) SendMsg(any) error            { return nil }
func (*fakeAgentStream) RecvMsg(any) error            { return io.EOF }
func (*fakeAgentStream) Recv() (*agentv1.Challenge, error) {
	return nil, io.EOF
}
func (s *fakeAgentStream) Send(response *agentv1.PayloadOrChallengeResponse) error {
	s.sent = append(s.sent, response)
	return nil
}
