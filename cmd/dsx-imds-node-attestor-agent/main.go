package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hashicorp/hcl"
	"github.com/spiffe/spire-plugin-sdk/pluginmain"
	agentv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"dsx-identity/internal/dsximds"
	"dsx-identity/internal/stricturl"
)

const defaultEndpoint = "http://169.254.169.254"

func newIMDSHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Contact the configured metadata endpoint directly. Do not proxy or follow redirects.
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type Config struct {
	Endpoint string `hcl:"endpoint"`
	Audience string `hcl:"audience"`
}

type Plugin struct {
	agentv1.UnimplementedNodeAttestorServer
	configv1.UnimplementedConfigServer

	// Configure and AidAttestation may run concurrently. Publish one immutable
	// snapshot so an RPC never observes a partially updated configuration.
	config atomic.Pointer[Config]
	client *http.Client
}

func (p *Plugin) Configure(_ context.Context, req *configv1.ConfigureRequest) (*configv1.ConfigureResponse, error) {
	config := &Config{Endpoint: defaultEndpoint}
	if err := hcl.Decode(config, req.HclConfiguration); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode configuration: %v", err)
	}
	if _, err := stricturl.ParseHTTPOrigin(config.Endpoint); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "endpoint %v", err)
	}
	if config.Audience == "" {
		return nil, status.Error(codes.InvalidArgument, "audience is required")
	}

	p.config.Store(config)
	return &configv1.ConfigureResponse{}, nil
}

func (p *Plugin) AidAttestation(stream agentv1.NodeAttestor_AidAttestationServer) error {
	config := p.config.Load()
	if config == nil {
		return status.Error(codes.FailedPrecondition, "not configured")
	}

	token, err := dsximds.FetchHostToken(stream.Context(), p.client, config.Endpoint, config.Audience)
	if err != nil {
		return status.Errorf(codes.Unavailable, "fetch host identity: %v", err)
	}
	return stream.Send(&agentv1.PayloadOrChallengeResponse{
		Data: &agentv1.PayloadOrChallengeResponse_Payload{Payload: []byte(token)},
	})
}

func main() {
	plugin := &Plugin{client: newIMDSHTTPClient(10 * time.Second)}
	pluginmain.Serve(
		agentv1.NodeAttestorPluginServer(plugin),
		configv1.ConfigServiceServer(plugin),
	)
}
