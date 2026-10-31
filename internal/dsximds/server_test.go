// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dsximds

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const testIssuer = "https://ds9.dsx.nvid.id"

func TestAgentServerIntegration(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(context.Background(), testIssuer, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), jwksServer(t, key).URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signedHostToken := signToken(t, key, jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/latest/meta-data/identity" || r.URL.Query().Get("aud") != "spire-server" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Metadata") != "true" {
			t.Errorf("Metadata = %q", r.Header.Get("Metadata"))
		}
		_, _ = w.Write([]byte(signedHostToken))
	}))
	t.Cleanup(imds.Close)

	attestationData, err := FetchHostToken(context.Background(), imds.Client(), imds.URL, "spire-server")
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := verifier.Verify(context.Background(), attestationData)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := agentID.String(), "spiffe://k2.internal/spire/agent/dsx_imds/tenant/operator-1/bm/host-7"; got != want {
		t.Fatalf("agent ID = %q, want %q", got, want)
	}
}

func TestVerifyHostAttestation(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(context.Background(), testIssuer, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), jwksServer(t, key).URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		issuer   string
		subject  string
		audience jwt.Audience
		expiry   time.Time
		want     string
		wantErr  bool
	}{
		{
			name:     "base host",
			issuer:   testIssuer,
			subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
			audience: jwt.Audience{"spire-server"},
			expiry:   time.Now().Add(time.Minute),
			want:     "spiffe://k2.internal/spire/agent/dsx_imds/tenant/operator-1/bm/host-7",
		},
		{name: "wrong issuer", issuer: "spiffe://wrong.example", subject: "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "wrong audience", issuer: testIssuer, subject: "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7", audience: jwt.Audience{"wrong"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "extra audience", issuer: testIssuer, subject: "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7", audience: jwt.Audience{"spire-server", "extra"}, expiry: time.Now().Add(time.Minute), want: "spiffe://k2.internal/spire/agent/dsx_imds/tenant/operator-1/bm/host-7"},
		{name: "expired", issuer: testIssuer, subject: "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(-time.Minute), wantErr: true},
		{name: "generic resource", issuer: testIssuer, subject: "spiffe://operator-1.ds9.dsx.nvid.id/cluster/cluster-1", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "VM", issuer: testIssuer, subject: "spiffe://operator-1.ds9.dsx.nvid.id/vm/vm-7", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
		{name: "host process", issuer: testIssuer, subject: "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7/process/agent", audience: jwt.Audience{"spire-server"}, expiry: time.Now().Add(time.Minute), wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			token := signToken(t, key, jwt.Claims{
				Issuer:   test.issuer,
				Subject:  test.subject,
				Audience: test.audience,
				Expiry:   jwt.NewNumericDate(test.expiry),
			})
			got, err := verifier.Verify(context.Background(), token)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got.String() != test.want {
				t.Fatalf("agent ID = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRemoteVerifierRejectsWrongSignature(t *testing.T) {
	t.Parallel()

	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := jwksServer(t, wrongKey)
	verifier, err := NewVerifier(context.Background(), testIssuer, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	token := signToken(t, signingKey, jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("expected wrong signature to fail")
	}
}

func TestRemoteVerifierVerifiesTLSCertificate(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJWKS(t, w, key)
	}))
	t.Cleanup(server.Close)
	verifier, err := NewVerifier(context.Background(), testIssuer, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	token := signToken(t, key, jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("self-signed remote JWKS certificate was trusted")
	}
}

func TestRemoteVerifierRefreshesUnknownKID(t *testing.T) {
	t.Parallel()
	type publication struct {
		key *ecdsa.PrivateKey
		kid string
	}
	first, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[publication]
	current.Store(&publication{key: first, kid: "first-key"})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		published := current.Load()
		writeJWKSWithKID(t, w, published.key, published.kid)
	}))
	t.Cleanup(server.Close)

	verifier, err := NewVerifier(context.Background(), testIssuer, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	firstToken := signTokenWithKID(t, first, "first-key", claims)
	firstID, err := verifier.Verify(t.Context(), firstToken)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(&publication{key: second, kid: "second-key"})
	secondToken := signTokenWithKID(t, second, "second-key", claims)
	got, err := verifier.Verify(t.Context(), secondToken)
	if err != nil {
		t.Fatalf("first verification after rotation failed: %v", err)
	}
	if want := "spiffe://k2.internal/spire/agent/dsx_imds/tenant/operator-1/bm/host-7"; got != firstID || got.String() != want {
		t.Fatalf("agent IDs = %q, %q, want %q", firstID, got, want)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("JWKS requests = %d, want 2", got)
	}
	if _, err := verifier.Verify(t.Context(), secondToken); err != nil {
		t.Fatalf("cached verification failed: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("cached verification fetched JWKS: requests = %d, want 2", got)
	}
}

func TestRemoteVerifierTimeoutRecovers(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	firstRequestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			<-r.Context().Done()
			close(firstRequestDone)
			return
		}
		writeJWKS(t, w, key)
	}))
	t.Cleanup(server.Close)

	verifier, err := NewVerifier(context.Background(), testIssuer, "spire-server",
		spiffeid.RequireTrustDomainFromString("k2.internal"), server.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	token := signToken(t, key, jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
		Audience: jwt.Audience{"spire-server"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("expected initial JWKS request to time out")
	}
	select {
	case <-firstRequestDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first JWKS request")
	}
	got, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("remote verifier did not recover: %v", err)
	}
	if got.String() != "spiffe://k2.internal/spire/agent/dsx_imds/tenant/operator-1/bm/host-7" {
		t.Fatalf("agent ID = %q", got)
	}
	if requests.Load() < 2 {
		t.Fatalf("JWKS requests = %d, want at least 2", requests.Load())
	}
}

func jwksServer(t *testing.T, key *ecdsa.PrivateKey) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJWKS(t, w, key)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeJWKS(t *testing.T, w http.ResponseWriter, key *ecdsa.PrivateKey) {
	writeJWKSWithKID(t, w, key, "test-key")
}

func writeJWKSWithKID(t *testing.T, w http.ResponseWriter, key *ecdsa.PrivateKey, kid string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &key.PublicKey,
		KeyID:     kid,
		Algorithm: string(testSignatureAlgorithm(t, key)),
		Use:       "sig",
	}}}); err != nil {
		t.Error(err)
	}
}

func signToken(t *testing.T, key *ecdsa.PrivateKey, claims jwt.Claims) string {
	return signTokenWithKID(t, key, "test-key", claims)
}

func signTokenWithKID(t *testing.T, key *ecdsa.PrivateKey, kid string, claims jwt.Claims) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: testSignatureAlgorithm(t, key), Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func testSignatureAlgorithm(t *testing.T, key *ecdsa.PrivateKey) jose.SignatureAlgorithm {
	t.Helper()
	switch key.Curve {
	case elliptic.P256():
		return jose.ES256
	case elliptic.P384():
		return jose.ES384
	case elliptic.P521():
		return jose.ES512
	default:
		t.Fatalf("unsupported test curve %q", key.Curve.Params().Name)
		return ""
	}
}
