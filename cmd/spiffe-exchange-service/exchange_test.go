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

func TestParseTargetDomain(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		wantSubdomain string
		wantRemaining string
		wantOk        bool
	}{
		{
			name:          "valid path with remaining",
			path:          "/domain/tenant1/workload/pod/abc",
			wantSubdomain: "tenant1",
			wantRemaining: "/workload/pod/abc",
			wantOk:        true,
		},
		{
			name:          "valid path no remaining",
			path:          "/domain/tenant1",
			wantSubdomain: "tenant1",
			wantRemaining: "",
			wantOk:        true,
		},
		{
			name:          "valid path with slash only",
			path:          "/domain/tenant1/",
			wantSubdomain: "tenant1",
			wantRemaining: "/",
			wantOk:        true,
		},
		{
			name:          "no domain prefix",
			path:          "/workload/pod/abc",
			wantSubdomain: "",
			wantRemaining: "",
			wantOk:        false,
		},
		{
			name:          "empty path",
			path:          "",
			wantSubdomain: "",
			wantRemaining: "",
			wantOk:        false,
		},
		{
			name:          "just domain prefix",
			path:          "/domain/",
			wantSubdomain: "",
			wantRemaining: "",
			wantOk:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subdomain, remaining, ok := parseTargetDomain(tt.path)
			if subdomain != tt.wantSubdomain {
				t.Errorf("subdomain = %q, want %q", subdomain, tt.wantSubdomain)
			}
			if remaining != tt.wantRemaining {
				t.Errorf("remaining = %q, want %q", remaining, tt.wantRemaining)
			}
			if ok != tt.wantOk {
				t.Errorf("ok = %v, want %v", ok, tt.wantOk)
			}
		})
	}
}

func TestTransformSpiffeID(t *testing.T) {
	tests := []struct {
		name       string
		originalID string
		baseDomain string
		wantID     string
		wantErr    bool
	}{
		{
			name:       "valid transformation",
			originalID: "spiffe://cluster.example.com/domain/tenant1/workload/pod/abc",
			baseDomain: "example.com",
			wantID:     "spiffe://tenant1.example.com/workload/pod/abc",
			wantErr:    false,
		},
		{
			name:       "no remaining path",
			originalID: "spiffe://cluster.example.com/domain/tenant1",
			baseDomain: "example.com",
			wantID:     "spiffe://tenant1.example.com",
			wantErr:    false,
		},
		{
			name:       "missing domain prefix",
			originalID: "spiffe://cluster.example.com/workload/pod/abc",
			baseDomain: "example.com",
			wantID:     "",
			wantErr:    true,
		},
		{
			name:       "complex base domain",
			originalID: "spiffe://k2.sjc-1.k8s2-control.nvidia.com/domain/acme-corp/vmaas/vm/12345",
			baseDomain: "sjc-1.k8s2-control.nvidia.com",
			wantID:     "spiffe://acme-corp.sjc-1.k8s2-control.nvidia.com/vmaas/vm/12345",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalID, err := spiffeid.FromString(tt.originalID)
			if err != nil {
				t.Fatalf("failed to parse original ID: %v", err)
			}

			got, err := transformSpiffeID(originalID, tt.baseDomain)
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

func TestExtractTenantPrefix(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "full kubernetes service host",
			host: "tenant1.default.svc.cluster.local",
			want: "tenant1",
		},
		{
			name: "host with port",
			host: "tenant1.example.com:8080",
			want: "tenant1",
		},
		{
			name: "simple host",
			host: "tenant1",
			want: "tenant1",
		},
		{
			name: "two parts",
			host: "tenant1.example",
			want: "tenant1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTenantPrefix(tt.host)
			if got != tt.want {
				t.Errorf("got = %q, want %q", got, tt.want)
			}
		})
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
		baseDomain: "example.com",
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
		baseDomain: "example.com",
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
