package memory

import (
	"testing"

	"github.com/go-jose/go-jose/v4"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

func TestKeyStore_GetOrCreateKey(t *testing.T) {
	store := New(keystore.ES256KeyGenerator{})

	// First call should create a key
	dk1, err := store.GetOrCreateKey("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dk1.Signer == nil {
		t.Error("signer should not be nil")
	}
	if dk1.KeyID == "" {
		t.Error("key ID should not be empty")
	}
	if dk1.Algorithm != jose.ES256 {
		t.Errorf("algorithm = %v, want ES256", dk1.Algorithm)
	}

	// Second call should return the same key
	dk2, err := store.GetOrCreateKey("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dk1.KeyID != dk2.KeyID {
		t.Error("key ID should be the same on second call")
	}
	if dk1.Signer != dk2.Signer {
		t.Error("signer should be the same on second call")
	}
	if dk1.Algorithm != dk2.Algorithm {
		t.Error("algorithm should be the same on second call")
	}

	// Different domain should get a different key
	dk3, err := store.GetOrCreateKey("domain2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dk1.KeyID == dk3.KeyID {
		t.Error("different domains should have different keys")
	}
}

func TestKeyStore_GetKeys(t *testing.T) {
	store := New(keystore.ES256KeyGenerator{})

	// GetKeys should create a key if none exist
	keys, err := store.GetKeys("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("got %d keys, want 1", len(keys))
	}

	// Verify the key matches what GetOrCreateKey returns
	dk, err := store.GetOrCreateKey("domain1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys[0].KeyID != dk.KeyID {
		t.Error("GetKeys key ID should match GetOrCreateKey")
	}
	if keys[0].Signer != dk.Signer {
		t.Error("GetKeys signer should match GetOrCreateKey")
	}
	if keys[0].Algorithm != dk.Algorithm {
		t.Error("GetKeys algorithm should match GetOrCreateKey")
	}
}
