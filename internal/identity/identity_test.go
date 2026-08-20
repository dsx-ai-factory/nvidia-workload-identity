package identity

import (
	"strings"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const testIssuer = "https://ds9.dsx.nvid.id"

func TestParseIssuer(t *testing.T) {
	t.Parallel()

	shortIssuer := strings.Replace(testIssuer, ".dsx.nvid", "", 1)
	for _, raw := range []string{testIssuer, shortIssuer} {
		if err := ValidateIssuer(raw); err != nil {
			t.Errorf("ValidateIssuer(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "spiffe://ds9.dsx.nvid.id", "http://ds9.dsx.nvid.id",
		"https://ds9", "https://ds9.dsx.nvid.id/path",
		"https://ds9.dsx.nvid.id:8443", "https://user@ds9.dsx.nvid.id",
	} {
		if err := ValidateIssuer(raw); err == nil {
			t.Errorf("ValidateIssuer(%q) succeeded", raw)
		}
	}
}

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
		service  string
		spiffeID string
		dsxID    string
	}{
		{
			name:     "dpu",
			service:  "dsx",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/dpu/dpu-1",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/dpu/dpu-1",
		},
		{
			name:     "operator bare metal host",
			service:  "dsx",
			spiffeID: "spiffe://k2.internal/tenant/operator-1/service/dsx/bm/worker-1",
			dsxID:    "spiffe://operator-1.ds9.dsx.nvid.id/bm/worker-1",
		},
		{
			name:     "dynamic tenant bare metal host",
			service:  "dsx",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/bm/host-1",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1",
		},
		{
			name:     "host process with role",
			service:  "dsx",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/dsx/bm/host-1/process/nke-agent/role/operator",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1/process/nke-agent/role/operator",
		},
		{
			name:     "pod with another service",
			service:  "identity-probe",
			spiffeID: "spiffe://k2.internal/tenant/tenant-2/service/identity-probe/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
			dsxID:    "spiffe://tenant-2.ds9.dsx.nvid.id/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		},
		{
			name:     "vm with role",
			service:  "dsx",
			spiffeID: "spiffe://k2.internal/tenant/tenant-database/service/dsx/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
			dsxID:    "spiffe://tenant-database.ds9.dsx.nvid.id/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := spiffeid.RequireFromString(test.spiffeID)
			got, err := ToDSXSPIFFEID(testIssuer, input)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != test.dsxID {
				t.Fatalf("got %q, want %q", got, test.dsxID)
			}
			parsed, err := ParseDSXID(testIssuer, got)
			if err != nil {
				t.Fatal(err)
			}
			if rebuilt, err := parsed.SPIFFEID(input.TrustDomain(), test.service); err != nil || rebuilt != input {
				t.Fatalf("round trip got %q, %v", rebuilt, err)
			}
		})
	}
}

func TestRejectInvalidSPIFFEID(t *testing.T) {
	t.Parallel()

	tests := []string{
		"spiffe://k2.internal/tenant/tenant_1/service/dsx/bm/host-1",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/pod/pod-1",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1/process",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1/pod/not-a-uuid",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/vm/vm-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1/role/BadRole",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1/extra/value",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/bm/%20host",
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
		"spiffe://operator-1.sjc1.dsx.nvid.id/bm/host-1",
		"spiffe://operator-1.ds9.identity.nvid.id/bm/host-1",
		"spiffe://operator-1.ds9.dsx.nvidia.com/bm/host-1",
		"spiffe://tenant_1.ds9.dsx.nvid.id/bm/host-1",
		"spiffe://operator-1.extra.ds9.dsx.nvid.id/bm/host-1",
		"spiffe://operator-1.ds9.dsx.nvid.id/invalid/resource-1",
		"spiffe://operator-1.ds9.dsx.nvid.id/dpu/dpu-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		"spiffe://operator-1.ds9.dsx.nvid.id/vm/vm-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			id, err := spiffeid.FromString(raw)
			if err != nil {
				return
			}
			if _, err := ParseDSXID(testIssuer, id); err == nil {
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
	spiffeID, err := c.SPIFFEID(spiffeid.RequireTrustDomainFromString("k2.internal"), "dsx")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spiffeID.String(), "spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-9e2a4c71"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
