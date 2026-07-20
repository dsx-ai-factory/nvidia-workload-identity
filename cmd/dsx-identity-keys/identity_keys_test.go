package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	_ "crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/identity"
)

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

	active := testJWKForAlgorithm(t, "active", jose.ES384, false)
	previous := testJWK(t, "previous", true)
	jwksPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{active, previous}, "active")
	keys, err := loadSigningKeyset(jwksPath, kidPath)
	if err != nil {
		t.Fatal(err)
	}
	if keys.activeKID != "active" || len(keys.public) != 2 {
		t.Fatalf("loaded keyset = kid %q, %d public keys", keys.activeKID, len(keys.public))
	}
	for _, key := range keys.public {
		if !key.IsPublic() {
			t.Fatalf("published kid %q contains private material", key.KeyID)
		}
	}

	server := &identityKeysServer{keys: keys}
	original := map[string]any{
		"iss":    "spiffe://k2.internal",
		"sub":    "spiffe://k2.internal/original",
		"aud":    []string{"spiffe://ds9.dsx.nvid.id/openbao"},
		"iat":    int64(1700000000),
		"exp":    int64(1700000300),
		"custom": "preserved",
	}
	subject := spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/host/host-1")
	token, err := server.resignJWT(original, subject)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(active.Algorithm)})
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Headers[0].KeyID; got != "active" {
		t.Fatalf("kid = %q, want active", got)
	}
	var claims map[string]any
	if err := parsed.Claims(active.Public().Key, &claims); err != nil {
		t.Fatal(err)
	}
	rawExpected, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(rawExpected, &expected); err != nil {
		t.Fatal(err)
	}
	expected["iss"] = identity.Issuer
	expected["sub"] = subject.String()
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
	if got := otherParsed.Headers[0].KeyID; got != "active" {
		t.Fatalf("second tenant kid = %q, want active", got)
	}
	var otherClaims map[string]any
	if err := otherParsed.Claims(active.Public().Key, &otherClaims); err != nil {
		t.Fatalf("verify second tenant token: %v", err)
	}
	if otherClaims["iss"] != identity.Issuer || otherClaims["sub"] != otherSubject.String() {
		t.Fatalf("second tenant claims = %#v", otherClaims)
	}

	reloaded, err := loadSigningKeyset(jwksPath, kidPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.public) != len(reloaded.public) || !verifyJWKPresent(reloaded.public, keys.public[0]) {
		t.Fatal("reloading the mounted keyset changed its public keys")
	}
}

func TestRejectInvalidSigningConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*jose.JSONWebKey)
		kid    string
	}{
		{name: "missing kid", mutate: func(k *jose.JSONWebKey) { k.KeyID = "" }, kid: "active"},
		{name: "algorithm does not match key", mutate: func(k *jose.JSONWebKey) { k.Algorithm = "ES384" }, kid: "active"},
		{name: "unsupported use", mutate: func(k *jose.JSONWebKey) { k.Use = "enc" }, kid: "active"},
		{name: "symmetric key", mutate: func(k *jose.JSONWebKey) { k.Key = []byte("symmetric") }, kid: "active"},
		{name: "public active key", mutate: func(k *jose.JSONWebKey) { *k = k.Public() }, kid: "active"},
		{name: "unknown active kid", mutate: func(*jose.JSONWebKey) {}, kid: "missing"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			key := testJWK(t, "active", false)
			test.mutate(&key)
			jwksPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{key}, test.kid)
			if _, err := loadSigningKeyset(jwksPath, kidPath); err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
		})
	}

	t.Run("empty keyset", func(t *testing.T) {
		jwksPath, kidPath := writeSigningFiles(t, nil, "active")
		if _, err := loadSigningKeyset(jwksPath, kidPath); err == nil {
			t.Fatal("empty keyset was accepted")
		}
	})
	t.Run("duplicate kid", func(t *testing.T) {
		key := testJWK(t, "active", false)
		jwksPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{key, key}, "active")
		if _, err := loadSigningKeyset(jwksPath, kidPath); err == nil {
			t.Fatal("duplicate kid was accepted")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		jwksPath, kidPath := writeSigningFiles(t, []jose.JSONWebKey{testJWK(t, "active", false)}, "active")
		if err := os.WriteFile(jwksPath, []byte(`{"keys":[`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSigningKeyset(jwksPath, kidPath); err == nil {
			t.Fatal("malformed JWKS was accepted")
		}
	})
	t.Run("missing files", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := loadSigningKeyset(filepath.Join(dir, "missing-jwks"), filepath.Join(dir, "missing-kid")); err == nil {
			t.Fatal("missing signing files were accepted")
		}
	})
}

func TestRegistryValidatesPublications(t *testing.T) {
	t.Parallel()

	registry := newKeyRegistry()
	first := testJWK(t, "first-kid", true)
	second := testJWKForAlgorithm(t, "second-kid", jose.ES384, true)
	if err := registerTestKey(registry, first, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := registerTestKey(registry, second, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := registry.keyset().Keys; len(got) != 2 || got[0].KeyID != first.KeyID || got[1].KeyID != second.KeyID {
		t.Fatalf("aggregate key order = %v, want [%s %s]", keyIDs(got), first.KeyID, second.KeyID)
	}
	private := testJWK(t, "private", false)
	if err := replaceTestPayload(registry, private.KeyID, rawPublishedKey(t, private, time.Hour)); err == nil {
		t.Fatal("private key was accepted")
	}
	if err := replaceTestPayload(registry, second.KeyID, []byte(`{"jwk":`)); err == nil {
		t.Fatal("malformed publication was accepted")
	}
	if err := replaceTestPayload(registry, second.KeyID, rawPublishedKey(t, second, -time.Second)); err == nil {
		t.Fatal("publication with an expired lease was accepted")
	}
	if got := registry.keyset().Keys; len(got) != 2 ||
		!verifyJWKPresent(got, first) || !verifyJWKPresent(got, second) {
		t.Fatalf("invalid publication changed aggregate: %#v", got)
	}
}

func TestAggregatesVerifyFlatKidsAndRotationOverlap(t *testing.T) {
	t.Parallel()
	now := time.Now()

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
	registries := []*keyRegistry{newKeyRegistry(), newKeyRegistry(), newKeyRegistry()}
	for _, registry := range registries {
		for _, shard := range shards {
			leaseExpiresAt := now.Add(2 * time.Hour)
			if shard.id == "shard-b" {
				leaseExpiresAt = now.Add(time.Hour)
			}
			if err := registry.replace(shard.key.KeyID, publishedKey{
				key:            shard.key.Public(),
				leaseExpiresAt: leaseExpiresAt,
			}); err != nil {
				t.Fatal(err)
			}
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
		if err := registry.replace(newKey.KeyID, publishedKey{
			key:            newKey.Public(),
			leaseExpiresAt: now.Add(2 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
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

	registry := newKeyRegistry()
	if err := registerTestKey(registry, testJWK(t, "one", true), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := registerTestKey(registry, testJWKForAlgorithm(t, "two", jose.ES384, true), time.Hour); err != nil {
		t.Fatal(err)
	}
	server := &identityKeysServer{
		registry: registry,
		jwksURL:  "https://exchange.example/.well-known/jwks.json",
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
	if cfg.IssuerURL != identity.Issuer || cfg.JWKSURL != server.jwksURL ||
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
	var keyset jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(os.Getenv("DSX_E2E_SIGNING_JWKS")), &keyset); err != nil {
		t.Fatalf("parse signing JWKS: %v", err)
	}
	activeKID := os.Getenv("DSX_E2E_ACTIVE_KID")
	keys := keyset.Key(activeKID)
	if len(keys) != 1 || keys[0].IsPublic() {
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
	assertLiveLoginRejected(t, relyingPartyURL, signLiveClaims(t, keys[0], base))
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
	dir := t.TempDir()
	jwksPath := filepath.Join(dir, "signing-jwks.json")
	kidPath := filepath.Join(dir, "active-kid")
	raw, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jwksPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kidPath, []byte(activeKID), 0o600); err != nil {
		t.Fatal(err)
	}
	return jwksPath, kidPath
}

func addJWKField(t *testing.T, payload []byte, name string, value any) []byte {
	t.Helper()
	var raw struct {
		JWK map[string]any `json:"jwk"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	raw.JWK[name] = value
	updated, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func testPublishedKey(t *testing.T, key jose.JSONWebKey, leaseDuration time.Duration) []byte {
	t.Helper()
	publishedAt := time.Now()
	payload, err := marshalPublicKey(key, publishedAt, publishedAt.Add(leaseDuration))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func rawPublishedKey(t *testing.T, key jose.JSONWebKey, leaseDuration time.Duration) []byte {
	t.Helper()
	publishedAt := time.Now()
	payload, err := json.Marshal(publicKeyPublication{
		PublishedAt:    publishedAt.Unix(),
		LeaseExpiresAt: publishedAt.Add(leaseDuration).Unix(),
		JWK:            key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func replaceTestPayload(registry *keyRegistry, keyID string, payload []byte) error {
	publication, err := decodePublishedKey(payload)
	if err != nil {
		return err
	}
	return registry.replace(keyID, publication)
}

func registerTestKey(registry *keyRegistry, key jose.JSONWebKey, leaseDuration time.Duration) error {
	return registry.replace(key.KeyID, publishedKey{key: key, leaseExpiresAt: time.Now().Add(leaseDuration)})
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
		Issuer:   identity.Issuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/host/host-1",
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
