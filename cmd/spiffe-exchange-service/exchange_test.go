package main

import (
	"crypto"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
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

	store := NewInMemoryKeyStore(ES256KeyGenerator{})
	server := &ExchangeServer{
		keyStore:   store,
		baseDomain: "nvidia.com",
		region:     "sjc-1",
	}

	// Pre-create a key for the full trust domain (simulating what transformSpiffeID does)
	fullDomain := "tenant1.sjc-1.internal.nvidia.com"
	_, expectedKID, _, err := store.GetOrCreateKey(fullDomain)
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

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

func TestInMemoryKeyStore_GetOrCreateKey(t *testing.T) {
	store := NewInMemoryKeyStore(ES256KeyGenerator{})

	// First call should create a key
	signer1, kid1, alg1, err := store.GetOrCreateKey("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if signer1 == nil {
		t.Error("signer should not be nil")
	}
	if kid1 == "" {
		t.Error("key ID should not be empty")
	}
	if alg1 != jose.ES256 {
		t.Errorf("algorithm = %v, want ES256", alg1)
	}

	// Second call should return the same key
	signer2, kid2, alg2, err := store.GetOrCreateKey("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kid1 != kid2 {
		t.Error("key ID should be the same on second call")
	}
	if signer1 != signer2 {
		t.Error("signer should be the same on second call")
	}
	if alg1 != alg2 {
		t.Error("algorithm should be the same on second call")
	}

	// Different domain should get a different key
	_, kid3, _, err := store.GetOrCreateKey("domain2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kid1 == kid3 {
		t.Error("different domains should have different keys")
	}
}

func TestInMemoryKeyStore_GetKeys(t *testing.T) {
	store := NewInMemoryKeyStore(ES256KeyGenerator{})

	// GetKeys should create a key if none exist
	keys, err := store.GetKeys("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("got %d keys, want 1", len(keys))
	}

	// Verify the key matches what GetOrCreateKey returns
	signer, kid, alg, err := store.GetOrCreateKey("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys[0].KeyID != kid {
		t.Error("GetKeys key ID should match GetOrCreateKey")
	}
	if keys[0].Signer != signer {
		t.Error("GetKeys signer should match GetOrCreateKey")
	}
	if keys[0].Algorithm != alg {
		t.Error("GetKeys algorithm should match GetOrCreateKey")
	}
}

// mockKeyStore implements KeyStore for testing handlers
type mockKeyStore struct {
	keys map[string]*DomainKey
}

func newMockKeyStore() *mockKeyStore {
	return &mockKeyStore{keys: make(map[string]*DomainKey)}
}

func (m *mockKeyStore) GetOrCreateKey(trustDomain string) (crypto.Signer, KeyID, jose.SignatureAlgorithm, error) {
	if dk, ok := m.keys[trustDomain]; ok {
		return dk.Signer, dk.KeyID, dk.Algorithm, nil
	}
	gen := ES256KeyGenerator{}
	signer, alg, err := gen.GenerateKey()
	if err != nil {
		return nil, "", "", err
	}
	kid := generateKeyID(signer.Public())
	m.keys[trustDomain] = &DomainKey{Signer: signer, KeyID: kid, Algorithm: alg}
	return signer, kid, alg, nil
}

func (m *mockKeyStore) GetKeys(trustDomain string) ([]DomainKey, error) {
	_, _, _, err := m.GetOrCreateKey(trustDomain)
	if err != nil {
		return nil, err
	}
	dk := m.keys[trustDomain]
	return []DomainKey{*dk}, nil
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

func TestHandleExchange_InvalidRequests(t *testing.T) {
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
	}{
		{
			name:        "wrong content type",
			contentType: "text/plain",
			body:        `{"jwt":"test"}`,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "invalid json",
			contentType: "application/json",
			body:        `{invalid}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "empty jwt",
			contentType: "application/json",
			body:        `{"jwt":""}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "missing jwt field",
			contentType: "application/json",
			body:        `{}`,
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/exchange", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rr := httptest.NewRecorder()

			server.handleExchange(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}

func TestGenerateKeyID(t *testing.T) {
	gen := ES256KeyGenerator{}

	// Generate two different keys
	signer1, _, _ := gen.GenerateKey()
	signer2, _, _ := gen.GenerateKey()

	kid1 := generateKeyID(signer1.Public())
	kid2 := generateKeyID(signer2.Public())

	// Same key should produce same ID
	kid1Again := generateKeyID(signer1.Public())
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
