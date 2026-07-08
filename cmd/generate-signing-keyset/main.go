package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	// Register SHA-256 for go-jose's crypto.Hash-based JWK thumbprints.
	_ "crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/go-jose/go-jose/v4"
)

type generatedKeyset struct {
	ActiveKID string             `json:"active_kid"`
	JWKS      jose.JSONWebKeySet `json:"jwks"`
}

func generate() (generatedKeyset, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return generatedKeyset{}, fmt.Errorf("generate P-256 key: %w", err)
	}
	public := jose.JSONWebKey{Key: &key.PublicKey}
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		return generatedKeyset{}, fmt.Errorf("compute public JWK thumbprint: %w", err)
	}
	// Deriving kid from public material makes independently generated key IDs
	// stable and globally collision-resistant without a separate ID allocator.
	kid := base64.RawURLEncoding.EncodeToString(thumbprint)
	return generatedKeyset{
		ActiveKID: kid,
		JWKS: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key,
			KeyID:     kid,
			Use:       "sig",
			Algorithm: string(jose.ES256),
		}}},
	}, nil
}

func main() {
	result, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
