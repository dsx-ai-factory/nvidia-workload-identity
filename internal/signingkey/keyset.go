package signingkey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/go-jose/go-jose/v4"
)

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

func parseSigningKeys(rawKeyset, rawKID []byte) (*Snapshot, error) {
	var activeDocument activeKeysetDocument
	if err := json.Unmarshal(rawKID, &activeDocument); err != nil {
		return nil, fmt.Errorf("parse active kid: %w", err)
	}
	if err := ValidateKeyID(activeDocument.ActiveKID); err != nil {
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
		key, err := ParsePrivate(encoded, activeDocument.Algorithm)
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
	return newSnapshot(activeDocument.ActiveKID, signer, public), nil
}

func ParsePrivate(encoded string, algorithm jose.SignatureAlgorithm) (jose.JSONWebKey, error) {
	key, err := ParsePrivatePEM(encoded)
	if err != nil {
		return jose.JSONWebKey{}, err
	}
	key.Algorithm = string(algorithm)
	key.Use = "sig"
	if err := Validate(&key, false); err != nil {
		return jose.JSONWebKey{}, err
	}
	return key, nil
}

func Validate(key *jose.JSONWebKey, publicOnly bool) error {
	if !key.Valid() {
		return fmt.Errorf("invalid JWK")
	}
	if err := ValidateKeyID(key.KeyID); err != nil {
		return err
	}
	if key.Use != "" && key.Use != "sig" {
		return fmt.Errorf("kid %q has unsupported use %q", key.KeyID, key.Use)
	}
	if publicOnly && !key.IsPublic() {
		return fmt.Errorf("kid %q contains private or symmetric key material", key.KeyID)
	}
	if !slices.Contains(supportedSignatureAlgorithms(key.Key), jose.SignatureAlgorithm(key.Algorithm)) {
		return fmt.Errorf("kid %q uses algorithm %q incompatible with its key", key.KeyID, key.Algorithm)
	}
	return nil
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

func ValidateKeyID(keyID string) error {
	// A kid occupies one NATS subject token. Wildcards and separators would
	// change subscription scope instead of naming a key.
	if keyID == "" || strings.ContainsAny(keyID, ".>* \t\r\n") {
		return fmt.Errorf("kid %q is not a valid NATS subject token", keyID)
	}
	return nil
}
