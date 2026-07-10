package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	// Register SHA-256 for go-jose's crypto.Hash-based JWK thumbprints.
	_ "crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-jose/go-jose/v4"

	"dsx-identity/internal/telemetry"
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

func run() error {
	var logLevel slog.Level
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	flag.Parse()

	if _, err := telemetry.Setup(context.Background(), "generate-signing-keyset", os.Stderr, logLevel); err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}

	result, err := generate()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fmt.Errorf("write signing keyset: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("Generate signing keyset", "error", err)
		os.Exit(1)
	}
}
