// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dsxidentity

import (
	"encoding/json"
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
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/Cluster/Cluster-1/namespace/team-a/workload/api",
			subject: identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: "Cluster", ResourceID: "Cluster-1",
				Path: []string{"namespace", "team-a", "workload", "api"}},
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
		"spiffe://operator-1.ds9.dsx.nvid.id/cluster/cluster-1/namespace",
		"spiffe://operator-1.ds9.dsx.nvid.id/cluster/cluster-1/namespace/team-a/namespace/team-b",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(spiffeid.RequireFromString(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bare metal",
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1",
			want: `{"dsx.nvidia.com/tenant": "tenant-2", "dsx.nvidia.com/region": "ds9", "dsx.nvidia.com/service": "dsx",
				"dsx.nvidia.com/partition": "nvid.id",
				"dsx.nvidia.com/resource": [{"resource_type": "bm", "resource_id": "host-1"}]}`,
		},
		{
			name: "vm with role",
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/vm/vm-1/role/database",
			want: `{"dsx.nvidia.com/tenant": "tenant-2", "dsx.nvidia.com/region": "ds9", "dsx.nvidia.com/service": "dsx",
				"dsx.nvidia.com/partition": "nvid.id",
				"dsx.nvidia.com/resource": [{"resource_type": "vm", "resource_id": "vm-1"}, {"resource_type": "role", "resource_id": "database"}]}`,
		},
		{
			name: "pod with role",
			raw:  "spiffe://operator-1.ds9.identity-probe.nvid.id/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6/role/web",
			want: `{"dsx.nvidia.com/tenant": "operator-1", "dsx.nvidia.com/region": "ds9", "dsx.nvidia.com/service": "identity-probe",
				"dsx.nvidia.com/partition": "nvid.id",
				"dsx.nvidia.com/resource": [{"resource_type": "bm", "resource_id": "worker-1"},
					{"resource_type": "pod", "resource_id": "13c31c2c-2673-42d1-bdeb-241864db47f6"},
					{"resource_type": "role", "resource_id": "web"}]}`,
		},
		{
			name: "process with role",
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1/process/nke-agent/role/operator",
			want: `{"dsx.nvidia.com/tenant": "tenant-2", "dsx.nvidia.com/region": "ds9", "dsx.nvidia.com/service": "dsx",
				"dsx.nvidia.com/partition": "nvid.id",
				"dsx.nvidia.com/resource": [{"resource_type": "bm", "resource_id": "host-1"}, {"resource_type": "process", "resource_id": "nke-agent"},
					{"resource_type": "role", "resource_id": "operator"}]}`,
		},
		{
			name: "DPU service",
			raw:  "spiffe://operator-1.ds9.dsx.nvid.id/dpu/dpu-1/dpuservice/storage_service/namespace/storage-system",
			want: `{"dsx.nvidia.com/tenant": "operator-1", "dsx.nvidia.com/region": "ds9", "dsx.nvidia.com/service": "dsx",
				"dsx.nvidia.com/partition": "nvid.id",
				"dsx.nvidia.com/resource": [{"resource_type": "dpu", "resource_id": "dpu-1"},
					{"resource_type": "dpuservice", "resource_id": "storage_service"},
					{"resource_type": "namespace", "resource_id": "storage-system"}]}`,
		},
		{
			name: "generic key",
			raw:  "spiffe://tenant-2.ds9.dsx.nvid.id/Cluster/Cluster-1/namespace/team-a",
			want: `{"dsx.nvidia.com/tenant": "tenant-2", "dsx.nvidia.com/region": "ds9", "dsx.nvidia.com/service": "dsx",
				"dsx.nvidia.com/partition": "nvid.id",
				"dsx.nvidia.com/resource": [{"resource_type": "Cluster", "resource_id": "Cluster-1"},
					{"resource_type": "namespace", "resource_id": "team-a"}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			subject, err := Parse(spiffeid.RequireFromString(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			claims, err := Claims(subject, testRegion, testPartition)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s, want %s", encoded, test.want)
			}
		})
	}
}

func TestClaimsRejectInvalidInput(t *testing.T) {
	t.Parallel()

	subject := identity.Subject{Tenant: "tenant-2", Service: "dsx", ResourceType: identity.ResourceBM, ResourceID: "host-1"}
	if _, err := Claims(subject, "DS9", testPartition); err == nil {
		t.Error("invalid region succeeded")
	}
	subject.Path = []string{"pod"}
	if _, err := Claims(subject, testRegion, testPartition); err == nil {
		t.Error("unpaired path succeeded")
	}
}
