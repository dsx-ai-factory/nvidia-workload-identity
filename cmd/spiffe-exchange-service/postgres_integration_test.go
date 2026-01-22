package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPostgresContainer(t *testing.T, ctx context.Context) (PostgresConfig, func()) {
	t.Helper()

	testcontainers.SkipIfProviderIsNotHealthy(t)

	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("exchange"),
		postgres.WithUsername("exchange"),
		postgres.WithPassword("testpassword"),
		postgres.WithInitScripts("testdata/schema.sql"),
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

	cfg := PostgresConfig{
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

func TestPostgresKeyStore_Integration_GetOrCreateKey(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	// First call should create a key
	signer1, kid1, alg1, err := store.GetOrCreateKey("domain1.example.com")
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
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
	_, kid2, alg2, err := store.GetOrCreateKey("domain1.example.com")
	if err != nil {
		t.Fatalf("second GetOrCreateKey failed: %v", err)
	}

	if kid1 != kid2 {
		t.Errorf("key IDs should match: %v != %v", kid1, kid2)
	}
	if alg1 != alg2 {
		t.Error("algorithms should match")
	}

	// Different domain should get different key
	_, kid3, _, err := store.GetOrCreateKey("domain2.example.com")
	if err != nil {
		t.Fatalf("GetOrCreateKey for domain2 failed: %v", err)
	}

	if kid1 == kid3 {
		t.Error("different domains should have different key IDs")
	}
}

func TestPostgresKeyStore_Integration_Persistence(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	trustDomain := "persistent.example.com"

	// Create first store instance and generate a key
	store1, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create first keystore: %v", err)
	}

	_, kid1, _, err := store1.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
	}

	store1.Close()

	// Create second store instance (simulating application restart)
	store2, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create second keystore: %v", err)
	}
	defer store2.Close()

	// Should get the same key
	_, kid2, _, err := store2.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey on second store failed: %v", err)
	}

	if kid1 != kid2 {
		t.Errorf("key should persist across store instances: %v != %v", kid1, kid2)
	}
}

func TestPostgresKeyStore_Integration_GetKeys(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
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

func TestPostgresKeyStore_Integration_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	trustDomain := "concurrent.example.com"

	// Run multiple goroutines trying to get/create the same key
	const numGoroutines = 10
	results := make(chan KeyID, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			_, kid, _, err := store.GetOrCreateKey(trustDomain)
			if err != nil {
				errors <- err
				return
			}
			results <- kid
		}()
	}

	var keyIDs []KeyID
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

func TestPostgresKeyStore_Integration_MultipleDomains(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	domains := []string{
		"tenant1.sjc-1.internal.nvidia.com",
		"tenant2.sjc-1.internal.nvidia.com",
		"tenant1.sjc-1.vmaas.nvidia.com",
	}

	keyIDs := make(map[string]KeyID)
	for _, domain := range domains {
		_, kid, _, err := store.GetOrCreateKey(domain)
		if err != nil {
			t.Fatalf("GetOrCreateKey(%s) failed: %v", domain, err)
		}
		keyIDs[domain] = kid
	}

	// All domains should have unique keys
	seen := make(map[KeyID]string)
	for domain, kid := range keyIDs {
		if existingDomain, exists := seen[kid]; exists {
			t.Errorf("domains %s and %s have the same key ID", domain, existingDomain)
		}
		seen[kid] = domain
	}
}

func TestPostgresKeyStore_Integration_WrongEncryptionKey(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	trustDomain := "wrongkey.example.com"

	// Create a key with one encryption key
	encryptionKey1 := generateEncryptionKey(t)
	store1, err := NewPostgresKeyStore(ctx, cfg, encryptionKey1, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create first keystore: %v", err)
	}

	_, _, _, err = store1.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
	}
	store1.Close()

	// Try to read with a different encryption key (new store instance, no cache)
	encryptionKey2 := generateEncryptionKey(t)
	store2, err := NewPostgresKeyStore(ctx, cfg, encryptionKey2, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create second keystore: %v", err)
	}
	defer store2.Close()

	// GetOrCreateKey should fail because the stored key can't be decrypted
	_, _, _, err = store2.GetOrCreateKey(trustDomain)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestPostgresKeyStore_Integration_KeyStoreInterface(t *testing.T) {
	ctx := context.Background()

	cfg, cleanup := setupPostgresContainer(t, ctx)
	defer cleanup()

	encryptionKey := generateEncryptionKey(t)
	store, err := NewPostgresKeyStore(ctx, cfg, encryptionKey, ES256KeyGenerator{})
	if err != nil {
		t.Fatalf("failed to create keystore: %v", err)
	}
	defer store.Close()

	// Verify it implements KeyStore interface
	var _ KeyStore = store

	// Use it like any KeyStore
	trustDomain := "interface.example.com"
	signer, kid, alg, err := store.GetOrCreateKey(trustDomain)
	if err != nil {
		t.Fatalf("GetOrCreateKey failed: %v", err)
	}

	// Sign something to verify the key works
	message := []byte("test message")
	hash := make([]byte, 32)
	copy(hash, message)

	sig, err := signer.Sign(rand.Reader, hash, nil)
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}

	if len(sig) == 0 {
		t.Error("signature should not be empty")
	}

	t.Logf("Successfully created key %s with algorithm %s", kid, alg)
}
