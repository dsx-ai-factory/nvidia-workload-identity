package keystore

import (
	"encoding/json"
	"fmt"

	"github.com/go-jose/go-jose/v4"
)

// JWKSEncryptor encrypts and decrypts data using a JWKS containing symmetric keys.
//
// The JWKS must contain symmetric keys (kty: oct) with A256GCM algorithm.
// One key is designated as "active" for new encryptions; all keys are available
// for decryption to support key rotation.
//
// Encryption produces JWE compact serialization with:
//   - Algorithm: dir (direct encryption)
//   - Encryption: A256GCM
//   - kid header: identifies which key was used
//
// Key Rotation:
//  1. Add new key to JWKS with new kid
//  2. Update active key ID to the new kid
//  3. Restart service - new encryptions use new key
//  4. Old keys remain for decrypting existing data
type JWKSEncryptor struct {
	activeKeyID string
	keys        map[string]jose.JSONWebKey
}

// NewJWKSEncryptor creates a new encryptor from a JWKS JSON string.
//
// The JWKS must contain symmetric keys with:
//   - kty: "oct"
//   - alg: "A256GCM"
//   - k: base64url-encoded 32-byte key
//   - kid: unique key identifier
//
// The activeKeyID must match a kid in the JWKS.
func NewJWKSEncryptor(jwksJSON string, activeKeyID string) (*JWKSEncryptor, error) {
	var jwks jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(jwksJSON), &jwks); err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}

	if len(jwks.Keys) == 0 {
		return nil, fmt.Errorf("JWKS contains no keys")
	}

	keys := make(map[string]jose.JSONWebKey, len(jwks.Keys))
	for _, key := range jwks.Keys {
		if key.KeyID == "" {
			return nil, fmt.Errorf("key missing kid")
		}
		if _, ok := key.Key.([]byte); !ok {
			return nil, fmt.Errorf("key %s is not a symmetric key (kty: oct)", key.KeyID)
		}
		keyBytes := key.Key.([]byte)
		if len(keyBytes) != 32 {
			return nil, fmt.Errorf("key %s must be 32 bytes, got %d", key.KeyID, len(keyBytes))
		}
		if key.Algorithm != string(jose.A256GCM) {
			return nil, fmt.Errorf("key %s must have alg A256GCM, got %s", key.KeyID, key.Algorithm)
		}
		keys[key.KeyID] = key
	}

	if _, ok := keys[activeKeyID]; !ok {
		return nil, fmt.Errorf("active key ID %q not found in JWKS", activeKeyID)
	}

	return &JWKSEncryptor{
		activeKeyID: activeKeyID,
		keys:        keys,
	}, nil
}

// ActiveKeyID returns the key ID used for new encryptions.
func (e *JWKSEncryptor) ActiveKeyID() string {
	return e.activeKeyID
}

// Encrypt encrypts plaintext using the active key.
// Returns the JWE compact serialization and the key ID used.
func (e *JWKSEncryptor) Encrypt(plaintext []byte) (string, string, error) {
	key := e.keys[e.activeKeyID]

	encrypter, err := jose.NewEncrypter(
		jose.A256GCM,
		jose.Recipient{
			Algorithm: jose.DIRECT,
			Key:       key.Key,
			KeyID:     key.KeyID,
		},
		(&jose.EncrypterOptions{}).WithContentType("application/octet-stream"),
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to create encrypter: %w", err)
	}

	jwe, err := encrypter.Encrypt(plaintext)
	if err != nil {
		return "", "", fmt.Errorf("failed to encrypt: %w", err)
	}

	compact, err := jwe.CompactSerialize()
	if err != nil {
		return "", "", fmt.Errorf("failed to serialize JWE: %w", err)
	}

	return compact, e.activeKeyID, nil
}

// Decrypt decrypts a JWE compact serialization.
// The key is looked up by the kid in the JWE header.
func (e *JWKSEncryptor) Decrypt(jweCompact string) ([]byte, error) {
	jwe, err := jose.ParseEncrypted(jweCompact, []jose.KeyAlgorithm{jose.DIRECT}, []jose.ContentEncryption{jose.A256GCM})
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWE: %w", err)
	}

	kid := jwe.Header.KeyID
	if kid == "" {
		return nil, fmt.Errorf("JWE missing kid header")
	}

	key, ok := e.keys[kid]
	if !ok {
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}

	plaintext, err := jwe.Decrypt(key.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
