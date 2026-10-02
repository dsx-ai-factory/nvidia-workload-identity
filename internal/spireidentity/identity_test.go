package spireidentity

import (
	"reflect"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/identity"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		subject identity.Subject
	}{
		{
			name: "generic key",
			raw:  "spiffe://k2.internal/tenant/tenant-2/service/dsx/Cluster/Cluster-1/namespace/team-a/namespace/team-b",
			subject: identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: "Cluster", ResourceID: "Cluster-1",
				Path: []string{"namespace", "team-a", "namespace", "team-b"}},
		},
		{
			name:    "dpu",
			raw:     "spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-1",
			subject: identity.Subject{Tenant: "operator-1", Service: "dsx", ResourceType: identity.ResourceDPU, ResourceID: "dpu-1"},
		},
		{
			name: "DPU service",
			raw:  "spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-1/dpuservice/storage_service/namespace/storage-system",
			subject: identity.Subject{Tenant: "operator-1", Service: "dsx", ResourceType: identity.ResourceDPU, ResourceID: "dpu-1",
				Path: []string{"dpuservice", "storage_service", "namespace", "storage-system"}},
		},
		{
			name: "process with role",
			raw:  "spiffe://k2.internal/tenant/tenant-2/service/dsx/bm/host-1/process/nke-agent/role/operator",
			subject: identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: identity.ResourceBM, ResourceID: "host-1",
				Path: []string{"process", "nke-agent", "role", "operator"}},
		},
		{
			name: "pod",
			raw:  "spiffe://k2.internal/tenant/tenant-2/service/identity-probe/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
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
			rebuilt, err := ID(got, id.TrustDomain())
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
		"spiffe://k2.internal/dpu/dpu-1",
		"spiffe://k2.internal/tenant/tenant_1/service/dsx/bm/host-1",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/_invalid/resource-1",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/bm",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/cluster",
		"spiffe://k2.internal/tenant/operator-1/service/dsx/cluster/_invalid",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(spiffeid.RequireFromString(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
