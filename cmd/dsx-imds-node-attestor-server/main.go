package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/hashicorp/hcl"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/spire-plugin-sdk/pluginmain"
	serverv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"dsx-identity/internal/dsximds"
	"dsx-identity/internal/telemetry"
)

type Config struct {
	JWKSURL  string `hcl:"jwks_url"`
	Issuer   string `hcl:"issuer"`
	Audience string `hcl:"audience"`
}

type Plugin struct {
	serverv1.UnimplementedNodeAttestorServer
	configv1.UnimplementedConfigServer

	// Configure and Attest may run concurrently. Replace the complete verifier
	// atomically so in-flight attestations keep a consistent trust configuration.
	verifier atomic.Pointer[dsximds.Verifier]
}

func (p *Plugin) Configure(_ context.Context, req *configv1.ConfigureRequest) (*configv1.ConfigureResponse, error) {
	config := new(Config)
	if err := hcl.Decode(config, req.HclConfiguration); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode configuration: %v", err)
	}
	core := req.GetCoreConfiguration()
	if core == nil {
		return nil, status.Error(codes.InvalidArgument, "core configuration is missing the trust domain")
	}
	trustDomain, err := spiffeid.TrustDomainFromString(core.GetTrustDomain())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid trust domain: %v", err)
	}
	// The verifier and its JWKS cache outlive this Configure RPC.
	verifier, err := dsximds.NewRemoteVerifier(context.Background(), config.Issuer, config.Audience, trustDomain, config.JWKSURL)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid configuration: %v", err)
	}

	p.verifier.Store(verifier)
	return &configv1.ConfigureResponse{}, nil
}

func (p *Plugin) Attest(stream serverv1.NodeAttestor_AttestServer) error {
	verifier := p.verifier.Load()
	if verifier == nil {
		return status.Error(codes.FailedPrecondition, "not configured")
	}

	req, err := stream.Recv()
	if err != nil {
		return err
	}
	payload := req.GetPayload()
	if len(payload) == 0 {
		return status.Error(codes.InvalidArgument, "attestation payload is required")
	}
	agentID, err := verifier.Verify(stream.Context(), string(payload))
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "invalid host identity: %v", err)
	}
	return stream.Send(&serverv1.AttestResponse{
		Response: &serverv1.AttestResponse_AgentAttributes{
			AgentAttributes: &serverv1.AgentAttributes{
				SpiffeId:    agentID.String(),
				CanReattest: true,
			},
		},
	})
}

func run() error {
	var logLevel slog.Level
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	flag.Parse()

	if _, err := telemetry.Setup(context.Background(), "dsx-imds-node-attestor-server", os.Stderr, logLevel); err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}

	plugin := new(Plugin)
	pluginmain.Serve(
		serverv1.NodeAttestorPluginServer(plugin),
		configv1.ConfigServiceServer(plugin),
	)
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("DSX IMDS node attestor server failed", "error", err)
		os.Exit(1)
	}
}
