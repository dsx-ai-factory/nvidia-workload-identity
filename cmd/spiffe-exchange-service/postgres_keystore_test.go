package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
)

func TestKeyEncryptor_RoundTrip(t *testing.T) {
	// Generate a valid 32-byte key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(keyBytes)

	encryptor, err := NewKeyEncryptor(keyB64)
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
			name:      "empty message",
			plaintext: []byte{},
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
			ciphertext, err := encryptor.Encrypt(tc.plaintext)
			if err != nil {
				t.Fatalf("encryption failed: %v", err)
			}

			// Ciphertext should be different from plaintext (except for empty)
			if len(tc.plaintext) > 0 && bytes.Equal(ciphertext, tc.plaintext) {
				t.Error("ciphertext should differ from plaintext")
			}

			// Ciphertext should be longer than plaintext (nonce + tag)
			if len(ciphertext) <= len(tc.plaintext) {
				t.Error("ciphertext should be longer than plaintext")
			}

			decrypted, err := encryptor.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("decryption failed: %v", err)
			}

			if !bytes.Equal(decrypted, tc.plaintext) {
				t.Errorf("decrypted = %v, want %v", decrypted, tc.plaintext)
			}
		})
	}
}

func TestKeyEncryptor_DifferentCiphertexts(t *testing.T) {
	// Ensure each encryption produces different ciphertext (due to random nonce)
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(keyBytes)

	encryptor, err := NewKeyEncryptor(keyB64)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	plaintext := []byte("test message")

	ct1, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("first encryption failed: %v", err)
	}

	ct2, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("second encryption failed: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("same plaintext should produce different ciphertexts")
	}
}

func TestKeyEncryptor_InvalidKey(t *testing.T) {
	tests := []struct {
		name   string
		keyB64 string
	}{
		{
			name:   "invalid base64",
			keyB64: "not valid base64!!!",
		},
		{
			name:   "too short",
			keyB64: base64.StdEncoding.EncodeToString([]byte("short")),
		},
		{
			name:   "too long",
			keyB64: base64.StdEncoding.EncodeToString(make([]byte, 64)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewKeyEncryptor(tc.keyB64)
			if err == nil {
				t.Error("expected error for invalid key")
			}
		})
	}
}

func TestKeyEncryptor_DecryptInvalid(t *testing.T) {
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(keyBytes)

	encryptor, err := NewKeyEncryptor(keyB64)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	tests := []struct {
		name       string
		ciphertext []byte
	}{
		{
			name:       "too short",
			ciphertext: []byte("short"),
		},
		{
			name:       "corrupted",
			ciphertext: make([]byte, 50),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encryptor.Decrypt(tc.ciphertext)
			if err == nil {
				t.Error("expected error for invalid ciphertext")
			}
		})
	}
}

func TestKeyEncryptor_WrongKey(t *testing.T) {
	// Encrypt with one key, try to decrypt with another
	keyBytes1 := make([]byte, 32)
	keyBytes2 := make([]byte, 32)
	if _, err := rand.Read(keyBytes1); err != nil {
		t.Fatalf("failed to generate key1: %v", err)
	}
	if _, err := rand.Read(keyBytes2); err != nil {
		t.Fatalf("failed to generate key2: %v", err)
	}

	encryptor1, _ := NewKeyEncryptor(base64.StdEncoding.EncodeToString(keyBytes1))
	encryptor2, _ := NewKeyEncryptor(base64.StdEncoding.EncodeToString(keyBytes2))

	ciphertext, _ := encryptor1.Encrypt([]byte("secret"))

	_, err := encryptor2.Decrypt(ciphertext)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestKeyEncryptor_PKCS8RoundTrip(t *testing.T) {
	// Generate encryption key
	encKeyBytes := make([]byte, 32)
	if _, err := rand.Read(encKeyBytes); err != nil {
		t.Fatalf("failed to generate encryption key: %v", err)
	}
	encryptor, err := NewKeyEncryptor(base64.StdEncoding.EncodeToString(encKeyBytes))
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	// Generate signing key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Serialize using PKCS8 (same as postgres_keystore.go)
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("failed to marshal PKCS8: %v", err)
	}

	// Encrypt
	encrypted, err := encryptor.Encrypt(pkcs8Bytes)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// Decrypt
	decrypted, err := encryptor.Decrypt(encrypted)
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

func TestPostgresConfig_DSN(t *testing.T) {
	tests := []struct {
		name     string
		cfg      PostgresConfig
		expected string
	}{
		{
			name: "explicit sslmode",
			cfg: PostgresConfig{
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				User:     "testuser",
				Password: "testpass",
				SSLMode:  "require",
			},
			expected: "host=localhost port=5432 dbname=testdb user=testuser password=testpass sslmode=require",
		},
		{
			name: "default sslmode",
			cfg: PostgresConfig{
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				User:     "testuser",
				Password: "testpass",
			},
			expected: "host=localhost port=5432 dbname=testdb user=testuser password=testpass sslmode=disable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.DSN(); got != tc.expected {
				t.Errorf("DSN() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestNewPostgresKeyStore_InvalidEncryptionKey(t *testing.T) {
	cfg := PostgresConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		User:     "testuser",
		Password: "testpass",
	}

	_, err := NewPostgresKeyStore(context.Background(), cfg, "invalid-key", ES256KeyGenerator{})
	if err == nil {
		t.Error("expected error for invalid encryption key")
	}
}
