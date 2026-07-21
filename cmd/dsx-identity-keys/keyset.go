package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"

	// Register SHA-256 for go-jose's crypto.Hash-based JWK thumbprints.
	_ "crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jellydator/ttlcache/v3"
)

type signingKeyset struct {
	activeKID string
	signer    jose.Signer
	public    []jose.JSONWebKey
}

// privateKeyset reads keyset.json as
// {"keys":["<Transit-exported private PEM>", ...]}.
// Entries may be SEC 1 EC, PKCS#1 RSA, or PKCS#8 asymmetric private keys.
type privateKeyset struct {
	Keys []string `json:"keys"`
}

// activeKeysetDocument reads active-kid.json as
// {"active_kid":"<kid>","alg":"<JOSE signing algorithm>"}.
// The kid is the RFC 7638 SHA-256 public JWK thumbprint of a key in keyset.json.
type activeKeysetDocument struct {
	ActiveKID string                  `json:"active_kid"`
	Algorithm jose.SignatureAlgorithm `json:"alg"`
}

func loadSigningKeyset(keysetPath, activeKIDPath string) (*signingKeyset, error) {
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		return nil, fmt.Errorf("read signing keyset: %w", err)
	}
	rawKID, err := os.ReadFile(activeKIDPath)
	if err != nil {
		return nil, fmt.Errorf("read active kid: %w", err)
	}
	var activeDocument activeKeysetDocument
	if err := json.Unmarshal(rawKID, &activeDocument); err != nil {
		return nil, fmt.Errorf("parse active kid: %w", err)
	}
	if err := validateKeyID(activeDocument.ActiveKID); err != nil {
		return nil, fmt.Errorf("active kid: %w", err)
	}

	var document privateKeyset
	if err := json.Unmarshal(rawKeyset, &document); err != nil {
		return nil, fmt.Errorf("parse signing keyset: %w", err)
	}
	if len(document.Keys) == 0 {
		return nil, fmt.Errorf("signing keyset contains no keys")
	}

	seen := make(map[string]struct{}, len(document.Keys))
	public := make([]jose.JSONWebKey, 0, len(document.Keys))
	var active *jose.JSONWebKey
	for i, encoded := range document.Keys {
		key, err := parsePrivateSigningKey(encoded, activeDocument.Algorithm)
		if err != nil {
			return nil, fmt.Errorf("signing keyset key %d: %w", i, err)
		}
		if _, ok := seen[key.KeyID]; ok {
			return nil, fmt.Errorf("signing keyset contains duplicate kid %q", key.KeyID)
		}
		seen[key.KeyID] = struct{}{}
		public = append(public, key.Public())
		if key.KeyID == activeDocument.ActiveKID {
			active = &key
		}
	}
	if active == nil {
		return nil, fmt.Errorf("active kid %q not found in signing keyset", activeDocument.ActiveKID)
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: activeDocument.Algorithm, Key: active.Key},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), activeDocument.ActiveKID),
	)
	if err != nil {
		return nil, fmt.Errorf("create signer for active kid %q: %w", activeDocument.ActiveKID, err)
	}
	return &signingKeyset{activeKID: activeDocument.ActiveKID, signer: signer, public: public}, nil
}

func parsePrivateSigningKey(encoded string, algorithm jose.SignatureAlgorithm) (jose.JSONWebKey, error) {
	block, rest := pem.Decode([]byte(encoded))
	if block == nil {
		return jose.JSONWebKey{}, fmt.Errorf("decode PEM")
	}
	if len(rest) != 0 {
		return jose.JSONWebKey{}, fmt.Errorf("PEM contains trailing data")
	}
	var key any
	var err error
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return jose.JSONWebKey{}, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
	if err != nil {
		return jose.JSONWebKey{}, fmt.Errorf("parse %s: %w", block.Type, err)
	}
	if !slices.Contains(supportedSignatureAlgorithms(key), algorithm) {
		return jose.JSONWebKey{}, fmt.Errorf("algorithm %q is incompatible with private key type %T", algorithm, key)
	}
	private := jose.JSONWebKey{Key: key}
	public := private.Public()
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		return jose.JSONWebKey{}, fmt.Errorf("compute public JWK thumbprint: %w", err)
	}
	kid := base64.RawURLEncoding.EncodeToString(thumbprint)
	jwk := jose.JSONWebKey{
		Key:       key,
		KeyID:     kid,
		Algorithm: string(algorithm),
		Use:       "sig",
	}
	if _, err := validateSigningKey(&jwk, false); err != nil {
		return jose.JSONWebKey{}, err
	}
	return jwk, nil
}

func validateSigningKey(key *jose.JSONWebKey, publicOnly bool) (jose.SignatureAlgorithm, error) {
	if !key.Valid() {
		return "", fmt.Errorf("invalid JWK")
	}
	if err := validateKeyID(key.KeyID); err != nil {
		return "", err
	}
	if key.Use != "" && key.Use != "sig" {
		return "", fmt.Errorf("kid %q has unsupported use %q", key.KeyID, key.Use)
	}
	if publicOnly && !key.IsPublic() {
		return "", fmt.Errorf("kid %q contains private or symmetric key material", key.KeyID)
	}
	algorithm := jose.SignatureAlgorithm(key.Algorithm)
	if !slices.Contains(supportedSignatureAlgorithms(key.Key), algorithm) {
		return "", fmt.Errorf("kid %q uses algorithm %q incompatible with its key", key.KeyID, key.Algorithm)
	}
	return algorithm, nil
}

func supportedSignatureAlgorithms(key any) []jose.SignatureAlgorithm {
	if signer, ok := key.(crypto.Signer); ok {
		return supportedSignatureAlgorithms(signer.Public())
	}
	switch material := key.(type) {
	case ed25519.PublicKey:
		return []jose.SignatureAlgorithm{jose.EdDSA}
	case *ecdsa.PublicKey:
		switch material.Curve {
		case elliptic.P256():
			return []jose.SignatureAlgorithm{jose.ES256}
		case elliptic.P384():
			return []jose.SignatureAlgorithm{jose.ES384}
		case elliptic.P521():
			return []jose.SignatureAlgorithm{jose.ES512}
		}
	case *rsa.PublicKey:
		return []jose.SignatureAlgorithm{jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512}
	}
	return nil
}

type publishedKey struct {
	key            jose.JSONWebKey
	publishedAt    time.Time
	leaseExpiresAt time.Time
}

type publicKeyPublication struct {
	PublishedAt    int64           `json:"published_at"`
	LeaseExpiresAt int64           `json:"lease_expires_at"`
	JWK            jose.JSONWebKey `json:"jwk"`
}

func marshalPublicKey(key jose.JSONWebKey, publishedAt, leaseExpiresAt time.Time) ([]byte, error) {
	if _, err := validateSigningKey(&key, true); err != nil {
		return nil, err
	}
	if !leaseExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("public key publication lease expiration must be in the future")
	}
	return json.Marshal(publicKeyPublication{
		PublishedAt:    publishedAt.Unix(),
		LeaseExpiresAt: leaseExpiresAt.Unix(),
		JWK:            key,
	})
}

func decodePublishedKey(payload []byte) (publishedKey, error) {
	var publication publicKeyPublication
	if err := json.Unmarshal(payload, &publication); err != nil {
		return publishedKey{}, err
	}
	key := publication.JWK
	if _, err := validateSigningKey(&key, true); err != nil {
		return publishedKey{}, err
	}
	if publication.PublishedAt <= 0 {
		return publishedKey{}, fmt.Errorf("public key publication timestamp is required")
	}
	publishedAt := time.Unix(publication.PublishedAt, 0)
	leaseExpiresAt := time.Unix(publication.LeaseExpiresAt, 0)
	if !publishedAt.Before(leaseExpiresAt) {
		return publishedKey{}, fmt.Errorf("public key publication timestamp must precede lease expiration")
	}
	if !leaseExpiresAt.After(time.Now()) {
		return publishedKey{}, fmt.Errorf("public key publication lease has expired")
	}
	return publishedKey{key: key, publishedAt: publishedAt, leaseExpiresAt: leaseExpiresAt}, nil
}

type keyRegistry struct {
	byKID *ttlcache.Cache[string, publishedKey]
}

func newKeyRegistry() *keyRegistry {
	return &keyRegistry{byKID: ttlcache.New[string, publishedKey](
		ttlcache.WithDisableTouchOnHit[string, publishedKey](),
	)}
}

func (r *keyRegistry) start() {
	go r.byKID.Start()
}

func (r *keyRegistry) stop() {
	r.byKID.Stop()
}

func (r *keyRegistry) contains(keyID string) bool {
	return r.byKID.Get(keyID) != nil
}

func (r *keyRegistry) replace(keyID string, publication publishedKey) error {
	if publication.key.KeyID != keyID {
		return fmt.Errorf("topic kid %q does not match JWK kid %q", keyID, publication.key.KeyID)
	}
	remaining := time.Until(publication.leaseExpiresAt)
	if remaining <= 0 {
		return fmt.Errorf("public key %q publication lease has expired", keyID)
	}
	r.byKID.Set(keyID, publication, remaining)
	return nil
}

func (r *keyRegistry) keyset() jose.JSONWebKeySet {
	items := r.byKID.Items()
	keys := make([]jose.JSONWebKey, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Value().key)
	}
	slices.SortFunc(keys, func(a, b jose.JSONWebKey) int {
		return strings.Compare(a.KeyID, b.KeyID)
	})
	return jose.JSONWebKeySet{Keys: keys}
}

func signingAlgorithms(keys jose.JSONWebKeySet) []string {
	algorithms := make(map[string]struct{}, len(keys.Keys))
	for _, key := range keys.Keys {
		algorithms[key.Algorithm] = struct{}{}
	}
	return slices.Sorted(maps.Keys(algorithms))
}

func validateKeyID(keyID string) error {
	// A kid occupies one MQTT topic segment. Wildcards and separators would
	// change subscription scope instead of naming a key.
	if !validTopicSegment(keyID) {
		return fmt.Errorf("kid %q is not a valid MQTT topic segment", keyID)
	}
	return nil
}
