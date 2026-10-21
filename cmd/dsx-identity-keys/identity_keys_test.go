// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	_ "crypto/sha256"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"dsx-identity/internal/keydistribution"
	"dsx-identity/internal/keytest"
	"dsx-identity/internal/signingkey"
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

	active := keytest.TestJWKForAlgorithm(t, "", jose.ES384, false)
	previous := keytest.TestJWKForAlgorithm(t, "", jose.ES384, false)
	activeKID := keytest.SigningKID(t, active)
	keysetPath, kidPath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{active, previous}, activeKID)
	reloader, err := signingkey.NewReloader(keysetPath, kidPath)
	if err != nil {
		t.Fatal(err)
	}
	publicKeys := slices.Collect(maps.Values(reloader.Source().Get().PublicKeys()))
	if reloader.Source().Get().KeyID() != activeKID || len(publicKeys) != 2 {
		t.Fatalf("loaded signing keys = kid %q, %d public keys", reloader.Source().Get().KeyID(), len(publicKeys))
	}
	for _, key := range publicKeys {
		if !key.IsPublic() {
			t.Fatalf("published kid %q contains private material", key.KeyID)
		}
	}

	server := &identityKeysServer{signingKeys: reloader.Source(), issuer: testIssuer}
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

	reloadedReloader, err := signingkey.NewReloader(keysetPath, kidPath)
	if err != nil {
		t.Fatal(err)
	}
	reloadedPublicKeys := slices.Collect(maps.Values(reloadedReloader.Source().Get().PublicKeys()))
	if len(publicKeys) != len(reloadedPublicKeys) || !keytest.VerifyJWKPresent(reloadedPublicKeys, publicKeys[0]) {
		t.Fatal("reloading the mounted keyset changed its public keys")
	}
}

func TestRegistryValidatesKeys(t *testing.T) {
	t.Parallel()

	registry := newTestKeyRegistry(t, nil)
	first := keytest.TestJWK(t, "first-kid", true)
	second := keytest.TestJWKForAlgorithm(t, "second-kid", jose.ES384, true)
	registerTestKey(t, registry, first)
	registerTestKey(t, registry, second)
	if got := registry.Keyset().Keys; len(got) != 2 || !keytest.VerifyJWKPresent(got, first) || !keytest.VerifyJWKPresent(got, second) {
		t.Fatalf("remote keys = %#v, want %s and %s", got, first.KeyID, second.KeyID)
	}
	if err := registry.ReplaceRemote("wrong-kid", second, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("mismatched key ID was accepted")
	}
	if got := registry.Keyset().Keys; len(got) != 2 ||
		!keytest.VerifyJWKPresent(got, first) || !keytest.VerifyJWKPresent(got, second) {
		t.Fatalf("invalid key changed aggregate: %#v", got)
	}
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
		key := keytest.TestJWKForAlgorithm(t, id+"-kid", algorithm, false)
		shards = append(shards, shard{id: id, key: key, token: keytest.SignTestToken(t, key)})
	}
	registries := []*keydistribution.Registry{newTestKeyRegistry(t, nil), newTestKeyRegistry(t, nil), newTestKeyRegistry(t, nil)}
	for _, registry := range registries {
		for _, shard := range shards {
			registerTestKey(t, registry, shard.key.Public())
		}
	}
	for i, registry := range registries {
		if len(registry.Keyset().Keys) != len(shards) {
			t.Fatalf("aggregate %d has the wrong key count", i)
		}
		for _, shard := range shards {
			if !keytest.VerifyTestToken(registry.Keyset(), shard.token) {
				t.Fatalf("aggregate %d did not verify %s token", i, shard.id)
			}
		}
	}

	rotated := &shards[1]
	newKey := keytest.TestJWK(t, rotated.id+"-rotated-kid", false)
	newToken := keytest.SignTestToken(t, newKey)
	for i, registry := range registries {
		registerTestKey(t, registry, newKey.Public())
		if len(registry.Keyset().Keys) != len(shards)+1 || !keytest.VerifyTestToken(registry.Keyset(), newToken) {
			t.Fatalf("aggregate %d did not preserve both rotation keys", i)
		}
		for _, shard := range shards {
			if !keytest.VerifyTestToken(registry.Keyset(), shard.token) {
				t.Fatalf("aggregate %d lost %s during key rotation", i, shard.id)
			}
		}
	}

}

func TestJWKSAndDiscoveryAreConfiguredNotHostScoped(t *testing.T) {
	t.Parallel()

	local := keytest.TestJWK(t, "local", true)
	registry := newTestKeyRegistry(t, []jose.JSONWebKey{local})
	registerTestKey(t, registry, keytest.TestJWK(t, "one", true))
	registerTestKey(t, registry, keytest.TestJWKForAlgorithm(t, "two", jose.ES384, true))
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
	var Keyset jose.JSONWebKeySet
	if err := json.NewDecoder(jwksResponse.Body).Decode(&Keyset); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(Keyset.Keys))
	for _, key := range Keyset.Keys {
		got = append(got, key.KeyID)
	}
	if !reflect.DeepEqual(got, []string{"local", "one", "two"}) {
		t.Fatalf("aggregate key IDs = %v", got)
	}
}

func TestLocalRegistryIsReadyWithoutNATS(t *testing.T) {
	t.Parallel()

	registry := newTestKeyRegistry(t, []jose.JSONWebKey{keytest.TestJWK(t, "local", true)})
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
		if !keytest.VerifyTestToken(keys, token) {
			t.Fatalf("live aggregate did not verify token %d", i)
		}
	}
}

func TestLiveRelyingPartyRejectsMalformedSubject(t *testing.T) {
	relyingPartyURL := os.Getenv("DSX_E2E_RELYING_PARTY_URL")
	if relyingPartyURL == "" {
		t.Skip("DSX_E2E_RELYING_PARTY_URL is not set")
	}
	var Keyset keytest.PrivateKeyset
	if err := json.Unmarshal([]byte(os.Getenv("DSX_E2E_SIGNING_KEYSET")), &Keyset); err != nil {
		t.Fatalf("parse signing keyset: %v", err)
	}
	activeKID := os.Getenv("DSX_E2E_ACTIVE_KID")
	var active []jose.JSONWebKey
	for _, encoded := range Keyset.Keys {
		key, err := signingkey.ParsePrivate(encoded, jose.ES256)
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

func registerTestKey(t *testing.T, registry *keydistribution.Registry, key jose.JSONWebKey) {
	t.Helper()
	if err := registry.ReplaceRemote(key.KeyID, key, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func newTestKeyRegistry(t *testing.T, local []jose.JSONWebKey) *keydistribution.Registry {
	t.Helper()
	registry := keydistribution.NewRegistry(testSigningKeySource(nil, local))
	t.Cleanup(registry.Close)
	return registry
}

func testSigningKeySource(signer jose.Signer, keys []jose.JSONWebKey) *signingkey.Source {
	return signingkey.NewSource("", signer, keys)
}

func TestKeyRegistryReadinessOwnsSynchronization(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	registry := startTestKeyRegistry(t, broker.ClientURL(), "readiness", "shard-one", []jose.JSONWebKey{
		keytest.TestJWK(t, "first", true),
	}, 10*time.Second, testNATSKVConfig("READINESS"), keydistribution.AuthConfig{Mode: keydistribution.AuthNone})
	server := &identityKeysServer{registry: registry}

	response := httptest.NewRecorder()
	server.handleReady(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("ready registry status = %d", response.Code)
	}

	registry.Close()
	response = httptest.NewRecorder()
	server.handleReady(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed registry status = %d", response.Code)
	}
}

func testNATSKVConfig(bucket string) keydistribution.KVConfig {
	return keydistribution.KVConfig{Bucket: bucket, Replicas: 1}
}

func startTestKeyRegistry(t *testing.T, serverURL, clientName, shardID string, keys []jose.JSONWebKey, ttl time.Duration, kvConfig keydistribution.KVConfig, auth keydistribution.AuthConfig) *keydistribution.SynchronizedRegistry {
	t.Helper()
	return startTestSourceRegistry(t, serverURL, clientName, shardID, testSigningKeySource(nil, keys), ttl, kvConfig, auth)
}

func startTestSourceRegistry(t *testing.T, serverURL, clientName, shardID string, source *signingkey.Source, ttl time.Duration, kvConfig keydistribution.KVConfig, auth keydistribution.AuthConfig) *keydistribution.SynchronizedRegistry {
	t.Helper()
	registry := keydistribution.NewRegistry(source)
	synchronizer, err := keydistribution.NewSynchronizer(t.Context(), serverURL, clientName, shardID, source, ttl, kvConfig, registry, auth)
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	registryWithSync := &keydistribution.SynchronizedRegistry{Registry: registry, Synchronizer: synchronizer}
	t.Cleanup(registryWithSync.Close)
	return registryWithSync
}
