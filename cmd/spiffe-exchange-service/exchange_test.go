package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore/memory"
)

func TestParsePathComponents(t *testing.T) {
	tests := []struct {
		name             string
		path             string
		wantTenant       string
		wantService      string
		wantResourceType string
		wantResourceID   string
		wantErr          bool
	}{
		{
			name:             "vm workload",
			path:             "/tenant/tenant1/service/vmaas/vm/abc-123",
			wantTenant:       "tenant1",
			wantService:      "vmaas",
			wantResourceType: "vm",
			wantResourceID:   "abc-123",
			wantErr:          false,
		},
		{
			name:             "pod workload",
			path:             "/tenant/acme-corp/service/default/pod/xyz-789",
			wantTenant:       "acme-corp",
			wantService:      "default",
			wantResourceType: "pod",
			wantResourceID:   "xyz-789",
			wantErr:          false,
		},
		{
			name:    "missing domain prefix",
			path:    "/workload/pod/abc",
			wantErr: true,
		},
		{
			name:    "missing service segment",
			path:    "/tenant/tenant1/vmaas/vm/abc",
			wantErr: true,
		},
		{
			name:    "insufficient components",
			path:    "/tenant/tenant1/service/vmaas",
			wantErr: true,
		},
		{
			name:    "empty path",
			path:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePathComponents(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if got.Tenant != tt.wantTenant {
				t.Errorf("Tenant = %q, want %q", got.Tenant, tt.wantTenant)
			}
			if got.Service != tt.wantService {
				t.Errorf("Service = %q, want %q", got.Service, tt.wantService)
			}
			if got.ResourceType != tt.wantResourceType {
				t.Errorf("ResourceType = %q, want %q", got.ResourceType, tt.wantResourceType)
			}
			if got.ResourceID != tt.wantResourceID {
				t.Errorf("ResourceID = %q, want %q", got.ResourceID, tt.wantResourceID)
			}
		})
	}
}

func TestTransformSpiffeID(t *testing.T) {
	tests := []struct {
		name       string
		originalID string
		region     string
		baseDomain string
		wantID     string
		wantErr    bool
	}{
		{
			name:       "vm workload",
			originalID: "spiffe://k2.sjc-1.nvidia.com/tenant/tenant1/service/vmaas/vm/12345",
			region:     "sjc-1",
			baseDomain: "nvidia.com",
			wantID:     "spiffe://tenant1.sjc-1.vmaas.nvidia.com/vm/12345",
			wantErr:    false,
		},
		{
			name:       "pod workload",
			originalID: "spiffe://k2.sjc-1.nvidia.com/tenant/acme-corp/service/default/pod/abc-xyz",
			region:     "sjc-1",
			baseDomain: "nvidia.com",
			wantID:     "spiffe://acme-corp.sjc-1.default.nvidia.com/pod/abc-xyz",
			wantErr:    false,
		},
		{
			name:       "missing domain prefix",
			originalID: "spiffe://k2.sjc-1.nvidia.com/workload/pod/abc",
			region:     "sjc-1",
			baseDomain: "nvidia.com",
			wantID:     "",
			wantErr:    true,
		},
		{
			name:       "missing product segment",
			originalID: "spiffe://k2.sjc-1.nvidia.com/tenant/tenant1/vmaas/vm/abc",
			region:     "sjc-1",
			baseDomain: "nvidia.com",
			wantID:     "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalID, err := spiffeid.FromString(tt.originalID)
			if err != nil {
				t.Fatalf("failed to parse original ID: %v", err)
			}

			got, err := transformSpiffeID(originalID, tt.region, tt.baseDomain)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.String() != tt.wantID {
				t.Errorf("got = %q, want %q", got.String(), tt.wantID)
			}
		})
	}
}

func TestJWKS_UsesFullHostForKeyLookup(t *testing.T) {
	// Regression test: JWKS handler was extracting only tenant prefix,
	// looking for keys under "tenant1.nvidia.com" instead of "tenant1.sjc-1.internal.nvidia.com"

	store := memory.New(keystore.ES256KeyGenerator{})
	server := &ExchangeServer{
		keyStore:   store,
		baseDomain: "nvidia.com",
		region:     "sjc-1",
	}

	// Pre-create a key for the full trust domain (simulating what transformSpiffeID does)
	fullDomain := "tenant1.sjc-1.internal.nvidia.com"
	dk, err := store.GetOrCreateKey(fullDomain)
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}
	expectedKID := dk.KeyID

	// Make JWKS request with full domain as Host header
	req := httptest.NewRequest("GET", "/.well-known/jwks.json", nil)
	req.Host = fullDomain + ":8080"
	rec := httptest.NewRecorder()

	server.handleJWKS(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Verify the returned JWKS contains the key we created
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&jwks); err != nil {
		t.Fatalf("failed to decode JWKS: %v", err)
	}

	if len(jwks.Keys) == 0 {
		t.Fatal("expected at least one key in JWKS")
	}

	// The key ID should match the one we created for the full domain
	if jwks.Keys[0].Kid != expectedKID.String() {
		t.Errorf("key ID = %q, want %q", jwks.Keys[0].Kid, expectedKID.String())
	}
}

// mockKeyStore implements keystore.KeyStore for testing handlers
type mockKeyStore struct {
	keys   map[string]keystore.DomainKey
	keygen keystore.KeyGenerator
}

func newMockKeyStore() *mockKeyStore {
	return &mockKeyStore{
		keys:   make(map[string]keystore.DomainKey),
		keygen: keystore.ES256KeyGenerator{},
	}
}

func (m *mockKeyStore) GetOrCreateKey(trustDomain string) (keystore.DomainKey, error) {
	if dk, ok := m.keys[trustDomain]; ok {
		return dk, nil
	}
	dk, err := m.keygen.GenerateKey()
	if err != nil {
		return keystore.DomainKey{}, err
	}
	m.keys[trustDomain] = dk
	return dk, nil
}

func (m *mockKeyStore) GetKeys(trustDomain string) ([]keystore.DomainKey, error) {
	dk, err := m.GetOrCreateKey(trustDomain)
	if err != nil {
		return nil, err
	}
	return []keystore.DomainKey{dk}, nil
}

func TestHandleJWKS(t *testing.T) {
	keyStore := newMockKeyStore()
	server := &ExchangeServer{
		keyStore:   keyStore,
		baseDomain: "nvidia.com",
	}

	tests := []struct {
		name       string
		host       string
		wantStatus int
		wantKeys   int
	}{
		{
			name:       "valid request",
			host:       "tenant1.default.svc.cluster.local",
			wantStatus: http.StatusOK,
			wantKeys:   1,
		},
		{
			name:       "simple tenant host",
			host:       "acme.example.com",
			wantStatus: http.StatusOK,
			wantKeys:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
			if tt.host != "" {
				req.Host = tt.host
			}
			rr := httptest.NewRecorder()

			server.handleJWKS(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			if tt.wantStatus == http.StatusOK {
				var jwks jose.JSONWebKeySet
				if err := json.NewDecoder(rr.Body).Decode(&jwks); err != nil {
					t.Fatalf("failed to decode JWKS: %v", err)
				}
				if len(jwks.Keys) != tt.wantKeys {
					t.Errorf("got %d keys, want %d", len(jwks.Keys), tt.wantKeys)
				}
				if tt.wantKeys > 0 {
					key := jwks.Keys[0]
					if key.KeyID == "" {
						t.Error("key ID should not be empty")
					}
					if key.Algorithm != "ES256" {
						t.Errorf("algorithm = %s, want ES256", key.Algorithm)
					}
					if key.Use != "sig" {
						t.Errorf("use = %s, want sig", key.Use)
					}
				}
			}
		})
	}
}

func TestHandleTokenExchange_InvalidRequests(t *testing.T) {
	keyStore := newMockKeyStore()
	server := &ExchangeServer{
		keyStore:   keyStore,
		baseDomain: "nvidia.com",
	}

	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantError   string
	}{
		{
			name:        "wrong content type",
			contentType: "application/json",
			body:        `{"jwt":"test"}`,
			wantStatus:  http.StatusUnsupportedMediaType,
			wantError:   "invalid_request",
		},
		{
			name:        "missing grant_type",
			contentType: "application/x-www-form-urlencoded",
			body:        "subject_token=test&subject_token_type=urn:ietf:params:oauth:token-type:jwt",
			wantStatus:  http.StatusBadRequest,
			wantError:   "unsupported_grant_type",
		},
		{
			name:        "wrong grant_type",
			contentType: "application/x-www-form-urlencoded",
			body:        "grant_type=client_credentials&subject_token=test&subject_token_type=urn:ietf:params:oauth:token-type:jwt",
			wantStatus:  http.StatusBadRequest,
			wantError:   "unsupported_grant_type",
		},
		{
			name:        "missing subject_token",
			contentType: "application/x-www-form-urlencoded",
			body:        "grant_type=urn:ietf:params:oauth:grant-type:token-exchange&subject_token_type=urn:ietf:params:oauth:token-type:jwt",
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid_request",
		},
		{
			name:        "wrong subject_token_type",
			contentType: "application/x-www-form-urlencoded",
			body:        "grant_type=urn:ietf:params:oauth:grant-type:token-exchange&subject_token=test&subject_token_type=urn:ietf:params:oauth:token-type:access_token",
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/exchange", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rr := httptest.NewRecorder()

			server.handleTokenExchange(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			// Verify error response format
			var errResp TokenExchangeErrorResponse
			if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errResp.Error != tt.wantError {
				t.Errorf("error = %q, want %q", errResp.Error, tt.wantError)
			}
		})
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestGenerateKeyID(t *testing.T) {
	gen := keystore.ES256KeyGenerator{}

	// Generate two different keys
	dk1 := must(gen.GenerateKey())
	dk2 := must(gen.GenerateKey())

	kid1 := keystore.GenerateKeyID(dk1.Signer.Public())
	kid2 := keystore.GenerateKeyID(dk2.Signer.Public())

	// Same key should produce same ID
	kid1Again := keystore.GenerateKeyID(dk1.Signer.Public())
	if kid1 != kid1Again {
		t.Error("same key should produce same key ID")
	}

	// Different keys should produce different IDs
	if kid1 == kid2 {
		t.Error("different keys should produce different key IDs")
	}

	// Key ID should be non-empty
	if kid1 == "" || kid2 == "" {
		t.Error("key ID should not be empty")
	}
}
