package identity

import (
	"strings"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

func TestValidTenant(t *testing.T) {
	t.Parallel()

	for _, tenant := range []string{"a", "tenant1", "tenant-2", strings.Repeat("a", 63)} {
		if !ValidTenant(tenant) {
			t.Errorf("ValidTenant(%q) = false", tenant)
		}
	}
	for _, tenant := range []string{"", "-tenant", "tenant-", "Tenant", "tenant_1", strings.Repeat("a", 64)} {
		if ValidTenant(tenant) {
			t.Errorf("ValidTenant(%q) = true", tenant)
		}
	}
}

func TestToDSXSPIFFEID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spiffeID string
		dsxID    string
	}{
		{
			name:     "dpu",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/dpu/dpu-1",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/dpu/dpu-1",
		},
		{
			name:     "operator bare metal host",
			spiffeID: "spiffe://k2.internal/tenant/operator-1/service/dsx/host/worker-1",
			dsxID:    "spiffe://operator-1.ds9.dsx.nvid.id/host/worker-1",
		},
		{
			name:     "dynamic tenant bare metal host",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/host/host-1",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/host/host-1",
		},
		{
			name:     "host process with role",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/host/host-1/process/nke-agent/role/operator",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/host/host-1/process/nke-agent/role/operator",
		},
		{
			name:     "pod",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/host/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/host/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		},
		{
			name:     "vm with role",
			spiffeID: "spiffe://k2.internal/tenant/tenant-database/service/dsx/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
			dsxID:    "spiffe://tenant-database.ds9.dsx.nvid.id/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := spiffeid.RequireFromString(test.spiffeID)
			got, err := ToDSXSPIFFEID(input)
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
		"spiffe://k2.internal/tenant/tenant_1/service/dsx/host/host-1",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/pod/pod-1",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/host/host-1/process",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/host/host-1/pod/not-a-uuid",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/vm/vm-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/host/host-1/role/BadRole",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/host/host-1/extra/value",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/host/%20host",
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
		"spiffe://operator-1.sjc1.dsx.nvid.id/host/host-1",
		"spiffe://operator-1.ds9.identity.nvid.id/host/host-1",
		"spiffe://operator-1.ds9.dsx.nvidia.com/host/host-1",
		"spiffe://tenant_1.ds9.dsx.nvid.id/host/host-1",
		"spiffe://operator-1.extra.ds9.dsx.nvid.id/host/host-1",
		"spiffe://operator-1.ds9.dsx.nvid.id/host",
		"spiffe://operator-1.ds9.dsx.nvid.id/dpu/dpu-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		"spiffe://operator-1.ds9.dsx.nvid.id/vm/vm-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
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
		Tenant:       "operator-1",
		ResourceType: "dpu",
		ResourceID:   "dpu-9e2a4c71",
	}
	spiffeID, err := c.SPIFFEID(spiffeid.RequireTrustDomainFromString("k2.internal"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spiffeID.String(), "spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-9e2a4c71"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
