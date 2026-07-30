package dpu_test

import (
	"context"
	"slices"
	"testing"

	"dsx-identity/internal/dpu"

	"github.com/spiffe/spire-plugin-sdk/pluginsdk"
	"github.com/spiffe/spire-plugin-sdk/plugintest"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testTrustDomain = "example.org"

func start(t *testing.T) (nodeattestorv1.NodeAttestorClient, configv1.ConfigClient) {
	t.Helper()
	plugin := dpu.New()
	naClient := new(nodeattestorv1.NodeAttestorPluginClient)
	cfgClient := new(configv1.ConfigServiceClient)
	plugintest.ServeInBackground(t, plugintest.Config{
		PluginServer:   nodeattestorv1.NodeAttestorPluginServer(plugin),
		PluginClient:   naClient,
		ServiceServers: []pluginsdk.ServiceServer{configv1.ConfigServiceServer(plugin)},
		ServiceClients: []pluginsdk.ServiceClient{cfgClient},
	})
	if !naClient.IsInitialized() {
		t.Fatal("node attestor client not initialized")
	}
	if !cfgClient.IsInitialized() {
		t.Fatal("config client not initialized")
	}
	return naClient, cfgClient
}

func configure(ctx context.Context, t *testing.T, cfg configv1.ConfigClient, trustDomain string) error {
	t.Helper()
	_, err := cfg.Configure(ctx, &configv1.ConfigureRequest{
		CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: trustDomain},
	})
	return err
}

func attest(ctx context.Context, t *testing.T, na nodeattestorv1.NodeAttestorClient, payload []byte) (*nodeattestorv1.AgentAttributes, error) {
	t.Helper()
	stream, err := na.Attest(ctx)
	if err != nil {
		t.Fatalf("Attest stream: %v", err)
	}
	// The server may return before reading. Ignore Send errors and let the
	// terminal status surface on Recv.
	_ = stream.Send(&nodeattestorv1.AttestRequest{
		Request: &nodeattestorv1.AttestRequest_Payload{Payload: payload},
	})
	_ = stream.CloseSend()
	resp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	return resp.GetAgentAttributes(), nil
}

func TestConfigureThenAttest(t *testing.T) {
	ctx := context.Background()
	na, cfg := start(t)

	if err := configure(ctx, t, cfg, testTrustDomain); err != nil {
		t.Fatalf("configure: %v", err)
	}

	attrs, err := attest(ctx, t, na, []byte("MT2152X00ABC"))
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if attrs == nil {
		t.Fatal("expected agent attributes")
	}
	wantID := "spiffe://example.org/spire/agent/dpu_hw/mt2152x00abc"
	if attrs.GetSpiffeId() != wantID {
		t.Fatalf("SpiffeId = %q, want %q", attrs.GetSpiffeId(), wantID)
	}
	if !attrs.GetCanReattest() {
		t.Fatal("CanReattest = false, want true")
	}
	if !slices.Contains(attrs.GetSelectorValues(), "serial:mt2152x00abc") {
		t.Fatalf("SelectorValues = %v, want serial:mt2152x00abc", attrs.GetSelectorValues())
	}
}

func TestAttestBeforeConfigureFails(t *testing.T) {
	ctx := context.Background()
	na, _ := start(t)

	_, err := attest(ctx, t, na, []byte("MT2152X00ABC"))
	if err == nil {
		t.Fatal("expected error before configure")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestAttestInvalidSerialFails(t *testing.T) {
	ctx := context.Background()
	na, cfg := start(t)
	if err := configure(ctx, t, cfg, testTrustDomain); err != nil {
		t.Fatalf("configure: %v", err)
	}

	_, err := attest(ctx, t, na, []byte("bad serial"))
	if err == nil {
		t.Fatal("expected invalid serial error")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestAttestEmptyPayloadFails(t *testing.T) {
	ctx := context.Background()
	na, cfg := start(t)
	if err := configure(ctx, t, cfg, testTrustDomain); err != nil {
		t.Fatalf("configure: %v", err)
	}

	_, err := attest(ctx, t, na, []byte{})
	if err == nil {
		t.Fatal("expected empty payload error")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestConfigureRequiresTrustDomain(t *testing.T) {
	ctx := context.Background()
	_, cfg := start(t)

	err := configure(ctx, t, cfg, "")
	if err == nil {
		t.Fatal("expected empty trust domain error")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestConfigureRejectsInvalidTrustDomain(t *testing.T) {
	ctx := context.Background()
	_, cfg := start(t)

	err := configure(ctx, t, cfg, "example.org/foo")
	if err == nil {
		t.Fatal("expected invalid trust domain error")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestValidate(t *testing.T) {
	ctx := context.Background()
	_, cfg := start(t)

	t.Run("valid when trust domain is present", func(t *testing.T) {
		resp, err := cfg.Validate(ctx, &configv1.ValidateRequest{
			CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: testTrustDomain},
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if !resp.GetValid() {
			t.Fatalf("Valid = false, notes = %v", resp.GetNotes())
		}
		if len(resp.GetNotes()) != 0 {
			t.Fatalf("Notes = %v, want empty", resp.GetNotes())
		}
	})

	t.Run("invalid with a note when trust domain is missing", func(t *testing.T) {
		resp, err := cfg.Validate(ctx, &configv1.ValidateRequest{
			CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: ""},
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if resp.GetValid() {
			t.Fatal("Valid = true, want false")
		}
		if len(resp.GetNotes()) == 0 {
			t.Fatal("expected notes")
		}
	})

	t.Run("invalid with a note when trust domain is malformed", func(t *testing.T) {
		resp, err := cfg.Validate(ctx, &configv1.ValidateRequest{
			CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "example.org/foo"},
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if resp.GetValid() {
			t.Fatal("Valid = true, want false")
		}
		if len(resp.GetNotes()) == 0 {
			t.Fatal("expected notes")
		}
	})
}

func TestNewWithVerifierNilDefaults(t *testing.T) {
	if dpu.NewWithVerifier(nil) == nil {
		t.Fatal("NewWithVerifier(nil) returned nil")
	}
}
