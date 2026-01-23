package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

func setupPostgresContainer(t *testing.T, ctx context.Context) (Config, func()) {
	t.Helper()

	testcontainers.SkipIfProviderIsNotHealthy(t)

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("exchange"),
		tcpostgres.WithUsername("exchange"),
		tcpostgres.WithPassword("testpassword"),
		tcpostgres.WithInitScripts("testdata/schema.sql"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("failed to get container host: %v", err)
	}

	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("failed to get container port: %v", err)
	}

	cfg := Config{
		Host:     host,
		Port:     port.Int(),
		Database: "exchange",
		User:     "exchange",
		Password: "testpassword",
		SSLMode:  "disable",
	}

	cleanup := func() {
		container.Terminate(ctx)
	}

	return cfg, cleanup
}

func generateEncryptionKey(t *testing.T) string {
	t.Helper()
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("failed to generate encryption key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(keyBytes)
}

func TestKeyStore_Integration_GetOrCreateKey(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	// First call should create a key
	dk1, err := store.GetOrCreateKey("domain1.example.com")
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
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
	dk2, err := store.GetOrCreateKey("domain1.example.com")
	if err != nil {
		t.Fatalf("second GetOrCreateKey failed: %v", err)
	}

	if dk1.KeyID != dk2.KeyID {
		t.Errorf("key IDs should match: %v != %v", dk1.KeyID, dk2.KeyID)
	}
	if dk1.Algorithm != dk2.Algorithm {
		t.Error("algorithms should match")
	}

	// Different domain should get different key
	dk3, err := store.GetOrCreateKey("domain2.example.com")
	if err != nil {
		t.Fatalf("GetOrCreateKey for domain2 failed: %v", err)
	}

	if dk1.KeyID == dk3.KeyID {
		t.Error("different domains should have different key IDs")
	}
}

func TestKeyStore_Integration_Persistence(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	trustDomain := "persistent.example.com"

	// Create first store instance and generate a key
	store1, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create first keystore: %v", err)
	}

	dk1, err := store1.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
	}

	store1.Close()

	// Create second store instance (simulating application restart)
	store2, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create second keystore: %v", err)
	}
	defer store2.Close()

	// Should get the same key
	dk2, err := store2.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey on second store failed: %v", err)
	}

	if dk1.KeyID != dk2.KeyID {
		t.Errorf("key should persist across store instances: %v != %v", dk1.KeyID, dk2.KeyID)
	}
}

func TestKeyStore_Integration_GetKeys(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	trustDomain := "getkeys.example.com"

	// GetKeys should create a key if none exist
	keys, err := store.GetKeys(trustDomain)
	if err != nil {
		t.Fatalf("GetKeys failed: %v", err)
	}

	if len(keys) != 1 {
		t.Errorf("expected 1 key, got %d", len(keys))
	}

	if keys[0].KeyID == "" {
		t.Error("key ID should not be empty")
	}

	if keys[0].Algorithm != jose.ES256 {
		t.Errorf("algorithm = %v, want ES256", keys[0].Algorithm)
	}
}

func TestKeyStore_Integration_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	trustDomain := "concurrent.example.com"

	// Run multiple goroutines trying to get/create the same key
	const numGoroutines = 10
	results := make(chan keystore.KeyID, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			dk, err := store.GetOrCreateKey(trustDomain)
			if err != nil {
				errors <- err
				return
			}
			results <- dk.KeyID
		}()
	}

	var keyIDs []keystore.KeyID
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errors:
			t.Errorf("goroutine error: %v", err)
		case kid := <-results:
			keyIDs = append(keyIDs, kid)
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for goroutines")
		}
	}

	// All goroutines should get the same key ID
	if len(keyIDs) > 0 {
		first := keyIDs[0]
		for i, kid := range keyIDs {
			if kid != first {
				t.Errorf("goroutine %d got different key ID: %v != %v", i, kid, first)
			}
		}
	}
}

func TestKeyStore_Integration_MultipleDomains(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	domains := []string{
		"tenant1.sjc-1.internal.nvidia.com",
		"tenant2.sjc-1.internal.nvidia.com",
		"tenant1.sjc-1.vmaas.nvidia.com",
	}

	keyIDs := make(map[string]keystore.KeyID)
	for _, domain := range domains {
		dk, err := store.GetOrCreateKey(domain)
		if err != nil {
			t.Fatalf("GetOrCreateKey(%s) failed: %v", domain, err)
		}
		keyIDs[domain] = dk.KeyID
	}

	// All domains should have unique keys
	seen := make(map[keystore.KeyID]string)
	for domain, kid := range keyIDs {
		if existingDomain, exists := seen[kid]; exists {
			t.Errorf("domains %s and %s have the same key ID", domain, existingDomain)
		}
		seen[kid] = domain
	}
}

func TestKeyStore_Integration_WrongEncryptionKey(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	trustDomain := "wrongkey.example.com"

	// Create a key with one encryption key
	encryptionKey1 := generateEncryptionKey(t)
	store1, err := New(ctx, cfg, encryptionKey1, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create first keystore: %v", err)
	}

	_, err = store1.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
	}
	store1.Close()

	// Try to read with a different encryption key (new store instance, no cache)
	encryptionKey2 := generateEncryptionKey(t)
	store2, err := New(ctx, cfg, encryptionKey2, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create second keystore: %v", err)
	}
	defer store2.Close()

	// GetOrCreateKey should fail because the stored key can't be decrypted
	_, err = store2.GetOrCreateKey(trustDomain)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestKeyStore_Integration_Interface(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := New(ctx, cfg, encryptionKey, keystore.ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	// Verify it implements KeyStore interface
	var _ keystore.KeyStore = store

	// Use it like any KeyStore
	trustDomain := "interface.example.com"
	dk, err := store.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
	}

	// Sign something to verify the key works
	message := []byte("test message")
	hash := make([]byte, 32)
	copy(hash, message)

	sig, err := dk.Signer.Sign(rand.Reader, hash, nil)
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}

	if len(sig) == 0 {
		t.Error("signature should not be empty")
	}

	t.Logf("Successfully created key %s with algorithm %s", dk.KeyID, dk.Algorithm)
}
