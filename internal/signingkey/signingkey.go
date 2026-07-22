package signingkey

import (
	"crypto"
	_ "crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"github.com/go-jose/go-jose/v4"
)

// ParsePrivatePEM parses a private signing key and derives its RFC 7638 key ID.
func ParsePrivatePEM(encoded string) (jose.JSONWebKey, error) {
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

	private := jose.JSONWebKey{Key: key}
	if !private.Valid() || private.IsPublic() {
		return jose.JSONWebKey{}, fmt.Errorf("invalid private JWK")
	}
	public := private.Public()
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		return jose.JSONWebKey{}, fmt.Errorf("compute public JWK thumbprint: %w", err)
	}
	private.KeyID = base64.RawURLEncoding.EncodeToString(thumbprint)
	return private, nil
}
