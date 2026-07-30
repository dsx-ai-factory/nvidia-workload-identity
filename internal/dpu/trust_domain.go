package dpu

import (
	"fmt"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// ValidateTrustDomain checks that td is a non-empty DNS-1123 subdomain suitable
// for use as a SPIFFE ID authority segment.
func ValidateTrustDomain(td string) (string, error) {
	trimmed := strings.TrimSpace(td)
	if trimmed == "" {
		return "", fmt.Errorf("trust domain is empty after trimming")
	}
	if errs := k8svalidation.IsDNS1123Subdomain(trimmed); len(errs) > 0 {
		return "", fmt.Errorf("trust domain %q is not a valid DNS-1123 subdomain: %s", trimmed, strings.Join(errs, "; "))
	}
	return trimmed, nil
}
