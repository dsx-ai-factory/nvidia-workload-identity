package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/go-jose/go-jose/v4"
)

// KeyID is a unique identifier for a signing key.
type KeyID string

func (k KeyID) String() string { return string(k) }

// KeyGenerator generates signing keys.
type KeyGenerator interface {
	GenerateKey() (crypto.Signer, jose.SignatureAlgorithm, error)
}

// KeyStore is an interface for managing signing keys per trust domain.
// This allows swapping implementations (e.g., in-memory, Kubernetes secrets, HSM).
type KeyStore interface {
	// GetOrCreateKey returns the signer, key ID, and algorithm for a trust domain,
	// creating a new key pair if one doesn't exist.
	GetOrCreateKey(trustDomain string) (crypto.Signer, KeyID, jose.SignatureAlgorithm, error)
}

// DomainKey holds a key pair, its ID, and algorithm for a trust domain.
type DomainKey struct {
	Signer    crypto.Signer
	KeyID     KeyID
	Algorithm jose.SignatureAlgorithm
}

// InMemoryKeyStore is an in-memory implementation of KeyStore.
// Keys are generated on first use and lost on restart.
type InMemoryKeyStore struct {
	mu     sync.RWMutex
	keys   map[string]*DomainKey
	keygen KeyGenerator
}

// NewInMemoryKeyStore creates a new in-memory key store with the given key generator.
func NewInMemoryKeyStore(keygen KeyGenerator) *InMemoryKeyStore {
	return &InMemoryKeyStore{
		keys:   make(map[string]*DomainKey),
		keygen: keygen,
	}
}

// generateKeyID creates a key ID from the public key using SHA-256 fingerprint.
func generateKeyID(pub crypto.PublicKey) KeyID {
	var pubBytes []byte
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		pubBytes = elliptic.MarshalCompressed(k.Curve, k.X, k.Y)
	default:
		// Fallback: use string representation
		pubBytes = []byte(fmt.Sprintf("%v", pub))
	}
	hash := sha256.Sum256(pubBytes)
	return KeyID(base64.RawURLEncoding.EncodeToString(hash[:12]))
}

// GetOrCreateKey returns the signer, key ID, and algorithm for a trust domain, creating one if needed.
func (s *InMemoryKeyStore) GetOrCreateKey(trustDomain string) (crypto.Signer, KeyID, jose.SignatureAlgorithm, error) {
	// Fast path: check if key exists
	s.mu.RLock()
	if dk, ok := s.keys[trustDomain]; ok {
		s.mu.RUnlock()
		return dk.Signer, dk.KeyID, dk.Algorithm, nil
	}
	s.mu.RUnlock()

	// Slow path: create new key
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if dk, ok := s.keys[trustDomain]; ok {
		return dk.Signer, dk.KeyID, dk.Algorithm, nil
	}

	signer, alg, err := s.keygen.GenerateKey()
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to generate key for domain %s: %w", trustDomain, err)
	}

	kid := generateKeyID(signer.Public())

	s.keys[trustDomain] = &DomainKey{
		Signer:    signer,
		KeyID:     kid,
		Algorithm: alg,
	}

	return signer, kid, alg, nil
}

// ES256KeyGenerator generates ECDSA P-256 keys for ES256 signing.
type ES256KeyGenerator struct{}

func (ES256KeyGenerator) GenerateKey() (crypto.Signer, jose.SignatureAlgorithm, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return key, jose.ES256, err
}
