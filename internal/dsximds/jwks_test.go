// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dsximds

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

func TestRemoteKeySetConcurrentRefreshAndCancellation(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		writeJWKS(t, w, key)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	keys := &remoteKeySet{url: server.URL, client: server.Client(), algorithms: []jose.SignatureAlgorithm{jose.ES256}, refresh: make(chan struct{}, 1)}
	token := signToken(t, key, jwt.Claims{Subject: "host"})
	const callers = 16
	results := make(chan error, callers)
	go func() {
		_, err := keys.VerifySignature(ctx, token)
		results <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("JWKS fetch did not start")
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := keys.VerifySignature(canceled, token); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter returned %v", err)
	}
	for range callers - 1 {
		go func() {
			_, err := keys.VerifySignature(ctx, token)
			results <- err
		}()
	}
	close(release)
	for range callers {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("concurrent verification failed: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent verification did not finish")
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("JWKS requests = %d, want 1", got)
	}
}

func TestRemoteKeySetRefreshFailurePreservesCache(t *testing.T) {
	t.Parallel()
	first, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		switch mode.Load() {
		case 0:
			writeJWKSWithKID(t, w, first, "first")
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			_, _ = w.Write([]byte(`{"keys":`))
		case 3:
			writeJWKSWithKID(t, w, second, "second")
		}
	}))
	t.Cleanup(server.Close)
	keys := &remoteKeySet{url: server.URL, client: server.Client(), algorithms: []jose.SignatureAlgorithm{jose.ES256}, refresh: make(chan struct{}, 1)}
	firstToken := signTokenWithKID(t, first, "first", jwt.Claims{Subject: "host"})
	secondToken := signTokenWithKID(t, second, "second", jwt.Claims{Subject: "host"})
	if _, err := keys.VerifySignature(t.Context(), firstToken); err != nil {
		t.Fatal(err)
	}
	for _, state := range []int32{0, 1, 2} {
		mode.Store(state)
		if _, err := keys.VerifySignature(t.Context(), secondToken); err == nil {
			t.Fatalf("accepted unpublished key with endpoint state %d", state)
		}
		before := requests.Load()
		if _, err := keys.VerifySignature(t.Context(), firstToken); err != nil {
			t.Fatalf("cached key rejected after failed refresh: %v", err)
		}
		if requests.Load() != before {
			t.Fatal("cached key required another HTTP request")
		}
	}
	mode.Store(3)
	if _, err := keys.VerifySignature(t.Context(), secondToken); err != nil {
		t.Fatalf("first verification after recovery failed: %v", err)
	}
	if got := requests.Load(); got != 5 {
		t.Fatalf("JWKS requests = %d, want 5", got)
	}
}

func TestRemoteKeySetSelectsSigningKey(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &wrong.PublicKey, KeyID: "shared"},
			{Key: &key.PublicKey, KeyID: "shared"},
		}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	keys := &remoteKeySet{url: server.URL, client: server.Client(), algorithms: []jose.SignatureAlgorithm{jose.ES256}, refresh: make(chan struct{}, 1)}
	for _, kid := range []string{"shared", "", "missing"} {
		token := signTokenWithKID(t, key, kid, jwt.Claims{Subject: "host"})
		_, err := keys.VerifySignature(t.Context(), token)
		if (err != nil) != (kid == "missing") {
			t.Errorf("kid %q: error = %v", kid, err)
		}
	}
}

func TestRemoteVerifierSigningAlgorithms(t *testing.T) {
	t.Parallel()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, algorithm := range []jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512,
		jose.ES256, jose.ES384, jose.ES512, jose.EdDSA, jose.HS256,
	} {
		t.Run(string(algorithm), func(t *testing.T) {
			t.Parallel()
			var key any = rsaKey
			switch algorithm {
			case jose.ES256, jose.ES384, jose.ES512:
				curve := elliptic.P256()
				if algorithm == jose.ES384 {
					curve = elliptic.P384()
				} else if algorithm == jose.ES512 {
					curve = elliptic.P521()
				}
				var err error
				key, err = ecdsa.GenerateKey(curve, rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
			case jose.EdDSA:
				key = edKey
			case jose.HS256:
				key = make([]byte, 32)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if err := json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
					(&jose.JSONWebKey{Key: key, KeyID: "test"}).Public(),
				}}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			verifier, err := NewVerifier(testIssuer, "spire-server",
				spiffeid.RequireTrustDomainFromString("k2.internal"), server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
			if err != nil {
				t.Fatal(err)
			}
			token, err := jwt.Signed(signer).Claims(jwt.Claims{
				Issuer: testIssuer, Subject: "spiffe://operator-1.ds9.dsx.nvid.id/bm/host-7",
				Audience: jwt.Audience{"spire-server"}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			}).Serialize()
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifier.Verify(t.Context(), token)
			if (err != nil) != (algorithm == jose.HS256) {
				t.Fatalf("verification error = %v", err)
			}
		})
	}
}
