package dsximds

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"dsx-identity/internal/identity"
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
	return &http.Client{Transport: otelhttp.NewTransport(transport), Timeout: timeout}
}

func newRemoteVerifier(ctx context.Context, jwksURL, audience string, trustDomain spiffeid.TrustDomain, timeout time.Duration) (*Verifier, error) {
	ctx = oidc.ClientContext(ctx, newRemoteHTTPClient(timeout))
	return NewVerifier(oidc.NewRemoteKeySet(ctx, jwksURL), audience, trustDomain)
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
