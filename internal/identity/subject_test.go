package identity

import (
	"reflect"
	"strings"
	"testing"
)

func TestResourceRoundTrip(t *testing.T) {
	t.Parallel()

	want := Subject{
		Tenant: "tenant-2", Service: "identity-probe", ResourceType: ResourceBM, ResourceID: "worker-1",
		WorkloadType: "pod", WorkloadID: "13c31c2c-2673-42d1-bdeb-241864db47f6", Role: "database",
	}
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
