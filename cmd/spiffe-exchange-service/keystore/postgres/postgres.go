// Package postgres provides a PostgreSQL-backed KeyStore implementation.
package postgres

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/go-jose/go-jose/v4"
	_ "github.com/lib/pq"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

// KeyStore implements keystore.KeyStore with encrypted storage in PostgreSQL.
// Keys are encrypted with AES-256-GCM before being stored.
type KeyStore struct {
	db        *sql.DB
	keygen    keystore.KeyGenerator
	encryptor *Encryptor
}

// Config holds connection parameters for PostgreSQL.
type Config struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string // default "disable" for Kind clusters
}

// DSN returns the PostgreSQL connection string.
func (c Config) DSN() string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		c.Host, c.Port, c.Database, c.User, c.Password, sslMode)
}

// New creates a new Postgres-backed key store.
// The encryptionKeyB64 must be a base64-encoded 32-byte key for AES-256-GCM.
func New(ctx context.Context, cfg Config, encryptionKeyB64 string, keygen keystore.KeyGenerator) (*KeyStore, error) {
	encryptor, err := NewEncryptor(encryptionKeyB64)
	if err != nil {
		return nil, fmt.Errorf("invalid encryption key: %w", err)
	}

	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	store := &KeyStore{
		db:        db,
		keygen:    keygen,
		encryptor: encryptor,
	}

	return store, nil
}

// Close closes the database connection.
func (s *KeyStore) Close() error {
	return s.db.Close()
}

// keysetRow represents a row in the keysets table.
type keysetRow struct {
	TrustDomain     string
	KeyID           string
	Algorithm       string
	EncryptedKeyset []byte // AES-256-GCM encrypted PKCS8 private key bytes
	IsActive        bool
	CreatedAt       time.Time
}

// generateKey creates a new active signing key and returns it as a keysetRow ready for insertion.
func (s *KeyStore) generateKey(trustDomain string) (*keysetRow, keystore.DomainKey, error) {
	dk, err := s.keygen.GenerateKey()
	if err != nil {
		return nil, keystore.DomainKey{}, fmt.Errorf("failed to generate key: %w", err)
	}

	encrypted, err := s.encryptKey(dk.Signer)
	if err != nil {
		return nil, keystore.DomainKey{}, err
	}

	row := &keysetRow{
		TrustDomain:     trustDomain,
		KeyID:           string(dk.KeyID),
		Algorithm:       string(dk.Algorithm),
		EncryptedKeyset: encrypted,
		IsActive:        true,
		CreatedAt:       time.Now().UTC(),
	}

	return row, dk, nil
}

// encryptKey serializes a signing key to PKCS8 and encrypts it for storage.
func (s *KeyStore) encryptKey(signer crypto.Signer) ([]byte, error) {
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	encrypted, err := s.encryptor.Encrypt(pkcs8Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt key: %w", err)
	}

	return encrypted, nil
}

// decryptKey decrypts and parses a signing key from a keysetRow.
func (s *KeyStore) decryptKey(row *keysetRow) (keystore.DomainKey, error) {
	pkcs8Bytes, err := s.encryptor.Decrypt(row.EncryptedKeyset)
	if err != nil {
		return keystore.DomainKey{}, fmt.Errorf("failed to decrypt key: %w", err)
	}

	key, err := x509.ParsePKCS8PrivateKey(pkcs8Bytes)
	if err != nil {
		return keystore.DomainKey{}, fmt.Errorf("failed to parse private key: %w", err)
	}

	signer, ok := key.(crypto.Signer)
	if !ok {
		return keystore.DomainKey{}, fmt.Errorf("key is not a signer")
	}

	return keystore.DomainKey{
		Signer:    signer,
		KeyID:     keystore.KeyID(row.KeyID),
		Algorithm: jose.SignatureAlgorithm(row.Algorithm),
	}, nil
}

// GetOrCreateKey returns the active key for a trust domain, creating a new key
// pair and marking it active if none exists.
// Synchronization is enforced by a partial unique index on (trust_domain) WHERE is_active = TRUE.
func (s *KeyStore) GetOrCreateKey(trustDomain string) (keystore.DomainKey, error) {
	// Try to get existing active key first
	dk, err := s.getActiveKey(trustDomain)
	if err == nil {
		return dk, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return keystore.DomainKey{}, err
	}

	// No active key exists, generate and try to insert
	row, dk, err := s.generateKey(trustDomain)
	if err != nil {
		return keystore.DomainKey{}, err
	}

	// Insert with ON CONFLICT DO NOTHING - the partial unique index ensures only one active key.
	result, err := s.db.Exec(`
		INSERT INTO keysets (trust_domain, key_id, algorithm, encrypted_keyset, is_active, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING`,
		row.TrustDomain, row.KeyID, row.Algorithm, row.EncryptedKeyset, row.IsActive, row.CreatedAt)
	if err != nil {
		return keystore.DomainKey{}, fmt.Errorf("failed to insert key: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return keystore.DomainKey{}, fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// Another replica inserted first, read the active key
		dk, err = s.getActiveKey(trustDomain)
		if err != nil {
			return keystore.DomainKey{}, fmt.Errorf("failed to read active key after conflict: %w", err)
		}
		return dk, nil
	}

	slog.Info("Created new active key for trust domain", "trustDomain", trustDomain, "keyID", row.KeyID)
	return dk, nil
}

// getActiveKey retrieves the active signing key from the database.
func (s *KeyStore) getActiveKey(trustDomain string) (keystore.DomainKey, error) {
	row := &keysetRow{}
	err := s.db.QueryRow(`
		SELECT trust_domain, key_id, algorithm, encrypted_keyset, is_active, created_at
		FROM keysets
		WHERE trust_domain = $1 AND is_active = TRUE`,
		trustDomain).Scan(&row.TrustDomain, &row.KeyID, &row.Algorithm, &row.EncryptedKeyset, &row.IsActive, &row.CreatedAt)
	if err != nil {
		return keystore.DomainKey{}, err
	}

	return s.decryptKey(row)
}

// GetKeys returns all keys for a trust domain (for JWKS endpoint).
// Creates an active key if none exist.
func (s *KeyStore) GetKeys(trustDomain string) ([]keystore.DomainKey, error) {
	// Ensure at least one active key exists
	_, err := s.GetOrCreateKey(trustDomain)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT trust_domain, key_id, algorithm, encrypted_keyset, is_active, created_at
		FROM keysets
		WHERE trust_domain = $1
		ORDER BY created_at DESC`,
		trustDomain)
	if err != nil {
		return nil, fmt.Errorf("failed to query keys: %w", err)
	}
	defer rows.Close()

	var keys []keystore.DomainKey
	for rows.Next() {
		row := &keysetRow{}
		if err := rows.Scan(&row.TrustDomain, &row.KeyID, &row.Algorithm, &row.EncryptedKeyset, &row.IsActive, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan key row: %w", err)
		}

		dk, err := s.decryptKey(row)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt key %s: %w", row.KeyID, err)
		}
		keys = append(keys, dk)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating keys: %w", err)
	}

	return keys, nil
}

// Encryptor handles AES-256-GCM encryption/decryption.
type Encryptor struct {
	gcm cipher.AEAD
}

// NewEncryptor creates a new encryptor from a base64-encoded 32-byte key.
func NewEncryptor(keyB64 string) (*Encryptor, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 key: %w", err)
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	return &Encryptor{gcm: gcm}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM.
// The returned ciphertext includes the nonce prepended.
func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := e.gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts ciphertext that was encrypted with Encrypt.
// Expects the nonce to be prepended to the ciphertext.
func (e *Encryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	nonceSize := e.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
