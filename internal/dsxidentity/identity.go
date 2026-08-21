// Package dsxidentity builds and parses exchanged DSX identities.
// For example, it encodes this local identity:
//
//	spiffe://k2.internal/tenant/operator-1/service/dsx/bm/host-1
//
// as:
//
//	spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1
package dsxidentity

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"k8s.io/apimachinery/pkg/util/validation"

	"dsx-identity/internal/identity"
)

// ValidateIssuer validates an HTTPS issuer containing only a DNS hostname,
// such as https://ds9.dsx.nvid.id.
func ValidateIssuer(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("issuer: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("issuer must be an HTTPS URL containing only a DNS hostname")
	}
	host := parsed.Hostname()
	// Reject single-label hosts such as https://ds9.
	if !strings.Contains(host, ".") || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return fmt.Errorf("issuer host %q must be a DNS hostname", host)
	}
	return nil
}

// ValidateDomain validates the deployment coordinates encoded in DSX identities.
func ValidateDomain(region, partition string) error {
	if len(validation.IsDNS1123Label(region)) != 0 {
		return fmt.Errorf("invalid region %q", region)
	}
	if len(validation.IsDNS1123Subdomain(partition)) != 0 {
		return fmt.Errorf("invalid partition %q", partition)
	}
	return nil
}

// Parse validates an exchanged DSX ID.
func Parse(id spiffeid.ID) (identity.Subject, error) {
	// operator-1.ds9.dsx.nvid.id becomes [operator-1, ds9, dsx, nvid.id].
	labels := strings.SplitN(id.TrustDomain().Name(), ".", 4)
	if len(labels) != 4 {
		return identity.Subject{}, fmt.Errorf("invalid DSX trust domain %q", id.TrustDomain().Name())
	}
	if err := ValidateDomain(labels[1], labels[3]); err != nil {
		return identity.Subject{}, err
	}
	resource, err := pathSegments(id)
	if err != nil {
		return identity.Subject{}, err
	}
	return identity.ParseResource(labels[0], labels[2], resource)
}

// ID encodes subject as tenant.region.service.partition. For example, region
// ds9, service dsx, and partition nvid.id produce
// spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1.
func ID(subject identity.Subject, region, partition string) (spiffeid.ID, error) {
	if err := ValidateDomain(region, partition); err != nil {
		return spiffeid.ID{}, err
	}
	resource, err := subject.ResourceSegments()
	if err != nil {
		return spiffeid.ID{}, err
	}
	// [operator-1, ds9, dsx, nvid.id] becomes operator-1.ds9.dsx.nvid.id.
	td, err := spiffeid.TrustDomainFromString(
		strings.Join([]string{subject.Tenant, region, subject.Service, partition}, "."),
	)
	if err != nil {
		return spiffeid.ID{}, err
	}
	// The trust domain plus [bm, host-1] becomes the complete exchanged ID.
	return spiffeid.FromSegments(td, resource...)
}

func pathSegments(id spiffeid.ID) ([]string, error) {
	// /bm/host-1 becomes bm/host-1.
	path, ok := strings.CutPrefix(id.Path(), "/")
	if !ok || path == "" {
		return nil, fmt.Errorf("identity path is empty")
	}
	// bm/host-1 becomes [bm, host-1].
	return strings.Split(path, "/"), nil
}
