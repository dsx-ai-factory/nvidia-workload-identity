// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package identity defines the subject shared by local and exchanged identities.
package identity

import (
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/util/validation"
)

var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ResourceType is the first path key, such as bm, vm, or cluster.
type ResourceType string

const (
	ResourceDPU ResourceType = "dpu"
	ResourceBM  ResourceType = "bm"
	ResourceVM  ResourceType = "vm"
)

// Subject contains the fields preserved between local and exchanged identities.
type Subject struct {
	Tenant       string
	Service      string
	ResourceType ResourceType
	ResourceID   string
	// ResourceType and ResourceID form the required first pair of the resource path.
	// Path contains only the additional key/value pairs after that pair.
	// For dpu/dpu-1/dpuservice/storage/namespace/system, it contains
	// [dpuservice, storage, namespace, system].
	Path []string
}

// Resource is one key/value pair of a subject's resource path.
type Resource struct {
	Type string
	ID   string
}

// ValidTenant reports whether tenant is an unchanged DNS label.
func ValidTenant(tenant string) bool {
	return len(validation.IsDNS1123Label(tenant)) == 0
}

// ValidService reports whether service is an unchanged DNS label, such as dsx
// or identity-probe.
func ValidService(service string) bool {
	return len(validation.IsDNS1123Label(service)) == 0
}

// ValidResourceID reports whether id is one valid path segment.
func ValidResourceID(id string) bool {
	return segmentPattern.MatchString(id)
}

// ParseResource validates path pairs and preserves their order and case.
func ParseResource(tenant, service string, resource []string) (Subject, error) {
	if len(resource) < 2 {
		return Subject{}, fmt.Errorf("resource path requires type and ID")
	}
	// [dpu, dpu-1, dpuservice, storage, namespace, system] fixes dpu/dpu-1
	// as the resource and retains the remaining segments in Path.
	subject := Subject{
		Tenant:       tenant,
		Service:      service,
		ResourceType: ResourceType(resource[0]),
		ResourceID:   resource[1],
		Path:         append([]string(nil), resource[2:]...),
	}
	if err := subject.validate(); err != nil {
		return Subject{}, err
	}
	return subject, nil
}

// ResourceSegments returns the fixed resource fields followed by Path unchanged.
func (s Subject) ResourceSegments() ([]string, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	// Reattach Path unchanged so dpu/dpu-1 plus
	// [dpuservice, storage, namespace, system] restores the original resource path.
	return append([]string{string(s.ResourceType), s.ResourceID}, s.Path...), nil
}

// Resources returns the resource pair followed by each pair of Path, in order.
// For bm/worker-1/pod/pod-1, it returns [{bm worker-1} {pod pod-1}].
func (s Subject) Resources() []Resource {
	resources := []Resource{{Type: string(s.ResourceType), ID: s.ResourceID}}
	for i := 0; i+1 < len(s.Path); i += 2 {
		resources = append(resources, Resource{Type: s.Path[i], ID: s.Path[i+1]})
	}
	return resources
}

func (s Subject) validate() error {
	if !ValidTenant(s.Tenant) {
		return fmt.Errorf("invalid tenant %q", s.Tenant)
	}
	if !ValidService(s.Service) {
		return fmt.Errorf("invalid service %q", s.Service)
	}
	if !segmentPattern.MatchString(string(s.ResourceType)) {
		return fmt.Errorf("invalid resource type %q", s.ResourceType)
	}
	if !ValidResourceID(s.ResourceID) {
		return fmt.Errorf("invalid resource ID %q", s.ResourceID)
	}
	if len(s.Path)%2 != 0 {
		return fmt.Errorf("resource path requires complete key/value pairs")
	}
	keys := map[string]struct{}{string(s.ResourceType): {}}
	for i := 0; i < len(s.Path); i += 2 {
		key, value := s.Path[i], s.Path[i+1]
		if !segmentPattern.MatchString(key) || !segmentPattern.MatchString(value) {
			return fmt.Errorf("invalid path pair %q/%q", key, value)
		}
		if _, exists := keys[key]; exists {
			return fmt.Errorf("duplicate path key %q", key)
		}
		keys[key] = struct{}{}
	}
	return nil
}
