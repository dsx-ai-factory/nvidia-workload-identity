package keystore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

// generateTestKey generates a random 32-byte symmetric key.
func generateTestKey() []byte {
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		panic(err)
	}
	return keyBytes
}

// makeJWKS creates a JWKS JSON string from key ID to key bytes mappings.
func makeJWKS(keys map[string][]byte) string {
	jwks := jose.JSONWebKeySet{
		Keys: make([]jose.JSONWebKey, 0, len(keys)),
	}
	for kid, keyBytes := range keys {
		jwks.Keys = append(jwks.Keys, jose.JSONWebKey{
			Key:       keyBytes,
			KeyID:     kid,
			Algorithm: string(jose.A256GCM),
			Use:       "enc",
		})
	}
	data, err := json.Marshal(jwks)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestJWKSEncryptor_RoundTrip(t *testing.T) {
	jwks := makeJWKS(map[string][]byte{"key-1": generateTestKey()})

	encryptor, err := NewJWKSEncryptor(jwks, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	testCases := []struct {
		name      string
		plaintext []byte
	}{
		{
			name:      "short message",
			plaintext: []byte("hello world"),
		},
		{
			name: "binary data",
			plaintext: func() []byte {
				data := make([]byte, 256)
				for i := range data {
					data[i] = byte(i)
				}
				return data
			}(),
		},
		{
			name:      "large message",
			plaintext: bytes.Repeat([]byte("x"), 10000),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jwe, keyID, err := encryptor.Encrypt(tc.plaintext)
			if err != nil {
				t.Fatalf("encryption failed: %v", err)
			}

			if keyID != "key-1" {
				t.Errorf("keyID = %q, want %q", keyID, "key-1")
			}

			// JWE should be a valid compact serialization (5 dot-separated parts)
			parts := strings.Split(jwe, ".")
			if len(parts) != 5 {
				t.Errorf("JWE should have 5 parts, got %d", len(parts))
			}

			decrypted, err := encryptor.Decrypt(jwe)
			if err != nil {
				t.Fatalf("decryption failed: %v", err)
			}

			if !bytes.Equal(decrypted, tc.plaintext) {
				t.Errorf("decrypted = %v, want %v", decrypted, tc.plaintext)
			}
		})
	}
}

func TestJWKSEncryptor_DifferentCiphertexts(t *testing.T) {
	jwks := makeJWKS(map[string][]byte{"key-1": generateTestKey()})

	encryptor, err := NewJWKSEncryptor(jwks, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	plaintext := []byte("test message")

	jwe1, _, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("first encryption failed: %v", err)
	}

	jwe2, _, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("second encryption failed: %v", err)
	}

	if jwe1 == jwe2 {
		t.Error("same plaintext should produce different JWEs")
	}
}

func TestJWKSEncryptor_MultiKeyDecrypt(t *testing.T) {
	key1 := generateTestKey()
	key2 := generateTestKey()
	jwks := makeJWKS(map[string][]byte{"key-1": key1, "key-2": key2})

	// Create encryptor with key-1 as active
	encryptor1, err := NewJWKSEncryptor(jwks, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor1: %v", err)
	}

	// Encrypt with key-1
	jwe1, keyID1, err := encryptor1.Encrypt([]byte("secret1"))
	if err != nil {
		t.Fatalf("encryption with key-1 failed: %v", err)
	}
	if keyID1 != "key-1" {
		t.Errorf("keyID1 = %q, want %q", keyID1, "key-1")
	}

	// Create encryptor with key-2 as active
	encryptor2, err := NewJWKSEncryptor(jwks, "key-2")
	if err != nil {
		t.Fatalf("failed to create encryptor2: %v", err)
	}

	// Encrypt with key-2
	jwe2, keyID2, err := encryptor2.Encrypt([]byte("secret2"))
	if err != nil {
		t.Fatalf("encryption with key-2 failed: %v", err)
	}
	if keyID2 != "key-2" {
		t.Errorf("keyID2 = %q, want %q", keyID2, "key-2")
	}

	// Both encryptors should be able to decrypt both JWEs
	decrypted1, err := encryptor1.Decrypt(jwe1)
	if err != nil {
		t.Fatalf("encryptor1 failed to decrypt jwe1: %v", err)
	}
	if string(decrypted1) != "secret1" {
		t.Errorf("decrypted1 = %q, want %q", decrypted1, "secret1")
	}

	decrypted2, err := encryptor1.Decrypt(jwe2)
	if err != nil {
		t.Fatalf("encryptor1 failed to decrypt jwe2: %v", err)
	}
	if string(decrypted2) != "secret2" {
		t.Errorf("decrypted2 = %q, want %q", decrypted2, "secret2")
	}

	decrypted1Again, err := encryptor2.Decrypt(jwe1)
	if err != nil {
		t.Fatalf("encryptor2 failed to decrypt jwe1: %v", err)
	}
	if string(decrypted1Again) != "secret1" {
		t.Errorf("decrypted1Again = %q, want %q", decrypted1Again, "secret1")
	}
}

func TestJWKSEncryptor_InvalidJWKS(t *testing.T) {
	validKeyBytes := generateTestKey()
	validKeyB64 := base64.RawURLEncoding.EncodeToString(validKeyBytes)

	tests := []struct {
		name        string
		jwks        string
		activeKeyID string
		wantErr     string
	}{
		{
			name:        "invalid JSON",
			jwks:        "not valid json",
			activeKeyID: "key-1",
			wantErr:     "failed to parse JWKS",
		},
		{
			name:        "empty keys",
			jwks:        `{"keys":[]}`,
			activeKeyID: "key-1",
			wantErr:     "JWKS contains no keys",
		},
		{
			name:        "missing kid",
			jwks:        `{"keys":[{"kty":"oct","k":"` + validKeyB64 + `","alg":"A256GCM","use":"enc"}]}`,
			activeKeyID: "key-1",
			wantErr:     "key missing kid",
		},
		{
			name:        "wrong key type",
			jwks:        `{"keys":[{"kty":"RSA","kid":"key-1","alg":"A256GCM","use":"enc"}]}`,
			activeKeyID: "key-1",
			wantErr:     "invalid RSA key",
		},
		{
			name:        "wrong key size",
			jwks:        `{"keys":[{"kty":"oct","kid":"key-1","k":"` + base64.RawURLEncoding.EncodeToString([]byte("short")) + `","alg":"A256GCM","use":"enc"}]}`,
			activeKeyID: "key-1",
			wantErr:     "must be 32 bytes",
		},
		{
			name:        "wrong algorithm",
			jwks:        `{"keys":[{"kty":"oct","kid":"key-1","k":"` + validKeyB64 + `","alg":"A128GCM","use":"enc"}]}`,
			activeKeyID: "key-1",
			wantErr:     "must have alg A256GCM",
		},
		{
			name:        "active key not found",
			jwks:        makeJWKS(map[string][]byte{"key-1": validKeyBytes}),
			activeKeyID: "key-2",
			wantErr:     "not found in JWKS",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewJWKSEncryptor(tc.jwks, tc.activeKeyID)
			if err == nil {
				t.Error("expected error")
				return
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestJWKSEncryptor_DecryptInvalidJWE(t *testing.T) {
	jwks := makeJWKS(map[string][]byte{"key-1": generateTestKey()})

	encryptor, err := NewJWKSEncryptor(jwks, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	tests := []struct {
		name    string
		jwe     string
		wantErr string
	}{
		{
			name:    "not a JWE",
			jwe:     "not.a.valid.jwe.format",
			wantErr: "failed to parse JWE",
		},
		{
			name:    "missing kid header",
			jwe:     "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..AAAAAAAAAAAAAAAA.AAAA.AAAAAAAAAAAAAAAAAAAAAA",
			wantErr: "JWE missing kid header",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encryptor.Decrypt(tc.jwe)
			if err == nil {
				t.Error("expected error")
				return
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestJWKSEncryptor_DecryptUnknownKid(t *testing.T) {
	key1 := generateTestKey()
	key2 := generateTestKey()

	// Encrypt with key-1
	jwks1 := makeJWKS(map[string][]byte{"key-1": key1})
	encryptor1, err := NewJWKSEncryptor(jwks1, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor1: %v", err)
	}

	jwe, _, err := encryptor1.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// Try to decrypt with encryptor that only has key-2
	jwks2 := makeJWKS(map[string][]byte{"key-2": key2})
	encryptor2, err := NewJWKSEncryptor(jwks2, "key-2")
	if err != nil {
		t.Fatalf("failed to create encryptor2: %v", err)
	}

	_, err = encryptor2.Decrypt(jwe)
	if err == nil {
		t.Error("expected error when decrypting with unknown kid")
		return
	}
	if !strings.Contains(err.Error(), "not found in JWKS") {
		t.Errorf("error = %q, want containing %q", err.Error(), "not found in JWKS")
	}
}

func TestJWKSEncryptor_PKCS8RoundTrip(t *testing.T) {
	jwks := makeJWKS(map[string][]byte{"key-1": generateTestKey()})

	encryptor, err := NewJWKSEncryptor(jwks, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	// Generate signing key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Serialize using PKCS8
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("failed to marshal PKCS8: %v", err)
	}

	// Encrypt
	jwe, keyID, err := encryptor.Encrypt(pkcs8Bytes)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}
	if keyID != "key-1" {
		t.Errorf("keyID = %q, want %q", keyID, "key-1")
	}

	// Decrypt
	decrypted, err := encryptor.Decrypt(jwe)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}

	// Parse back
	key, err := x509.ParsePKCS8PrivateKey(decrypted)
	if err != nil {
		t.Fatalf("failed to parse PKCS8: %v", err)
	}

	deserializedECDSA, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("deserialized key is not ECDSA")
	}

	if !privateKey.Equal(deserializedECDSA) {
		t.Error("round-tripped key should equal original")
	}
}

func TestJWKSEncryptor_ActiveKeyID(t *testing.T) {
	key1 := generateTestKey()
	key2 := generateTestKey()
	jwks := makeJWKS(map[string][]byte{"key-1": key1, "key-2": key2})

	encryptor1, err := NewJWKSEncryptor(jwks, "key-1")
	if err != nil {
		t.Fatalf("failed to create encryptor1: %v", err)
	}
	if encryptor1.ActiveKeyID() != "key-1" {
		t.Errorf("ActiveKeyID() = %q, want %q", encryptor1.ActiveKeyID(), "key-1")
	}

	encryptor2, err := NewJWKSEncryptor(jwks, "key-2")
	if err != nil {
		t.Fatalf("failed to create encryptor2: %v", err)
	}
	if encryptor2.ActiveKeyID() != "key-2" {
		t.Errorf("ActiveKeyID() = %q, want %q", encryptor2.ActiveKeyID(), "key-2")
	}
}

// TestJWKSEncryptor_KeyRotation simulates the master key rotation workflow:
// 1. Start with one key, encrypt data
// 2. Add a new key and make it active
// 3. New encryptions use the new key
// 4. Old data encrypted with the old key can still be decrypted
func TestJWKSEncryptor_KeyRotation(t *testing.T) {
	// Phase 1: Initial deployment with mk-2024-01
	initialKey := generateTestKey()
	initialJWKS := makeJWKS(map[string][]byte{"mk-2024-01": initialKey})

	encryptorV1, err := NewJWKSEncryptor(initialJWKS, "mk-2024-01")
	if err != nil {
		t.Fatalf("failed to create initial encryptor: %v", err)
	}

	// Encrypt some data with the initial key
	secret1 := []byte("domain-key-for-tenant1.example.com")
	jwe1, keyID1, err := encryptorV1.Encrypt(secret1)
	if err != nil {
		t.Fatalf("failed to encrypt with initial key: %v", err)
	}
	if keyID1 != "mk-2024-01" {
		t.Errorf("initial encryption used wrong key: got %q, want %q", keyID1, "mk-2024-01")
	}

	secret2 := []byte("domain-key-for-tenant2.example.com")
	jwe2, _, err := encryptorV1.Encrypt(secret2)
	if err != nil {
		t.Fatalf("failed to encrypt second secret: %v", err)
	}

	// Phase 2: Key rotation - add new key mk-2024-02 and make it active
	newKey := generateTestKey()
	rotatedJWKS := makeJWKS(map[string][]byte{
		"mk-2024-01": initialKey, // Keep old key for decryption
		"mk-2024-02": newKey,     // New key for encryption
	})

	encryptorV2, err := NewJWKSEncryptor(rotatedJWKS, "mk-2024-02")
	if err != nil {
		t.Fatalf("failed to create rotated encryptor: %v", err)
	}

	// New encryptions should use the new key
	secret3 := []byte("domain-key-for-tenant3.example.com")
	jwe3, keyID3, err := encryptorV2.Encrypt(secret3)
	if err != nil {
		t.Fatalf("failed to encrypt with new key: %v", err)
	}
	if keyID3 != "mk-2024-02" {
		t.Errorf("new encryption used wrong key: got %q, want %q", keyID3, "mk-2024-02")
	}

	// Old data encrypted with mk-2024-01 should still be decryptable
	decrypted1, err := encryptorV2.Decrypt(jwe1)
	if err != nil {
		t.Fatalf("failed to decrypt old data after rotation: %v", err)
	}
	if !bytes.Equal(decrypted1, secret1) {
		t.Error("decrypted old data doesn't match original")
	}

	decrypted2, err := encryptorV2.Decrypt(jwe2)
	if err != nil {
		t.Fatalf("failed to decrypt second old secret: %v", err)
	}
	if !bytes.Equal(decrypted2, secret2) {
		t.Error("decrypted second old data doesn't match original")
	}

	// New data should also be decryptable
	decrypted3, err := encryptorV2.Decrypt(jwe3)
	if err != nil {
		t.Fatalf("failed to decrypt new data: %v", err)
	}
	if !bytes.Equal(decrypted3, secret3) {
		t.Error("decrypted new data doesn't match original")
	}
}

// TestJWKSEncryptor_MultipleKeyGenerations tests handling many key generations
// (e.g., monthly rotation over a year).
func TestJWKSEncryptor_MultipleKeyGenerations(t *testing.T) {
	// Simulate 12 months of key rotation
	keys := make(map[string][]byte)
	keyIDs := make([]string, 12)
	for i := 0; i < 12; i++ {
		kid := "mk-2024-" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		keyIDs[i] = kid
		keys[kid] = generateTestKey()
	}

	// Create encryptor with all 12 keys, latest one active
	jwks := makeJWKS(keys)
	encryptor, err := NewJWKSEncryptor(jwks, "mk-2024-11")
	if err != nil {
		t.Fatalf("failed to create encryptor with 12 keys: %v", err)
	}

	// Encrypt data that would have been created in each month
	jwes := make(map[string]string)
	secrets := make(map[string][]byte)

	for _, kid := range keyIDs {
		// Create encryptor as if that key was active at the time
		monthEncryptor, err := NewJWKSEncryptor(jwks, kid)
		if err != nil {
			t.Fatalf("failed to create encryptor for %s: %v", kid, err)
		}

		secret := []byte("secret-from-" + kid)
		secrets[kid] = secret

		jwe, usedKID, err := monthEncryptor.Encrypt(secret)
		if err != nil {
			t.Fatalf("failed to encrypt with %s: %v", kid, err)
		}
		if usedKID != kid {
			t.Errorf("encryption used wrong key: got %q, want %q", usedKID, kid)
		}
		jwes[kid] = jwe
	}

	// Current encryptor (with mk-2024-11 active) should decrypt all of them
	for kid, jwe := range jwes {
		decrypted, err := encryptor.Decrypt(jwe)
		if err != nil {
			t.Errorf("failed to decrypt data from %s: %v", kid, err)
			continue
		}
		if !bytes.Equal(decrypted, secrets[kid]) {
			t.Errorf("decrypted data from %s doesn't match", kid)
		}
	}
}

// TestJWKSEncryptor_RotationWithRemovedKey tests that removing an old key
// prevents decryption of data encrypted with that key.
func TestJWKSEncryptor_RotationWithRemovedKey(t *testing.T) {
	key1 := generateTestKey()
	key2 := generateTestKey()

	// Encrypt with key1
	jwksWithKey1 := makeJWKS(map[string][]byte{"mk-old": key1})
	encryptor1, err := NewJWKSEncryptor(jwksWithKey1, "mk-old")
	if err != nil {
		t.Fatalf("failed to create encryptor1: %v", err)
	}

	jwe, _, err := encryptor1.Encrypt([]byte("secret data"))
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	// Create new encryptor with only key2 (key1 removed - simulating key retirement)
	jwksWithoutKey1 := makeJWKS(map[string][]byte{"mk-new": key2})
	encryptor2, err := NewJWKSEncryptor(jwksWithoutKey1, "mk-new")
	if err != nil {
		t.Fatalf("failed to create encryptor2: %v", err)
	}

	// Decryption should fail because mk-old is no longer in the keyset
	_, err = encryptor2.Decrypt(jwe)
	if err == nil {
		t.Error("expected decryption to fail when key is removed from keyset")
	}
	if !strings.Contains(err.Error(), "not found in JWKS") {
		t.Errorf("unexpected error: %v", err)
	}
}
