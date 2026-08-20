// Package identity defines one subject grammar with two SPIFFE ID encodings.
// Local IDs use /tenant/<tenant>/service/<service>/<resource-suffix>. DSX IDs
// use <tenant>.<issuer-host> as the trust domain. For issuer
// https://ds9.dsx.nvid.id, local ID
// spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1 becomes
// spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1.
package identity

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"k8s.io/apimachinery/pkg/util/validation"
)

var (
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	podUIDPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	rolePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

func parseIssuer(raw string) (spiffeid.TrustDomain, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return spiffeid.TrustDomain{}, fmt.Errorf("issuer: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return spiffeid.TrustDomain{}, fmt.Errorf("issuer must be an HTTPS URL containing only a DNS hostname")
	}
	host := parsed.Hostname()
	if !strings.Contains(host, ".") || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return spiffeid.TrustDomain{}, fmt.Errorf("issuer host %q must be a DNS hostname", host)
	}
	trustDomain, err := spiffeid.TrustDomainFromString(host)
	if err != nil {
		return spiffeid.TrustDomain{}, fmt.Errorf("issuer host: %w", err)
	}
	return trustDomain, nil
}

// ValidateIssuer validates an HTTPS issuer with a DNS hostname, for example
// https://ds9.dsx.nvid.id.
func ValidateIssuer(raw string) error {
	_, err := parseIssuer(raw)
	return err
}

// ResourceType identifies the DSX resource named by a subject.
type ResourceType string

const (
	ResourceDPU ResourceType = "dpu"
	ResourceBM  ResourceType = "bm"
	ResourceVM  ResourceType = "vm"
)

// ValidTenant reports whether tenant can form the leading DNS label of a DSX
// trust domain without changing the supplied value.
func ValidTenant(tenant string) bool {
	return len(validation.IsDNS1123Label(tenant)) == 0
}

// ValidService reports whether service is a DNS label, such as dsx or
// identity-probe.
func ValidService(service string) bool {
	return len(validation.IsDNS1123Label(service)) == 0
}

// ValidResourceID reports whether id is one valid DSX resource path segment.
func ValidResourceID(id string) bool {
	return segmentPattern.MatchString(id)
}

// ValidPodUID reports whether uid is the UUID form used in SPIRE Pod paths.
func ValidPodUID(uid string) bool {
	return podUIDPattern.MatchString(uid)
}

// Subject is the trust-domain-independent payload preserved when DSX Identity Keys
// converts a local SPIRE JWT-SVID subject into a DSX JWT subject.
type Subject struct {
	Tenant       string
	ResourceType ResourceType
	ResourceID   string
	WorkloadType string
	WorkloadID   string
	Role         string
}

// parseSPIFFEID parses the local SPIRE workload path. It intentionally does not
// validate the trust domain because DSX Identity Keys calls it only after local SPIRE
// has validated the JWT-SVID.
func parseSPIFFEID(id spiffeid.ID) (Subject, error) {
	segments, err := pathSegments(id)
	if err != nil {
		return Subject{}, err
	}
	if len(segments) < 6 || segments[0] != "tenant" || segments[2] != "service" || !ValidService(segments[3]) {
		return Subject{}, fmt.Errorf("invalid SPIRE identity path %q", id.Path())
	}
	return parseResourcePath(segments[1], segments[4:])
}

// ParseDSXID validates the tenant-bearing DSX trust domain and the shared
// resource suffix used by DSX JWT subjects.
func ParseDSXID(issuer string, id spiffeid.ID) (Subject, error) {
	issuerHost, err := parseIssuer(issuer)
	if err != nil {
		return Subject{}, err
	}
	suffix := "." + issuerHost.Name()
	segments, err := pathSegments(id)
	if err != nil {
		return Subject{}, err
	}
	domain := id.TrustDomain().Name()
	tenant, ok := strings.CutSuffix(domain, suffix)
	if !ok {
		return Subject{}, fmt.Errorf("invalid DSX trust domain %q", domain)
	}
	return parseResourcePath(tenant, segments)
}

// ToDSXSPIFFEID changes only the encoding of a validated local SPIFFE ID.
// It has no tenant input because the tenant in the local subject is preserved.
func ToDSXSPIFFEID(issuer string, id spiffeid.ID) (spiffeid.ID, error) {
	subject, err := parseSPIFFEID(id)
	if err != nil {
		return spiffeid.ID{}, err
	}
	return subject.DSXID(issuer)
}

// SPIFFEID encodes s as
// spiffe://<trust-domain>/tenant/<tenant>/service/<service>/<resource-suffix>.
func (s Subject) SPIFFEID(td spiffeid.TrustDomain, service string) (spiffeid.ID, error) {
	if !ValidService(service) {
		return spiffeid.ID{}, fmt.Errorf("invalid service %q", service)
	}
	segments, err := s.resourceSegments()
	if err != nil {
		return spiffeid.ID{}, err
	}
	return spiffeid.FromSegments(td, append([]string{"tenant", s.Tenant, "service", service}, segments...)...)
}

// DSXID encodes s as
// spiffe://<tenant>.ds9.dsx.nvid.id/<resource-suffix> for the example issuer
// https://ds9.dsx.nvid.id.
func (s Subject) DSXID(issuer string) (spiffeid.ID, error) {
	issuerHost, err := parseIssuer(issuer)
	if err != nil {
		return spiffeid.ID{}, err
	}
	segments, err := s.resourceSegments()
	if err != nil {
		return spiffeid.ID{}, err
	}
	td, err := spiffeid.TrustDomainFromString(s.Tenant + "." + issuerHost.Name())
	if err != nil {
		return spiffeid.ID{}, err
	}
	return spiffeid.FromSegments(td, segments...)
}

// parseResourcePath parses this shared suffix:
//
//	<dpu|bm|vm>/<id>[/process/<name>][/role/<role>]
//	bm/<id>/pod/<uuid>[/role/<role>]
//
// Sharing this parser prevents the two encodings from accepting different
// subjects.
func parseResourcePath(tenant string, resource []string) (Subject, error) {
	subject := Subject{Tenant: tenant}
	if len(resource) < 2 {
		return Subject{}, fmt.Errorf("resource path requires type and ID")
	}
	subject.ResourceType, subject.ResourceID = ResourceType(resource[0]), resource[1]
	rest := resource[2:]
	if len(rest) >= 2 && (rest[0] == "process" || rest[0] == "pod") {
		subject.WorkloadType, subject.WorkloadID = rest[0], rest[1]
		rest = rest[2:]
	}
	if len(rest) == 2 && rest[0] == "role" {
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

func (s Subject) resourceSegments() ([]string, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	segments := []string{string(s.ResourceType), s.ResourceID}
	if s.WorkloadType != "" {
		segments = append(segments, s.WorkloadType, s.WorkloadID)
	}
	if s.Role != "" {
		segments = append(segments, "role", s.Role)
	}
	return segments, nil
}

func (s Subject) validate() error {
	if !ValidTenant(s.Tenant) {
		return fmt.Errorf("invalid tenant %q", s.Tenant)
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

func pathSegments(id spiffeid.ID) ([]string, error) {
	path, ok := strings.CutPrefix(id.Path(), "/")
	if !ok || path == "" {
		return nil, fmt.Errorf("identity path is empty")
	}
	return strings.Split(path, "/"), nil
}
