// Package memory provides an in-memory KeyStore implementation.
package memory

import (
	"fmt"
	"sync"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

// KeyStore is an in-memory implementation of keystore.KeyStore.
// Keys are generated on first use and lost on restart.
type KeyStore struct {
	mu     sync.RWMutex
	keys   map[string]keystore.DomainKey
	keygen keystore.KeyGenerator
}

// New creates a new in-memory key store with the given key generator.
func New(keygen keystore.KeyGenerator) *KeyStore {
	return &KeyStore{
		keys:   make(map[string]keystore.DomainKey),
		keygen: keygen,
	}
}

// GetOrCreateKey returns the key for a trust domain, creating one if needed.
func (s *KeyStore) GetOrCreateKey(trustDomain string) (keystore.DomainKey, error) {
	// Fast path: check if key exists
	s.mu.RLock()
	if dk, ok := s.keys[trustDomain]; ok {
		s.mu.RUnlock()
		return dk, nil
	}
	s.mu.RUnlock()

	// Slow path: create new key
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if dk, ok := s.keys[trustDomain]; ok {
		return dk, nil
	}

	dk, err := s.keygen.GenerateKey()
	if err != nil {
		return keystore.DomainKey{}, fmt.Errorf("failed to generate key for domain %s: %w", trustDomain, err)
	}

	s.keys[trustDomain] = dk
	return dk, nil
}

// GetKeys returns all keys for a trust domain. Creates a key if none exist.
func (s *KeyStore) GetKeys(trustDomain string) ([]keystore.DomainKey, error) {
	// Ensure a key exists
	dk, err := s.GetOrCreateKey(trustDomain)
	if err != nil {
		return nil, err
	}

	return []keystore.DomainKey{dk}, nil
}
