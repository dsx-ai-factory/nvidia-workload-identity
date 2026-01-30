// Package postgres provides a PostgreSQL-backed KeyStore implementation.
package postgres

import (
	"context"
	"crypto"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-jose/go-jose/v4"
	_ "github.com/lib/pq"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

// KeyStore implements keystore.KeyStore with encrypted storage in PostgreSQL.
// Domain signing keys are encrypted as JWE using a master encryption keyset.
type KeyStore struct {
	db        *sql.DB
	keygen    keystore.KeyGenerator
	encryptor *keystore.JWKSEncryptor
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
//
// Parameters:
//   - cfg: PostgreSQL connection configuration
//   - encryptor: JWKSEncryptor for encrypting/decrypting domain keys
//   - keygen: Generator for domain signing keys
func New(ctx context.Context, cfg Config, encryptor *keystore.JWKSEncryptor, keygen keystore.KeyGenerator) (*KeyStore, error) {
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

// domainKeyRow represents a row in the domain_keys table.
// Each row stores a single signing key for a trust domain, encrypted as JWE.
type domainKeyRow struct {
	TrustDomain  string
	KeyID        string
	Algorithm    string
	EncryptedKey string // JWE compact serialization of PKCS8-encoded private key
	MasterKeyID  string // Key ID of the master key used to encrypt this row
	IsActive     bool
	CreatedAt    time.Time
}

// generateKey creates a new active signing key and returns it as a domainKeyRow ready for insertion.
func (s *KeyStore) generateKey(trustDomain string) (*domainKeyRow, keystore.DomainKey, error) {
	dk, err := s.keygen.GenerateKey()
	if err != nil {
		return nil, keystore.DomainKey{}, fmt.Errorf("failed to generate key: %w", err)
	}

	jwe, masterKeyID, err := s.encryptKey(dk.Signer)
	if err != nil {
		return nil, keystore.DomainKey{}, err
	}

	row := &domainKeyRow{
		TrustDomain:  trustDomain,
		KeyID:        string(dk.KeyID),
		Algorithm:    string(dk.Algorithm),
		EncryptedKey: jwe,
		MasterKeyID:  masterKeyID,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}

	return row, dk, nil
}

// encryptKey serializes a signing key to PKCS8 and encrypts it as JWE.
// Returns the JWE compact serialization and the master key ID used.
func (s *KeyStore) encryptKey(signer crypto.Signer) (string, string, error) {
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %w", err)
	}

	jwe, keyID, err := s.encryptor.Encrypt(pkcs8Bytes)
	if err != nil {
		return "", "", fmt.Errorf("failed to encrypt key: %w", err)
	}

	return jwe, keyID, nil
}

// decryptKey decrypts and parses a signing key from a domainKeyRow.
func (s *KeyStore) decryptKey(row *domainKeyRow) (keystore.DomainKey, error) {
	pkcs8Bytes, err := s.encryptor.Decrypt(row.EncryptedKey)
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
		INSERT INTO domain_keys (trust_domain, key_id, algorithm, encrypted_key, master_key_id, is_active, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING`,
		row.TrustDomain, row.KeyID, row.Algorithm, row.EncryptedKey, row.MasterKeyID, row.IsActive, row.CreatedAt)
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

	slog.Info("Created new active key for trust domain", "trustDomain", trustDomain, "keyID", row.KeyID, "masterKeyID", row.MasterKeyID)
	return dk, nil
}

// getActiveKey retrieves the active signing key from the database.
func (s *KeyStore) getActiveKey(trustDomain string) (keystore.DomainKey, error) {
	row := &domainKeyRow{}
	err := s.db.QueryRow(`
		SELECT trust_domain, key_id, algorithm, encrypted_key, master_key_id, is_active, created_at
		FROM domain_keys
		WHERE trust_domain = $1 AND is_active = TRUE`,
		trustDomain).Scan(&row.TrustDomain, &row.KeyID, &row.Algorithm, &row.EncryptedKey, &row.MasterKeyID, &row.IsActive, &row.CreatedAt)
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
		SELECT trust_domain, key_id, algorithm, encrypted_key, master_key_id, is_active, created_at
		FROM domain_keys
		WHERE trust_domain = $1
		ORDER BY created_at DESC`,
		trustDomain)
	if err != nil {
		return nil, fmt.Errorf("failed to query keys: %w", err)
	}
	defer rows.Close()

	var keys []keystore.DomainKey
	for rows.Next() {
		row := &domainKeyRow{}
		if err := rows.Scan(&row.TrustDomain, &row.KeyID, &row.Algorithm, &row.EncryptedKey, &row.MasterKeyID, &row.IsActive, &row.CreatedAt); err != nil {
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
