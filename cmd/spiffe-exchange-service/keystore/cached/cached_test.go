package cached

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

// countingKeyStore is a test double for KeyStore that tracks call counts.
type countingKeyStore struct {
	mu                  sync.Mutex
	getOrCreateKeyCount int
	getKeysCount        int
	keys                map[string]keystore.DomainKey
	keygen              keystore.KeyGenerator
	getOrCreateKeyErr   error
	getKeysErr          error
}

func newCountingKeyStore() *countingKeyStore {
	return &countingKeyStore{
		keys:   make(map[string]keystore.DomainKey),
		keygen: keystore.ES256KeyGenerator{},
	}
}

func (m *countingKeyStore) GetOrCreateKey(trustDomain string) (keystore.DomainKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getOrCreateKeyCount++

	if m.getOrCreateKeyErr != nil {
		return keystore.DomainKey{}, m.getOrCreateKeyErr
	}

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

func (m *countingKeyStore) GetKeys(trustDomain string) ([]keystore.DomainKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getKeysCount++

	if m.getKeysErr != nil {
		return nil, m.getKeysErr
	}

	// Ensure key exists
	if _, ok := m.keys[trustDomain]; !ok {
		dk, err := m.keygen.GenerateKey()
		if err != nil {
			return nil, err
		}
		m.keys[trustDomain] = dk
	}

	dk := m.keys[trustDomain]
	return []keystore.DomainKey{dk}, nil
}

func (m *countingKeyStore) callCounts() (getOrCreateKey, getKeys int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getOrCreateKeyCount, m.getKeysCount
}

// closableKeyStore wraps countingKeyStore and tracks Close calls.
type closableKeyStore struct {
	*countingKeyStore
	closed bool
}

func (m *closableKeyStore) Close() error {
	m.closed = true
	return nil
}

func TestKeyStore_GetOrCreateKey(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, DefaultTTL)
	defer cached.Close()

	// First call should hit backend
	dk1, err := cached.GetOrCreateKey("example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dk1.Signer == nil {
		t.Fatal("expected non-nil signer")
	}
	if dk1.KeyID == "" {
		t.Fatal("expected non-empty key ID")
	}
	if dk1.Algorithm != jose.ES256 {
		t.Fatalf("expected ES256, got %v", dk1.Algorithm)
	}

	getOrCreate1, _ := mock.callCounts()
	if getOrCreate1 != 1 {
		t.Fatalf("expected 1 backend call, got %d", getOrCreate1)
	}

	// Second call should hit cache
	dk2, err := cached.GetOrCreateKey("example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	getOrCreate2, _ := mock.callCounts()
	if getOrCreate2 != 1 {
		t.Fatalf("expected still 1 backend call, got %d", getOrCreate2)
	}

	// Should return same values
	if dk1.KeyID != dk2.KeyID {
		t.Fatalf("expected same key ID, got %s and %s", dk1.KeyID, dk2.KeyID)
	}
	if dk1.Algorithm != dk2.Algorithm {
		t.Fatalf("expected same algorithm")
	}
}

func TestKeyStore_GetKeys(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, DefaultTTL)
	defer cached.Close()

	// First call should hit backend
	keys1, err := cached.GetKeys("example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys1) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys1))
	}

	_, getKeys1 := mock.callCounts()
	if getKeys1 != 1 {
		t.Fatalf("expected 1 backend call, got %d", getKeys1)
	}

	// Second call should hit cache
	keys2, err := cached.GetKeys("example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, getKeys2 := mock.callCounts()
	if getKeys2 != 1 {
		t.Fatalf("expected still 1 backend call, got %d", getKeys2)
	}

	// Should return same key
	if keys1[0].KeyID != keys2[0].KeyID {
		t.Fatalf("expected same key ID")
	}
}

func TestKeyStore_DifferentDomains(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, DefaultTTL)
	defer cached.Close()

	dk1, _ := cached.GetOrCreateKey("domain1.com")
	dk2, _ := cached.GetOrCreateKey("domain2.com")

	if dk1.KeyID == dk2.KeyID {
		t.Fatal("different domains should have different keys")
	}

	getOrCreate, _ := mock.callCounts()
	if getOrCreate != 2 {
		t.Fatalf("expected 2 backend calls for 2 domains, got %d", getOrCreate)
	}
}

func TestKeyStore_ConcurrentAccess(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, DefaultTTL)
	defer cached.Close()

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	results := make(chan keystore.KeyID, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			dk, err := cached.GetOrCreateKey("concurrent.com")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			results <- dk.KeyID
		}()
	}

	wg.Wait()
	close(results)

	// All results should have the same key ID
	var firstKid keystore.KeyID
	for kid := range results {
		if firstKid == "" {
			firstKid = kid
		} else if kid != firstKid {
			t.Fatalf("expected all goroutines to get same key ID")
		}
	}
}

func TestKeyStore_SingleflightDedup(t *testing.T) {
	var backendCalls atomic.Int32

	// Create a slow mock that tracks concurrent calls
	slowMock := &slowKeyStore{
		countingKeyStore: newCountingKeyStore(),
		delay:            50 * time.Millisecond,
		calls:            &backendCalls,
	}

	cached := New(slowMock, DefaultTTL)
	defer cached.Close()

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Start all goroutines at roughly the same time
	start := make(chan struct{})
	for range goroutines {
		go func() {
			defer wg.Done()
			<-start
			cached.GetOrCreateKey("singleflight.com")
		}()
	}

	close(start)
	wg.Wait()

	// With singleflight, only 1 backend call should be made
	if calls := backendCalls.Load(); calls != 1 {
		t.Fatalf("expected 1 backend call due to singleflight, got %d", calls)
	}
}

type slowKeyStore struct {
	*countingKeyStore
	delay time.Duration
	calls *atomic.Int32
}

func (s *slowKeyStore) GetOrCreateKey(trustDomain string) (keystore.DomainKey, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return s.countingKeyStore.GetOrCreateKey(trustDomain)
}

func (s *slowKeyStore) GetKeys(trustDomain string) ([]keystore.DomainKey, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return s.countingKeyStore.GetKeys(trustDomain)
}

func TestKeyStore_TTLExpiration(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, 50*time.Millisecond)
	defer cached.Close()

	// First call
	cached.GetOrCreateKey("expiring.com")
	getOrCreate1, _ := mock.callCounts()
	if getOrCreate1 != 1 {
		t.Fatalf("expected 1 backend call, got %d", getOrCreate1)
	}

	// Immediate second call should hit cache
	cached.GetOrCreateKey("expiring.com")
	getOrCreate2, _ := mock.callCounts()
	if getOrCreate2 != 1 {
		t.Fatalf("expected still 1 backend call, got %d", getOrCreate2)
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Call after expiration should hit backend again
	cached.GetOrCreateKey("expiring.com")
	getOrCreate3, _ := mock.callCounts()
	if getOrCreate3 != 2 {
		t.Fatalf("expected 2 backend calls after expiration, got %d", getOrCreate3)
	}
}

func TestKeyStore_BackendError(t *testing.T) {
	mock := newCountingKeyStore()
	mock.getOrCreateKeyErr = errors.New("backend error")

	cached := New(mock, DefaultTTL)
	defer cached.Close()

	_, err := cached.GetOrCreateKey("error.com")
	if err == nil {
		t.Fatal("expected error")
	}

	// Error should not be cached, so another call should hit backend again
	mock.getOrCreateKeyErr = nil
	dk, err := cached.GetOrCreateKey("error.com")
	if err != nil {
		t.Fatalf("unexpected error after backend recovery: %v", err)
	}
	if dk.KeyID == "" {
		t.Fatal("expected valid key ID after recovery")
	}
}

func TestKeyStore_Close(t *testing.T) {
	mock := &closableKeyStore{countingKeyStore: newCountingKeyStore()}
	cached := New(mock, DefaultTTL)

	if err := cached.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.closed {
		t.Fatal("expected backend Close to be called")
	}
}

func TestKeyStore_CloseNonCloser(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, DefaultTTL)

	// Should not panic or error when backend doesn't implement Closer
	if err := cached.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKeyStore_ZeroTTLUsesDefault(t *testing.T) {
	mock := newCountingKeyStore()
	cached := New(mock, 0) // 0 should use DefaultTTL
	defer cached.Close()

	// Should work with default TTL
	_, err := cached.GetOrCreateKey("default.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
