package keytest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	_ "crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type PrivateKeyset struct {
	Keys []string `json:"keys"`
}
type ActiveKeysetDocument struct {
	ActiveKID string                  `json:"active_kid"`
	Algorithm jose.SignatureAlgorithm `json:"alg"`
}

func TestJWK(t *testing.T, kid string, public bool) jose.JSONWebKey {
	return TestJWKForAlgorithm(t, kid, jose.ES256, public)
}

func TestJWKForAlgorithm(t *testing.T, kid string, algorithm jose.SignatureAlgorithm, public bool) jose.JSONWebKey {
	t.Helper()
	curve := elliptic.P256()
	switch algorithm {
	case jose.ES384:
		curve = elliptic.P384()
	case jose.ES512:
		curve = elliptic.P521()
	case jose.ES256:
	default:
		t.Fatalf("test algorithm %q is unsupported", algorithm)
	}
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: kid, Algorithm: string(algorithm), Use: "sig"}
	if public {
		return jwk.Public()
	}
	return jwk
}

func WriteSigningFiles(t *testing.T, keys []jose.JSONWebKey, activeKID string) (string, string) {
	t.Helper()
	algorithm := jose.ES256
	if len(keys) > 0 {
		algorithm = jose.SignatureAlgorithm(keys[0].Algorithm)
	}
	encoded := make([]string, 0, len(keys))
	for _, key := range keys {
		private, ok := key.Key.(*ecdsa.PrivateKey)
		if !ok {
			t.Fatalf("test signing key has type %T", key.Key)
		}
		der, err := x509.MarshalECPrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})))
	}
	return WriteRawSigningFile(t, PrivateKeyset{Keys: encoded}), WriteActiveKeysetFile(t, activeKID, algorithm)
}

func WriteRawSigningFile(t *testing.T, keyset PrivateKeyset) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keyset.json")
	raw, err := json.Marshal(keyset)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func WriteActiveKeysetFile(t *testing.T, activeKID string, algorithm jose.SignatureAlgorithm) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "active-kid.json")
	raw, err := json.Marshal(ActiveKeysetDocument{ActiveKID: activeKID, Algorithm: algorithm})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func SigningKID(t *testing.T, key jose.JSONWebKey) string {
	t.Helper()
	public := key.Public()
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(thumbprint)
}

func VerifyJWKPresent(keys []jose.JSONWebKey, want jose.JSONWebKey) bool {
	wantThumbprint, err := want.Thumbprint(crypto.SHA256)
	if err != nil {
		return false
	}
	for _, key := range keys {
		thumbprint, err := key.Thumbprint(crypto.SHA256)
		if err == nil && key.KeyID == want.KeyID && string(thumbprint) == string(wantThumbprint) {
			return true
		}
	}
	return false
}

func SignTestToken(t *testing.T, key jose.JSONWebKey) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key.Key},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), key.KeyID),
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   "https://ds9.dsx.nvid.id",
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1",
		Audience: jwt.Audience{"spiffe://ds9.dsx.nvid.id/openbao"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func VerifyTestToken(keys jose.JSONWebKeySet, token string) bool {
	algorithms := make([]jose.SignatureAlgorithm, 0, len(keys.Keys))
	for _, key := range keys.Keys {
		algorithms = append(algorithms, jose.SignatureAlgorithm(key.Algorithm))
	}
	parsed, err := jwt.ParseSigned(token, algorithms)
	if err != nil || len(parsed.Headers) != 1 {
		return false
	}
	for _, key := range keys.Key(parsed.Headers[0].KeyID) {
		var claims jwt.Claims
		if parsed.Claims(key.Key, &claims) == nil {
			return true
		}
	}
	return false
}

func RunTestNATSServer(t *testing.T, token string) *server.Server {
	t.Helper()
	broker, err := server.NewServer(&server.Options{
		Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true, Authorization: token,
		JetStream: true, StoreDir: t.TempDir(), JetStreamMaxMemory: 64 << 20, JetStreamMaxStore: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	go broker.Start()
	t.Cleanup(func() {
		broker.Shutdown()
		broker.WaitForShutdown()
	})
	if !broker.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server did not become ready")
	}
	return broker
}

func TestJetStream(t *testing.T, serverURL string, options ...nats.Option) jetstream.JetStream {
	t.Helper()
	connection, err := nats.Connect(serverURL, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	js, err := jetstream.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func RequireKeyIDs(t *testing.T, registry interface{ Keyset() jose.JSONWebKeySet }, want ...string) {
	t.Helper()
	slices.Sort(want)
	RequireCondition(t, fmt.Sprintf("key IDs %v", want), func() bool {
		return slices.Equal(KeyIDs(registry), want)
	})
}

func KeyIDs(registry interface{ Keyset() jose.JSONWebKeySet }) []string {
	keys := registry.Keyset().Keys
	KeyIDs := make([]string, len(keys))
	for i, key := range keys {
		KeyIDs[i] = key.KeyID
	}
	slices.Sort(KeyIDs)
	return KeyIDs
}

func RequireCondition(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-poll.C:
		}
	}
}

func RequireNATSStatus(t *testing.T, statuses <-chan nats.Status, want nats.Status) {
	t.Helper()
	select {
	case got := <-statuses:
		if got != want {
			t.Fatalf("NATS status = %s, want %s", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for NATS status %s", want)
	}
}

func ContainsEveryKID(seen map[string]struct{}, KeyIDs []string) bool {
	for _, keyID := range KeyIDs {
		if _, ok := seen[keyID]; !ok {
			return false
		}
	}
	return true
}
