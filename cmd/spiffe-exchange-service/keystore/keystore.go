// Package keystore provides interfaces and types for managing signing keys per trust domain.
package keystore

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/go-jose/go-jose/v4"
)

// KeyID is a unique identifier for a signing key.
type KeyID string

func (k KeyID) String() string { return string(k) }

// DomainKey holds a key pair, its ID, and algorithm for a trust domain.
type DomainKey struct {
	Signer    crypto.Signer
	KeyID     KeyID
	Algorithm jose.SignatureAlgorithm
}

// KeyStore is an interface for managing signing keys per trust domain.
// This allows swapping implementations (e.g., in-memory, Kubernetes secrets, HSM).
type KeyStore interface {
	// GetOrCreateKey returns the key for a trust domain, creating a new key pair
	// if one doesn't exist. Used for signing.
	GetOrCreateKey(trustDomain string) (DomainKey, error)

	// GetKeys returns all keys for a trust domain. Used for the JWKS endpoint.
	// Creates a key if none exist for the domain.
	GetKeys(trustDomain string) ([]DomainKey, error)
}

// KeyGenerator generates signing keys.
type KeyGenerator interface {
	GenerateKey() (DomainKey, error)
}

// GenerateKeyID creates a key ID from a public key using SHA-256 fingerprint.
func GenerateKeyID(pub crypto.PublicKey) KeyID {
	var pubBytes []byte
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		pubBytes = elliptic.MarshalCompressed(k.Curve, k.X, k.Y)
	default:
		// Fallback: use string representation
		pubBytes = fmt.Appendf(nil, "%v", pub)
	}
	hash := sha256.Sum256(pubBytes)
	return KeyID(base64.RawURLEncoding.EncodeToString(hash[:12]))
}

// ES256KeyGenerator generates ECDSA P-256 keys for ES256 signing.
type ES256KeyGenerator struct{}

func (ES256KeyGenerator) GenerateKey() (DomainKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return DomainKey{}, err
	}
	return DomainKey{
		Signer:    key,
		KeyID:     GenerateKeyID(key.Public()),
		Algorithm: jose.ES256,
	}, nil
}
