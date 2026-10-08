// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package signingkey_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-jose/go-jose/v4"

	"dsx-identity/internal/keytest"
	"dsx-identity/internal/signingkey"
)

func TestRSAPEMAlgorithm(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, err := signingkey.ParsePrivate(string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})), jose.PS384)
	if err != nil {
		t.Fatal(err)
	}
	if got := jose.SignatureAlgorithm(key.Algorithm); got != jose.PS384 {
		t.Fatalf("algorithm = %q, want %q", got, jose.PS384)
	}
}

func TestOpenBaoEd25519PEMExport(t *testing.T) {
	t.Parallel()

	// Captured from OpenBao 2.5.2 Transit signing-key export with format=pem.
	const exported = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEIMqiR9EufBazVzAuhjsm7BUOy0iemEmUJCqxoN5qddp6
-----END PRIVATE KEY-----`
	_, err := signingkey.ParsePrivate(exported, jose.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidSigningConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("empty keyset", func(t *testing.T) {
		keysetPath, kidPath := keytest.WriteSigningFiles(t, nil, "active")
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("empty keyset was accepted")
		}
	})
	t.Run("duplicate kid", func(t *testing.T) {
		key := keytest.TestJWK(t, "", false)
		keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{key, key}, keytest.SigningKID(t, key))
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("duplicate kid was accepted")
		}
	})
	t.Run("unknown active kid", func(t *testing.T) {
		keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{keytest.TestJWK(t, "", false)}, "missing")
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("unknown active kid was accepted")
		}
	})
	t.Run("incompatible algorithm", func(t *testing.T) {
		key := keytest.TestJWK(t, "", false)
		keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{key}, keytest.SigningKID(t, key))
		if err := os.WriteFile(kidPath, []byte(`{"active_kid":"`+keytest.SigningKID(t, key)+`","alg":"RS256"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("algorithm incompatible with its key was accepted")
		}
	})
	t.Run("public key", func(t *testing.T) {
		key := keytest.TestJWK(t, "", false)
		der, err := x509.MarshalPKIXPublicKey(key.Public().Key)
		if err != nil {
			t.Fatal(err)
		}
		keysetPath := keytest.WriteRawSigningFile(t, keytest.PrivateKeyset{Keys: []string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}})
		if _, err := signingkey.NewReloader(keysetPath, keytest.WriteActiveKeysetFile(t, keytest.SigningKID(t, key), jose.ES256)); err == nil {
			t.Fatal("public key was accepted")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{keytest.TestJWK(t, "", false)}, "active")
		if err := os.WriteFile(keysetPath, []byte(`{"keys":[`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("malformed keyset was accepted")
		}
	})
	t.Run("malformed active kid JSON", func(t *testing.T) {
		key := keytest.TestJWK(t, "", false)
		keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{key}, keytest.SigningKID(t, key))
		if err := os.WriteFile(kidPath, []byte(`{"active_kid":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("malformed active kid document was accepted")
		}
	})
	t.Run("plain active kid", func(t *testing.T) {
		key := keytest.TestJWK(t, "", false)
		kid := keytest.SigningKID(t, key)
		keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{key}, kid)
		if err := os.WriteFile(kidPath, []byte(kid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := signingkey.NewReloader(keysetPath, kidPath); err == nil {
			t.Fatal("plain active kid was accepted")
		}
	})
	t.Run("missing files", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := signingkey.NewReloader(filepath.Join(dir, "missing-keyset"), filepath.Join(dir, "missing-kid")); err == nil {
			t.Fatal("missing signing files were accepted")
		}
	})
}
