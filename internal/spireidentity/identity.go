// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package spireidentity builds and parses cluster-local SPIRE workload IDs.
package spireidentity

import (
	"fmt"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/identity"
)

// Parse validates a complete local SPIRE workload ID:
//
//	spiffe://<trust-domain>/tenant/<tenant>/service/<service>/<resource>
//
// The resource contains key/value pairs with unique keys. SPIRE validates
// the JWT-SVID before this parser runs.
func Parse(id spiffeid.ID) (identity.Subject, error) {
	segments, err := pathSegments(id)
	if err != nil {
		return identity.Subject{}, err
	}
	if len(segments) < 6 || segments[0] != "tenant" || segments[2] != "service" {
		return identity.Subject{}, fmt.Errorf("invalid SPIRE identity path %q", id.Path())
	}
	return identity.ParseResource(segments[1], segments[3], segments[4:])
}

// ID encodes subject as:
//
//	spiffe://<trust-domain>/tenant/<tenant>/service/<service>/<resource>
//
// For example: spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1.
func ID(subject identity.Subject, td spiffeid.TrustDomain) (spiffeid.ID, error) {
	resource, err := subject.ResourceSegments()
	if err != nil {
		return spiffeid.ID{}, err
	}
	// [bm, host-1] gains [tenant, operator-1, service, dsx].
	return spiffeid.FromSegments(td,
		append([]string{"tenant", subject.Tenant, "service", subject.Service}, resource...)...)
}

func pathSegments(id spiffeid.ID) ([]string, error) {
	// /tenant/operator-1/service/dsx/bm/host-1 drops the leading slash.
	path, ok := strings.CutPrefix(id.Path(), "/")
	if !ok || path == "" {
		return nil, fmt.Errorf("identity path is empty")
	}
	// tenant/operator-1/... becomes [tenant, operator-1, service, dsx, ...].
	return strings.Split(path, "/"), nil
}
