package identity

import (
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

func TestExchangeSPIFFEID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spiffeID string
		dsxID    string
	}{
		{
			name:     "dpu",
			spiffeID: "spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/dpu/dpu-1",
			dsxID:    "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/dpu/dpu-1",
		},
		{
			name:     "host process with role",
			spiffeID: "spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/host/host-1/process/nke-agent/role/operator",
			dsxID:    "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host/host-1/process/nke-agent/role/operator",
		},
		{
			name:     "dpu pod",
			spiffeID: "spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/dpu/dpu-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
			dsxID:    "spiffe://nca-0000000000000001.ds9.dsx.nvid.id/dpu/dpu-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		},
		{
			name:     "vm with role",
			spiffeID: "spiffe://k2.internal/tenant/nca-1234567890123456/service/dsx/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
			dsxID:    "spiffe://nca-1234567890123456.ds9.dsx.nvid.id/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := spiffeid.RequireFromString(test.spiffeID)
			got, err := ExchangeSPIFFEID(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != test.dsxID {
				t.Fatalf("got %q, want %q", got, test.dsxID)
			}
			parsed, err := ParseDSXID(got)
			if err != nil {
				t.Fatal(err)
			}
			if rebuilt, err := parsed.SPIFFEID(input.TrustDomain()); err != nil || rebuilt != input {
				t.Fatalf("round trip got %q, %v", rebuilt, err)
			}
		})
	}
}

func TestRejectInvalidSPIFFEID(t *testing.T) {
	t.Parallel()

	tests := []string{
		"spiffe://k2.internal/tenant/tenant1/service/dsx/host/host-1",
		"spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/pod/pod-1",
		"spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/host/host-1/process",
		"spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/host/host-1/pod/not-a-uuid",
		"spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/host/host-1/role/BadRole",
		"spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/host/host-1/extra/value",
		"spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/host/%20host",
	}
	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			id, err := spiffeid.FromString(raw)
			if err != nil {
				return
			}
			if _, err := parseSPIFFEID(id); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRejectInvalidDSXID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"spiffe://nca-0000000000000001.sjc1.dsx.nvid.id/host/host-1",
		"spiffe://nca-0000000000000001.ds9.identity.nvid.id/host/host-1",
		"spiffe://nca-0000000000000001.ds9.dsx.nvidia.com/host/host-1",
		"spiffe://tenant1.ds9.dsx.nvid.id/host/host-1",
		"spiffe://nca-0000000000000001.extra.ds9.dsx.nvid.id/host/host-1",
		"spiffe://nca-0000000000000001.ds9.dsx.nvid.id/host",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			id, err := spiffeid.FromString(raw)
			if err != nil {
				return
			}
			if _, err := ParseDSXID(id); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBuildBaseDPUIdentity(t *testing.T) {
	t.Parallel()

	c := Subject{
		Tenant:       "nca-0000000000000001",
		ResourceType: "dpu",
		ResourceID:   "dpu-9e2a4c71",
	}
	spiffeID, err := c.SPIFFEID(spiffeid.RequireTrustDomainFromString("k2.internal"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spiffeID.String(), "spiffe://k2.internal/tenant/nca-0000000000000001/service/dsx/dpu/dpu-9e2a4c71"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
