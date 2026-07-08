package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	_ "crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestGenerateKeyset(t *testing.T) {
	t.Parallel()
	result, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.JWKS.Keys) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	key := result.JWKS.Keys[0]
	privateKey, ok := key.Key.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve != elliptic.P256() || privateKey.D.Sign() <= 0 ||
		key.KeyID != result.ActiveKID || key.Algorithm != "ES256" || key.Use != "sig" {
		t.Fatalf("invalid generated JWK: %#v", key)
	}
	public := key.Public()
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if want := base64.RawURLEncoding.EncodeToString(thumbprint); result.ActiveKID != want {
		t.Fatalf("active kid = %q, want public JWK thumbprint %q", result.ActiveKID, want)
	}
	other, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if other.ActiveKID == result.ActiveKID {
		t.Fatal("independently generated keys reused a kid")
	}
}
