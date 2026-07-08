package dsximds

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/identity"
	"dsx-identity/internal/stricturl"
)

const defaultJWKSRequestTimeout = 10 * time.Second

type Verifier struct {
	audience    string
	trustDomain spiffeid.TrustDomain
	keySet      oidc.KeySet
}

func NewVerifier(keySet oidc.KeySet, audience string, trustDomain spiffeid.TrustDomain) (*Verifier, error) {
	if keySet == nil {
		return nil, fmt.Errorf("key set is required")
	}
	if audience == "" {
		return nil, fmt.Errorf("audience is required")
	}
	if trustDomain.IsZero() {
		return nil, fmt.Errorf("trust domain is required")
	}
	return &Verifier{
		audience:    audience,
		trustDomain: trustDomain,
		keySet:      keySet,
	}, nil
}

func NewRemoteVerifier(ctx context.Context, jwksURL, audience string, trustDomain spiffeid.TrustDomain) (*Verifier, error) {
	return newRemoteVerifier(ctx, jwksURL, audience, trustDomain, defaultJWKSRequestTimeout)
}

func newRemoteHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Fetch JWKS directly from the configured endpoint. Ignore ambient proxies.
	transport.Proxy = nil
	return &http.Client{Transport: transport, Timeout: timeout}
}

func newRemoteVerifier(ctx context.Context, jwksURL, audience string, trustDomain spiffeid.TrustDomain, timeout time.Duration) (*Verifier, error) {
	u, err := stricturl.ParseHTTP(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("JWKS URL %w", err)
	}
	if u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("JWKS URL must not contain user information or a fragment")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("JWKS request timeout must be positive")
	}
	keyContext := oidc.ClientContext(ctx, newRemoteHTTPClient(timeout))
	return NewVerifier(oidc.NewRemoteKeySet(keyContext, jwksURL), audience, trustDomain)
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (spiffeid.ID, error) {
	// Accept only the asymmetric algorithms supported by go-oidc and Exchange
	// public JWKS. Symmetric algorithms cannot be verified from published keys.
	verifier := oidc.NewVerifier(identity.Issuer, v.keySet, &oidc.Config{
		ClientID: v.audience,
		SupportedSigningAlgs: []string{
			oidc.RS256, oidc.RS384, oidc.RS512,
			oidc.ES256, oidc.ES384, oidc.ES512,
			oidc.PS256, oidc.PS384, oidc.PS512,
			oidc.EdDSA,
		},
	})
	token, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("verify attestation token: %w", err)
	}

	subject, err := spiffeid.FromString(token.Subject)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("parse host subject: %w", err)
	}
	parsedSubject, err := identity.ParseDSXID(subject)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("parse host subject: %w", err)
	}
	// Process, Pod, and role identities are narrower than the host and must not
	// gain the authority to attest a SPIRE node.
	if parsedSubject.ResourceType != identity.ResourceHost || parsedSubject.WorkloadType != "" || parsedSubject.Role != "" {
		return spiffeid.ID{}, fmt.Errorf("attestation subject must identify a base host")
	}

	agentID, err := spiffeid.FromSegments(v.trustDomain,
		"spire", "agent", "dsx_imds", "tenant", parsedSubject.Tenant, "host", parsedSubject.ResourceID)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("build host agent ID: %w", err)
	}
	return agentID, nil
}
