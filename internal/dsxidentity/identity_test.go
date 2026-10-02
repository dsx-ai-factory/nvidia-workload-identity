package dsxidentity

import (
	"reflect"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/identity"
)

const testIssuer = "https://ds9.dsx.nvid.id"

const (
	testRegion    = "ds9"
	testPartition = "nvid.id"
)

func TestValidateIssuer(t *testing.T) {
	t.Parallel()

	if err := ValidateIssuer(testIssuer); err != nil {
		t.Fatal(err)
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

func TestValidateDomain(t *testing.T) {
	t.Parallel()

	if err := ValidateDomain(testRegion, testPartition); err != nil {
		t.Fatal(err)
	}
	for _, domain := range [][2]string{{"", testPartition}, {"DS9", testPartition}, {testRegion, ""}, {testRegion, "nvid_id"}} {
		if err := ValidateDomain(domain[0], domain[1]); err == nil {
			t.Errorf("ValidateDomain(%q, %q) succeeded", domain[0], domain[1])
		}
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		subject identity.Subject
	}{
		{
			name: "generic key",
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/Cluster/Cluster-1/namespace/team-a/namespace/team-b",
			subject: identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: "Cluster", ResourceID: "Cluster-1",
				Path: []string{"namespace", "team-a", "namespace", "team-b"}},
		},
		{
			name:    "bare metal",
			raw:     "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1",
			subject: identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: identity.ResourceBM, ResourceID: "host-1"},
		},
		{
			name: "DPU service",
			raw:  "spiffe://operator-1.ds9.dsx.nvid.id/dpu/dpu-1/dpuservice/storage_service/namespace/storage-system",
			subject: identity.Subject{Tenant: "operator-1", Service: "dsx", ResourceType: identity.ResourceDPU, ResourceID: "dpu-1",
				Path: []string{"dpuservice", "storage_service", "namespace", "storage-system"}},
		},
		{
			name: "process with role",
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1/process/nke-agent/role/operator",
			subject: identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: identity.ResourceBM, ResourceID: "host-1",
				Path: []string{"process", "nke-agent", "role", "operator"}},
		},
		{
			name: "pod",
			raw:  "spiffe://tenant-2.ds9.identity-probe.nvid.id/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
			subject: identity.Subject{Tenant: "tenant-2", Service: "identity-probe", ResourceType: identity.ResourceBM, ResourceID: "worker-1",
				Path: []string{"pod", "13c31c2c-2673-42d1-bdeb-241864db47f6"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			id := spiffeid.RequireFromString(test.raw)
			got, err := Parse(id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.subject) {
				t.Fatalf("got %#v, want %#v", got, test.subject)
			}
			rebuilt, err := ID(got, testRegion, testPartition)
			if err != nil {
				t.Fatal(err)
			}
			if rebuilt != id {
				t.Fatalf("got %q, want %q", rebuilt, id)
			}
		})
	}
}

func TestRejectInvalidID(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"spiffe://tenant_1.ds9.dsx.nvid.id/bm/host-1",
		"spiffe://operator-1.ds9.dsx/bm/host-1",
		"spiffe://operator-1.ds9.dsx.nvid.id/_invalid/resource-1",
		"spiffe://operator-1.ds9.dsx.nvid.id/bm",
		"spiffe://operator-1.ds9.dsx.nvid.id/cluster",
		"spiffe://operator-1.ds9.dsx.nvid.id/cluster/_invalid",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(spiffeid.RequireFromString(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
