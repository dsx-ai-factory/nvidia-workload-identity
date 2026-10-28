// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/dsxidentity"
)

func TestExchangeSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		local string
		dsx   string
	}{
		{
			name:  "generic key with workload suffix",
			local: "spiffe://k2.internal/tenant/tenant-2/service/dsx/Cluster/Cluster-1/namespace/team-a/workload/api",
			dsx:   "spiffe://tenant-2.ds9.dsx.nvid.id/Cluster/Cluster-1/namespace/team-a/workload/api",
		},
		{
			name:  "pod resource",
			local: "spiffe://k2.internal/tenant/tenant-2/service/dsx/pod/pod-1",
			dsx:   "spiffe://tenant-2.ds9.dsx.nvid.id/pod/pod-1",
		},
		{
			name:  "dpu",
			local: "spiffe://k2.internal/tenant/tenant-2/service/dsx/dpu/dpu-1",
			dsx:   "spiffe://tenant-2.ds9.dsx.nvid.id/dpu/dpu-1",
		},
		{
			name:  "DPU service",
			local: "spiffe://k2.internal/tenant/operator-1/service/dsx/dpu/dpu-1/dpuservice/storage_service/namespace/storage-system",
			dsx:   "spiffe://operator-1.ds9.dsx.nvid.id/dpu/dpu-1/dpuservice/storage_service/namespace/storage-system",
		},
		{
			name:  "operator bare metal host",
			local: "spiffe://k2.internal/tenant/operator-1/service/dsx/bm/worker-1",
			dsx:   "spiffe://operator-1.ds9.dsx.nvid.id/bm/worker-1",
		},
		{
			name:  "tenant bare metal host",
			local: "spiffe://k2.internal/tenant/tenant-2/service/dsx/bm/host-1",
			dsx:   "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1",
		},
		{
			name:  "process with role",
			local: "spiffe://k2.internal/tenant/tenant-2/service/dsx/bm/host-1/process/nke-agent/role/operator",
			dsx:   "spiffe://tenant-2.ds9.dsx.nvid.id/bm/host-1/process/nke-agent/role/operator",
		},
		{
			name:  "pod",
			local: "spiffe://k2.internal/tenant/tenant-2/service/identity-probe/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
			dsx:   "spiffe://tenant-2.ds9.identity-probe.nvid.id/bm/worker-1/pod/13c31c2c-2673-42d1-bdeb-241864db47f6",
		},
		{
			name:  "vm with role",
			local: "spiffe://k2.internal/tenant/tenant-database/service/dsx/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
			dsx:   "spiffe://tenant-database.ds9.dsx.nvid.id/vm/8114d80d-b149-4b0b-aafa-c9c398f72c74/role/database",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, claims, err := exchangeSubject("ds9", "nvid.id", spiffeid.RequireFromString(test.local))
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != test.dsx {
				t.Fatalf("got %q, want %q", got, test.dsx)
			}
			// The claims must agree with the exchanged sub.
			subject, err := dsxidentity.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			want, err := dsxidentity.Claims(subject, "ds9", "nvid.id")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(claims, want) {
				t.Fatalf("claims = %#v, want %#v", claims, want)
			}
		})
	}
}

func TestExchangeSubjectRejectsTenantlessLocalID(t *testing.T) {
	t.Parallel()

	if _, _, err := exchangeSubject(
		"ds9",
		"nvid.id",
		spiffeid.RequireFromString("spiffe://k2.internal/dpu/dpu-1/process/dpu-agent"),
	); err == nil {
		t.Fatal("expected error")
	}
}

func TestExchangeSubjectRejectsInvalidPairs(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"cluster/cluster-1/namespace",
		"cluster/cluster-1/namespace/team-a/namespace/team-b",
		"cluster/cluster-1/cluster/cluster-2",
		"cluster/cluster-1/_namespace/team-a",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			id := spiffeid.RequireFromString("spiffe://k2.internal/tenant/tenant-2/service/dsx/" + path)
			if _, _, err := exchangeSubject("ds9", "nvid.id", id); err == nil {
				t.Fatal("invalid subject was exchanged")
			}
		})
	}
}
