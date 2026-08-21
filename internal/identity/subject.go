// Package identity defines the subject shared by local and exchanged identities.
package identity

import (
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	podUIDPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	rolePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// ResourceType identifies the resource represented by a subject.
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
	WorkloadType string
	WorkloadID   string
	Role         string
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

// ValidPodUID reports whether uid is the UUID form used in Pod paths.
func ValidPodUID(uid string) bool {
	return podUIDPattern.MatchString(uid)
}

// ParseResource parses the resource suffix shared by both identity formats:
//
//	<dpu|bm|vm>/<id>[/process/<name>][/role/<role>]
//	bm/<id>/pod/<uuid>[/role/<role>]
func ParseResource(tenant, service string, resource []string) (Subject, error) {
	if len(resource) < 2 {
		return Subject{}, fmt.Errorf("resource path requires type and ID")
	}
	// [bm, host-1, process, nke-agent] starts with resource bm/host-1.
	subject := Subject{
		Tenant: tenant, Service: service,
		ResourceType: ResourceType(resource[0]), ResourceID: resource[1],
	}
	// The remaining [process, nke-agent] identifies the workload.
	rest := resource[2:]
	if len(rest) >= 2 && (rest[0] == "process" || rest[0] == "pod") {
		subject.WorkloadType, subject.WorkloadID = rest[0], rest[1]
		// Remove the consumed process/nke-agent or pod/<uuid> pair.
		rest = rest[2:]
	}
	if len(rest) == 2 && rest[0] == "role" {
		// [role, operator] sets the role to operator.
		subject.Role = rest[1]
		rest = nil
	}
	if len(rest) != 0 {
		return Subject{}, fmt.Errorf("invalid resource suffix")
	}
	if err := subject.validate(); err != nil {
		return Subject{}, err
	}
	return subject, nil
}

// ResourceSegments returns the validated resource suffix.
func (s Subject) ResourceSegments() ([]string, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	segments := []string{string(s.ResourceType), s.ResourceID}
	if s.WorkloadType != "" {
		// [bm, host-1] plus process/nke-agent becomes [bm, host-1, process, nke-agent].
		segments = append(segments, s.WorkloadType, s.WorkloadID)
	}
	if s.Role != "" {
		// Appending role/operator produces [..., role, operator].
		segments = append(segments, "role", s.Role)
	}
	return segments, nil
}

func (s Subject) validate() error {
	if !ValidTenant(s.Tenant) {
		return fmt.Errorf("invalid tenant %q", s.Tenant)
	}
	if !ValidService(s.Service) {
		return fmt.Errorf("invalid service %q", s.Service)
	}
	switch s.ResourceType {
	case ResourceDPU, ResourceBM, ResourceVM:
	default:
		return fmt.Errorf("invalid resource type %q", s.ResourceType)
	}
	if !ValidResourceID(s.ResourceID) {
		return fmt.Errorf("invalid resource ID %q", s.ResourceID)
	}
	if (s.WorkloadType == "") != (s.WorkloadID == "") {
		return fmt.Errorf("workload type and ID must be provided together")
	}
	if s.WorkloadType != "" {
		if s.WorkloadType != "process" && s.WorkloadType != "pod" {
			return fmt.Errorf("invalid workload type %q", s.WorkloadType)
		}
		if !segmentPattern.MatchString(s.WorkloadID) {
			return fmt.Errorf("invalid workload ID %q", s.WorkloadID)
		}
		if s.WorkloadType == "pod" && !ValidPodUID(s.WorkloadID) {
			return fmt.Errorf("invalid pod UID %q", s.WorkloadID)
		}
		if s.WorkloadType == "pod" && s.ResourceType != ResourceBM {
			return fmt.Errorf("pod identity requires BM resource type")
		}
	}
	if s.Role != "" && !rolePattern.MatchString(s.Role) {
		return fmt.Errorf("invalid role %q", s.Role)
	}
	return nil
}
