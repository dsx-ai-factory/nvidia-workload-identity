// Package identity defines the subject shared by local and exchanged identities.
package identity

import (
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/util/validation"
)

var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

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
	// Path contains every segment after ResourceID without assigning meaning to it.
	// For dpu/dpu-1/dpuservice/storage/namespace/system, it contains
	// [dpuservice, storage, namespace, system].
	Path []string
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

// ParseResource parses the fixed resource prefix and preserves the remaining
// path without interpreting it.
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
	return nil
}
