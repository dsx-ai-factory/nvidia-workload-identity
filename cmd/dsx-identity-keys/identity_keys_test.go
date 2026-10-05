package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const testIssuer = "https://ds9.dsx.nvid.id"

func TestServeHTTPStopsWhenContextIsAlreadyCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler()}
	if err := serveHTTP(ctx, server); err != nil {
		t.Fatal(err)
	}
}

func TestStaticSigningKeysetAndClaims(t *testing.T) {
	t.Parallel()

	active := testJWKForAlgorithm(t, "", jose.ES384, false)
	previous := testJWKForAlgorithm(t, "", jose.ES384, false)
	activeKID := signingKID(t, active)
	keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{active, previous}, activeKID)
	reloader, err := newSigningKeyReloader(keysetPath, kidPath)
	if err != nil {
		t.Fatal(err)
	}
	signer := reloader.source.Get().signer
	publicKeys := slices.Collect(maps.Values(reloader.source.Get().public))
	if signer.keyID != activeKID || len(publicKeys) != 2 {
		t.Fatalf("loaded signing keys = kid %q, %d public keys", signer.keyID, len(publicKeys))
	}
	for _, key := range publicKeys {
		if !key.IsPublic() {
			t.Fatalf("published kid %q contains private material", key.KeyID)
		}
	}

	server := &identityKeysServer{signingKeys: reloader.source, issuer: testIssuer}
	original := map[string]any{
		"iss":    "spiffe://k2.internal",
		"sub":    "spiffe://k2.internal/original",
		"aud":    []string{"spiffe://ds9.dsx.nvid.id/openbao"},
		"iat":    int64(1700000000),
		"exp":    int64(1700000300),
		"custom": "not propagated",
	}
	subject := spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1")
	token, err := server.resignJWT(original, subject)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(active.Algorithm)})
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Headers[0].KeyID; got != activeKID {
		t.Fatalf("kid = %q, want %q", got, activeKID)
	}
	var claims map[string]any
	if err := parsed.Claims(active.Public().Key, &claims); err != nil {
		t.Fatal(err)
	}
	rawExpected, err := json.Marshal(map[string]any{
		"aud": original["aud"],
		"exp": original["exp"],
		"iat": original["iat"],
		"iss": testIssuer,
		"sub": subject.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(rawExpected, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claims, expected) {
		t.Fatalf("claims = %#v, want %#v", claims, expected)
	}

	otherSubject := spiffeid.RequireFromString("spiffe://tenant-2.ds9.dsx.nvid.id/vm/vm-2")
	otherToken, err := server.resignJWT(original, otherSubject)
	if err != nil {
		t.Fatal(err)
	}
	otherParsed, err := jwt.ParseSigned(otherToken, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(active.Algorithm)})
	if err != nil {
		t.Fatal(err)
	}
	if got := otherParsed.Headers[0].KeyID; got != activeKID {
		t.Fatalf("second tenant kid = %q, want %q", got, activeKID)
	}
	var otherClaims map[string]any
	if err := otherParsed.Claims(active.Public().Key, &otherClaims); err != nil {
		t.Fatalf("verify second tenant token: %v", err)
	}
	if otherClaims["iss"] != testIssuer || otherClaims["sub"] != otherSubject.String() {
		t.Fatalf("second tenant claims = %#v", otherClaims)
	}

	reloadedReloader, err := newSigningKeyReloader(keysetPath, kidPath)
	if err != nil {
		t.Fatal(err)
	}
	reloadedPublicKeys := slices.Collect(maps.Values(reloadedReloader.source.Get().public))
	if len(publicKeys) != len(reloadedPublicKeys) || !verifyJWKPresent(reloadedPublicKeys, publicKeys[0]) {
		t.Fatal("reloading the mounted keyset changed its public keys")
	}
}

func TestRSAPEMAlgorithm(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, err := parsePrivateSigningKey(string(pem.EncodeToMemory(&pem.Block{
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
	_, err := parsePrivateSigningKey(exported, jose.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidSigningConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("empty keyset", func(t *testing.T) {
		keysetPath, kidPath := writeSigningFiles(t, nil, "active")
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("empty keyset was accepted")
		}
	})
	t.Run("duplicate kid", func(t *testing.T) {
		key := testJWK(t, "", false)
		keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{key, key}, signingKID(t, key))
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("duplicate kid was accepted")
		}
	})
	t.Run("unknown active kid", func(t *testing.T) {
		keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{testJWK(t, "", false)}, "missing")
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("unknown active kid was accepted")
		}
	})
	t.Run("incompatible algorithm", func(t *testing.T) {
		key := testJWK(t, "", false)
		keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{key}, signingKID(t, key))
		if err := os.WriteFile(kidPath, []byte(`{"active_kid":"`+signingKID(t, key)+`","alg":"RS256"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("algorithm incompatible with its key was accepted")
		}
	})
	t.Run("public key", func(t *testing.T) {
		key := testJWK(t, "", false)
		der, err := x509.MarshalPKIXPublicKey(key.Public().Key)
		if err != nil {
			t.Fatal(err)
		}
		keysetPath := writeRawSigningFile(t, privateKeyset{Keys: []string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}})
		if _, err := newSigningKeyReloader(keysetPath, writeActiveKeysetFile(t, signingKID(t, key), jose.ES256)); err == nil {
			t.Fatal("public key was accepted")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{testJWK(t, "", false)}, "active")
		if err := os.WriteFile(keysetPath, []byte(`{"keys":[`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("malformed keyset was accepted")
		}
	})
	t.Run("malformed active kid JSON", func(t *testing.T) {
		key := testJWK(t, "", false)
		keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{key}, signingKID(t, key))
		if err := os.WriteFile(kidPath, []byte(`{"active_kid":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("malformed active kid document was accepted")
		}
	})
	t.Run("plain active kid", func(t *testing.T) {
		key := testJWK(t, "", false)
		kid := signingKID(t, key)
		keysetPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{key}, kid)
		if err := os.WriteFile(kidPath, []byte(kid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newSigningKeyReloader(keysetPath, kidPath); err == nil {
			t.Fatal("plain active kid was accepted")
		}
	})
	t.Run("missing files", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := newSigningKeyReloader(filepath.Join(dir, "missing-keyset"), filepath.Join(dir, "missing-kid")); err == nil {
			t.Fatal("missing signing files were accepted")
		}
	})
}

func TestRegistryValidatesKeys(t *testing.T) {
	t.Parallel()

	registry := newTestKeyRegistry(t, nil)
	first := testJWK(t, "first-kid", true)
	second := testJWKForAlgorithm(t, "second-kid", jose.ES384, true)
	registerTestKey(t, registry, first)
	registerTestKey(t, registry, second)
	if got := registry.keyset().Keys; len(got) != 2 || !verifyJWKPresent(got, first) || !verifyJWKPresent(got, second) {
		t.Fatalf("remote keys = %#v, want %s and %s", got, first.KeyID, second.KeyID)
	}
	if err := registry.replaceRemote("wrong-kid", second, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("mismatched key ID was accepted")
	}
	if got := registry.keyset().Keys; len(got) != 2 ||
		!verifyJWKPresent(got, first) || !verifyJWKPresent(got, second) {
		t.Fatalf("invalid key changed aggregate: %#v", got)
	}
}

func TestRegistryExpiresKeysAtTheirKVDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := newTestKeyRegistry(t, nil)
		key := testJWK(t, "expiring-kid", true)
		if err := registry.replaceRemote(key.KeyID, key, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if len(registry.keyset().Keys) != 1 {
			t.Fatal("unexpired key was not available")
		}
		time.Sleep(time.Minute + time.Nanosecond)
		synctest.Wait()
		if registry.remote.Len() != 0 {
			t.Fatal("background expiry did not remove the key")
		}
		if len(registry.keyset().Keys) != 0 {
			t.Fatal("expired key remained available")
		}
	})
}

func TestAggregatesVerifyFlatKidsAndRotationOverlap(t *testing.T) {
	t.Parallel()
	type shard struct {
		id    string
		key   jose.JSONWebKey
		token string
	}
	shards := make([]shard, 0, 5)
	for _, id := range []string{"shard-a", "shard-b", "shard-c", "shard-d", "shard-e"} {
		algorithm := jose.ES256
		if id == "shard-b" {
			algorithm = jose.ES384
		}
		key := testJWKForAlgorithm(t, id+"-kid", algorithm, false)
		shards = append(shards, shard{id: id, key: key, token: signTestToken(t, key)})
	}
	registries := []*keyRegistry{newTestKeyRegistry(t, nil), newTestKeyRegistry(t, nil), newTestKeyRegistry(t, nil)}
	for _, registry := range registries {
		for _, shard := range shards {
			registerTestKey(t, registry, shard.key.Public())
		}
	}
	for i, registry := range registries {
		if len(registry.keyset().Keys) != len(shards) {
			t.Fatalf("aggregate %d has the wrong key count", i)
		}
		for _, shard := range shards {
			if !verifyTestToken(registry.keyset(), shard.token) {
				t.Fatalf("aggregate %d did not verify %s token", i, shard.id)
			}
		}
	}

	rotated := &shards[1]
	newKey := testJWK(t, rotated.id+"-rotated-kid", false)
	newToken := signTestToken(t, newKey)
	for i, registry := range registries {
		registerTestKey(t, registry, newKey.Public())
		if len(registry.keyset().Keys) != len(shards)+1 || !verifyTestToken(registry.keyset(), newToken) {
			t.Fatalf("aggregate %d did not preserve both rotation keys", i)
		}
		for _, shard := range shards {
			if !verifyTestToken(registry.keyset(), shard.token) {
				t.Fatalf("aggregate %d lost %s during key rotation", i, shard.id)
			}
		}
	}

}

func verifyJWKPresent(keys []jose.JSONWebKey, want jose.JSONWebKey) bool {
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

func TestJWKSAndDiscoveryAreConfiguredNotHostScoped(t *testing.T) {
	t.Parallel()

	local := testJWK(t, "local", true)
	registry := newTestKeyRegistry(t, []jose.JSONWebKey{local})
	registerTestKey(t, registry, testJWK(t, "one", true))
	registerTestKey(t, registry, testJWKForAlgorithm(t, "two", jose.ES384, true))
	server := &identityKeysServer{
		registry: registry,
		jwksURL:  "https://exchange.example/.well-known/jwks.json",
		issuer:   testIssuer,
	}

	var previous string
	for _, host := range []string{"tenant-one.example", "tenant-two.example"} {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		server.handleJWKS(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("host %s status = %d", host, rec.Code)
		}
		if previous != "" && previous != rec.Body.String() {
			t.Fatal("JWKS changed with Host header")
		}
		previous = rec.Body.String()
	}

	rec := httptest.NewRecorder()
	server.handleOIDCDiscovery(rec, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
	var cfg oidc.ProviderConfig
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.IssuerURL != testIssuer || cfg.JWKSURL != server.jwksURL ||
		!reflect.DeepEqual(cfg.Algorithms, []string{"ES256", "ES384"}) {
		t.Fatalf("unexpected discovery: %#v", cfg)
	}
	if got := rec.Header().Get("Cache-Control"); got != discoveryCacheControl {
		t.Fatalf("discovery Cache-Control = %q", got)
	}
	jwksResponse := httptest.NewRecorder()
	server.handleJWKS(jwksResponse, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if got := jwksResponse.Header().Get("Cache-Control"); got != discoveryCacheControl {
		t.Fatalf("JWKS Cache-Control = %q", got)
	}
	var keyset jose.JSONWebKeySet
	if err := json.NewDecoder(jwksResponse.Body).Decode(&keyset); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(keyset.Keys))
	for _, key := range keyset.Keys {
		got = append(got, key.KeyID)
	}
	if !reflect.DeepEqual(got, []string{"local", "one", "two"}) {
		t.Fatalf("aggregate key IDs = %v", got)
	}
}

func TestLocalRegistryIsReadyWithoutNATS(t *testing.T) {
	t.Parallel()

	registry := newTestKeyRegistry(t, []jose.JSONWebKey{testJWK(t, "local", true)})
	server := &identityKeysServer{registry: registry}
	response := httptest.NewRecorder()
	server.handleReady(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestTokenExchangeRequestContract(t *testing.T) {
	t.Parallel()

	server := new(identityKeysServer)
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
		code        string
	}{
		{"wrong content type", "application/json", `{}`, http.StatusUnsupportedMediaType, "invalid_request"},
		{"malformed content type", ContentTypeFormURLEncoded + "; charset", ``, http.StatusUnsupportedMediaType, "invalid_request"},
		{"content type parameters", ContentTypeFormURLEncoded + "; charset=utf-8", ``, http.StatusBadRequest, "unsupported_grant_type"},
		{"content type case", "Application/X-Www-Form-Urlencoded", ``, http.StatusBadRequest, "unsupported_grant_type"},
		{"missing grant", ContentTypeFormURLEncoded, "subject_token=x&subject_token_type=" + TokenTypeJWT, http.StatusBadRequest, "unsupported_grant_type"},
		{"missing subject", ContentTypeFormURLEncoded, "grant_type=" + GrantTypeTokenExchange + "&subject_token_type=" + TokenTypeJWT, http.StatusBadRequest, "invalid_request"},
		{"wrong token type", ContentTypeFormURLEncoded, "grant_type=" + GrantTypeTokenExchange + "&subject_token=x&subject_token_type=access_token", http.StatusBadRequest, "invalid_request"},
		{"wrong requested token type", ContentTypeFormURLEncoded, "grant_type=" + GrantTypeTokenExchange + "&subject_token=x&subject_token_type=" + TokenTypeJWT + "&requested_token_type=access_token", http.StatusBadRequest, "invalid_request"},
		{"malformed subject token", ContentTypeFormURLEncoded, "grant_type=" + GrantTypeTokenExchange + "&subject_token=not-a-jwt&subject_token_type=" + TokenTypeJWT, http.StatusUnauthorized, "invalid_grant"},
		{"oversized request", ContentTypeFormURLEncoded, "grant_type=" + GrantTypeTokenExchange + "&subject_token=" + strings.Repeat("x", maxExchangeRequestBytes) + "&subject_token_type=" + TokenTypeJWT, http.StatusBadRequest, "invalid_request"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/v1/exchange", strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.contentType)
			rec := httptest.NewRecorder()
			server.handleTokenExchange(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.status, rec.Body.String())
			}
			var response TokenExchangeErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Error != test.code {
				t.Fatalf("error = %q, want %q", response.Error, test.code)
			}
		})
	}
}

func TestLiveAggregateVerifiesTokens(t *testing.T) {
	rawJWKS := os.Getenv("DSX_E2E_VERIFY_JWKS")
	rawTokens := os.Getenv("DSX_E2E_VERIFY_TOKENS")
	if rawJWKS == "" && rawTokens == "" {
		t.Skip("live aggregate inputs are not set")
	}
	var keys jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(rawJWKS), &keys); err != nil {
		t.Fatalf("parse live JWKS: %v", err)
	}
	var tokens []string
	if err := json.Unmarshal([]byte(rawTokens), &tokens); err != nil {
		t.Fatalf("parse live tokens: %v", err)
	}
	if len(keys.Keys) == 0 || len(tokens) == 0 {
		t.Fatal("live JWKS and tokens must be non-empty")
	}
	for i, token := range tokens {
		if !verifyTestToken(keys, token) {
			t.Fatalf("live aggregate did not verify token %d", i)
		}
	}
}

func TestLiveRelyingPartyRejectsMalformedSubject(t *testing.T) {
	relyingPartyURL := os.Getenv("DSX_E2E_RELYING_PARTY_URL")
	if relyingPartyURL == "" {
		t.Skip("DSX_E2E_RELYING_PARTY_URL is not set")
	}
	var keyset privateKeyset
	if err := json.Unmarshal([]byte(os.Getenv("DSX_E2E_SIGNING_KEYSET")), &keyset); err != nil {
		t.Fatalf("parse signing keyset: %v", err)
	}
	activeKID := os.Getenv("DSX_E2E_ACTIVE_KID")
	var active []jose.JSONWebKey
	for _, encoded := range keyset.Keys {
		key, err := parsePrivateSigningKey(encoded, jose.ES256)
		if err != nil {
			t.Fatalf("parse signing key: %v", err)
		}
		if key.KeyID == activeKID {
			active = append(active, key)
		}
	}
	if len(active) != 1 {
		t.Fatalf("active private key %q was not found exactly once", activeKID)
	}
	validToken := os.Getenv("DSX_E2E_VALID_TOKEN")
	parsed, err := jwt.ParseSigned(validToken, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("parse valid token claims: %v", err)
	}
	var base map[string]any
	if err := parsed.UnsafeClaimsWithoutVerification(&base); err != nil {
		t.Fatalf("read valid token claims: %v", err)
	}

	base["sub"] = "not-a-dsx-subject"
	assertLiveLoginRejected(t, relyingPartyURL, signLiveClaims(t, active[0], base))
}

func signLiveClaims(t *testing.T, key jose.JSONWebKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key.Key},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), key.KeyID),
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func assertLiveLoginRejected(t *testing.T, relyingPartyURL, token string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"role": "basic-spiffe", "jwt": token})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, relyingPartyURL, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("login status = %d, want 400: %s", response.StatusCode, responseBody)
	}
}

func testJWK(t *testing.T, kid string, public bool) jose.JSONWebKey {
	return testJWKForAlgorithm(t, kid, jose.ES256, public)
}

func testJWKForAlgorithm(t *testing.T, kid string, algorithm jose.SignatureAlgorithm, public bool) jose.JSONWebKey {
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

func writeSigningFiles(t *testing.T, keys []jose.JSONWebKey, activeKID string) (string, string) {
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
	return writeRawSigningFile(t, privateKeyset{Keys: encoded}), writeActiveKeysetFile(t, activeKID, algorithm)
}

func writeRawSigningFile(t *testing.T, keyset privateKeyset) string {
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

func writeActiveKeysetFile(t *testing.T, activeKID string, algorithm jose.SignatureAlgorithm) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "active-kid.json")
	raw, err := json.Marshal(activeKeysetDocument{ActiveKID: activeKID, Algorithm: algorithm})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func signingKID(t *testing.T, key jose.JSONWebKey) string {
	t.Helper()
	public := key.Public()
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(thumbprint)
}

func registerTestKey(t *testing.T, registry *keyRegistry, key jose.JSONWebKey) {
	t.Helper()
	if err := registry.replaceRemote(key.KeyID, key, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func newTestKeyRegistry(t *testing.T, local []jose.JSONWebKey) *keyRegistry {
	t.Helper()
	registry := newKeyRegistry(testSigningKeySource(nil, local))
	t.Cleanup(registry.Close)
	return registry
}

func signTestToken(t *testing.T, key jose.JSONWebKey) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key.Key},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), key.KeyID),
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1",
		Audience: jwt.Audience{"spiffe://ds9.dsx.nvid.id/openbao"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func verifyTestToken(keys jose.JSONWebKeySet, token string) bool {
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

func testSigningKeySource(signer *tokenSigner, keys []jose.JSONWebKey) *signingKeySource {
	return newSigningKeySource(newSigningKeys(signer, keys))
}
