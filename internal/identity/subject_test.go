package identity

import (
	"reflect"
	"strings"
	"testing"
)

func TestResourceRoundTrip(t *testing.T) {
	t.Parallel()

	for _, want := range []Subject{
		{
			Tenant: "tenant-2", Service: "identity-probe", ResourceType: ResourceBM, ResourceID: "worker-1",
			Path: []string{"pod", "13c31c2c-2673-42d1-bdeb-241864db47f6", "role", "database"},
		},
		{
			Tenant: "operator-1", Service: "dsx", ResourceType: ResourceDPU, ResourceID: "dpu-1",
			Path: []string{"dpuservice", "storage_service", "namespace", "storage-system"},
		},
	} {
		segments, err := want.ResourceSegments()
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseResource(want.Tenant, want.Service, segments)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
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

func TestRejectInvalidResourcePairs(t *testing.T) {
	t.Parallel()

	for _, resource := range [][]string{
		{}, {"cluster"}, {"", "cluster-1"}, {"cluster", ""},
		{"_cluster", "cluster-1"}, {"cluster", "_invalid"},
		{"cluster", "cluster-1", "namespace"},
		{"cluster", "cluster-1", "", "team-a"},
		{"cluster", "cluster-1", "namespace", ""},
		{"cluster", "cluster-1", "_namespace", "team-a"},
		{"cluster", "cluster-1", "namespace", "_invalid"},
		{"cluster", "cluster-1", "namespace", "team-a", "namespace", "team-b"},
		{"cluster", "cluster-1", "namespace", "team-a", "namespace", "team-a"},
		{"cluster", "cluster-1", "cluster", "cluster-2"},
	} {
		t.Run(strings.Join(resource, "/"), func(t *testing.T) {
			t.Parallel()
			if _, err := ParseResource("tenant-2", "dsx", resource); err == nil {
				t.Fatal("invalid resource path was accepted")
			}
			if len(resource) >= 2 {
				subject := Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: ResourceType(resource[0]), ResourceID: resource[1], Path: resource[2:]}
				if _, err := subject.ResourceSegments(); err == nil {
					t.Fatal("invalid subject was encoded")
				}
			}
		})
	}
}
