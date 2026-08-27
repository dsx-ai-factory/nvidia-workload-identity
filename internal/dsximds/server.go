package dsximds

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"dsx-identity/internal/dsxidentity"
	"dsx-identity/internal/identity"
)

type Verifier struct {
	trustDomain   spiffeid.TrustDomain
	tokenVerifier *oidc.IDTokenVerifier
}

func NewVerifier(ctx context.Context, issuer, audience string, trustDomain spiffeid.TrustDomain, jwksURL string, timeout time.Duration) (*Verifier, error) {
	if audience == "" {
		return nil, fmt.Errorf("audience is required")
	}
	if err := dsxidentity.ValidateIssuer(issuer); err != nil {
		return nil, err
	}
	if trustDomain.IsZero() {
		return nil, fmt.Errorf("trust domain is required")
	}
	ctx = oidc.ClientContext(ctx, newRemoteHTTPClient(timeout))
	// Issuer, audience, accepted algorithms, and keys are invariant across tokens.
	// Public JWKS can verify only the supported asymmetric algorithms listed here.
	tokenVerifier := oidc.NewVerifier(issuer, oidc.NewRemoteKeySet(ctx, jwksURL), &oidc.Config{
		ClientID: audience,
		SupportedSigningAlgs: []string{
			oidc.RS256, oidc.RS384, oidc.RS512,
			oidc.ES256, oidc.ES384, oidc.ES512,
			oidc.PS256, oidc.PS384, oidc.PS512,
			oidc.EdDSA,
		},
	})
	return &Verifier{
		trustDomain:   trustDomain,
		tokenVerifier: tokenVerifier,
	}, nil
}

func newRemoteHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: otelhttp.NewTransport(transport), Timeout: timeout}
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (spiffeid.ID, error) {
	token, err := v.tokenVerifier.Verify(ctx, rawToken)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("verify attestation token: %w", err)
	}

	subject, err := spiffeid.FromString(token.Subject)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("parse host subject: %w", err)
	}
	parsedSubject, err := dsxidentity.Parse(subject)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("parse host subject: %w", err)
	}
	// Any path after the BM resource narrows the identity and must not gain the
	// authority to attest a SPIRE node.
	if parsedSubject.ResourceType != identity.ResourceBM || len(parsedSubject.Path) != 0 {
		return spiffeid.ID{}, fmt.Errorf("attestation subject must identify a base host")
	}

	agentID, err := spiffeid.FromSegments(v.trustDomain,
		"spire", "agent", "dsx_imds", "tenant", parsedSubject.Tenant, "bm", parsedSubject.ResourceID)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("build host agent ID: %w", err)
	}
	return agentID, nil
}
