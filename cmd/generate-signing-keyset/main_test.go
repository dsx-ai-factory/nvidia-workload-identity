package main

import (
	"crypto"
	"crypto/elliptic"
	_ "crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

func TestGenerateKeyset(t *testing.T) {
	t.Parallel()
	result, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Keys) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Algorithm != jose.ES256 {
		t.Fatalf("algorithm = %q, want %q", result.Algorithm, jose.ES256)
	}
	block, rest := pem.Decode([]byte(result.Keys[0]))
	if block == nil || len(rest) != 0 || block.Type != "EC PRIVATE KEY" {
		t.Fatalf("invalid generated PEM: %q", result.Keys[0])
	}
	privateKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if privateKey.Curve != elliptic.P256() || privateKey.D.Sign() <= 0 {
		t.Fatalf("invalid generated private key: %#v", privateKey)
	}
	public := jose.JSONWebKey{Key: &privateKey.PublicKey}
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
