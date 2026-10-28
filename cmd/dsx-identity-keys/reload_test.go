// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"golang.org/x/sync/errgroup"

	"dsx-identity/internal/dsxidentity"
	"dsx-identity/internal/filewatch"
	"dsx-identity/internal/keydistribution"
	"dsx-identity/internal/keytest"
	"dsx-identity/internal/signingkey"
	"dsx-identity/internal/watch"
)

func TestReloadSigningSources(t *testing.T) {
	for _, projected := range []bool{false, true} {
		name := "regular files"
		if projected {
			name = "projected Secret symlinks"
		}
		t.Run(name, func(t *testing.T) {
			first := keytest.TestJWK(t, "", false)
			second := keytest.TestJWK(t, "", false)
			firstID, secondID := keytest.SigningKID(t, first), keytest.SigningKID(t, second)
			keysetPath, activePath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{first}, firstID)
			sourceKeyset, sourceActive := keysetPath, activePath
			if projected {
				keysetPath = filepath.Join(t.TempDir(), "keyset.json")
				activePath = filepath.Join(t.TempDir(), "active-kid.json")
				replaceSigningSymlink(t, keysetPath, sourceKeyset)
				replaceSigningSymlink(t, activePath, sourceActive)
			}
			reloader, err := signingkey.NewReloader(keysetPath, activePath)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				registry := keydistribution.NewRegistry(reloader.Source())
				defer registry.Close()
				server := &identityKeysServer{signingKeys: reloader.Source(), registry: registry, issuer: testIssuer}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() {
					defer close(done)
					if err := reloader.Run(ctx, watch.Composite(
						filewatch.Contents(keysetPath, watch.Polling(30*time.Second)),
						filewatch.Contents(activePath, watch.Polling(30*time.Second)),
					)); err != nil {
						t.Error(err)
					}
				}()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				assertReloadedKeys(t, server, firstID, firstID)
				update := func(keys []jose.JSONWebKey, active string) {
					newKeyset, newActive := keytest.WriteSigningFiles(t, keys, active)
					if projected {
						replaceSigningSymlink(t, keysetPath, newKeyset)
						replaceSigningSymlink(t, activePath, newActive)
					} else {
						copySigningFile(t, keysetPath, newKeyset)
						copySigningFile(t, activePath, newActive)
					}
				}
				tick := func() {
					// Advance the reload deadline in virtual time, then drain its work.
					time.Sleep(30 * time.Second)
					synctest.Wait()
				}
				update([]jose.JSONWebKey{first, second}, firstID)
				tick()
				assertReloadedKeys(t, server, firstID, firstID, secondID)
				update([]jose.JSONWebKey{first, second}, secondID)
				tick()
				assertReloadedKeys(t, server, secondID, firstID, secondID)
				for _, invalid := range []string{
					`{"active_kid":`,
					`{"active_kid":"missing","alg":"ES256"}`,
					`{"active_kid":"` + secondID + `","alg":"RS256"}`,
				} {
					if err := os.WriteFile(sourceActive, []byte(invalid), 0o600); err != nil {
						t.Fatal(err)
					}
					if projected {
						replaceSigningSymlink(t, activePath, sourceActive)
					}
					tick()
					assertReloadedKeys(t, server, secondID, firstID, secondID)
				}
				update([]jose.JSONWebKey{first}, secondID)
				tick()
				assertReloadedKeys(t, server, secondID, firstID, secondID)
				if err := os.WriteFile(sourceKeyset, []byte(`{"keys":[`), 0o600); err != nil {
					t.Fatal(err)
				}
				if projected {
					replaceSigningSymlink(t, keysetPath, sourceKeyset)
				}
				tick()
				assertReloadedKeys(t, server, secondID, firstID, secondID)
				if err := os.Remove(sourceKeyset); err != nil {
					t.Fatal(err)
				}
				tick()
				assertReloadedKeys(t, server, secondID, firstID, secondID)
				update([]jose.JSONWebKey{second}, secondID)
				tick()
				assertReloadedKeys(t, server, secondID, secondID)
				cancel()
				<-done
				update([]jose.JSONWebKey{first}, firstID)
				tick()
				assertReloadedKeys(t, server, secondID, secondID)
			})
		})
	}
}

func replaceSigningSymlink(t *testing.T, path, target string) {
	t.Helper()
	next := path + ".next"
	if err := os.Symlink(target, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
}

func copySigningFile(t *testing.T, path, sourcePath string) {
	t.Helper()
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertReloadedKeys(t *testing.T, server *identityKeysServer, active string, want ...string) {
	t.Helper()
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("JWKS status = %d", response.Code)
	}
	var keys jose.JSONWebKeySet
	if err := json.Unmarshal(response.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(keys.Keys))
	for _, key := range keys.Keys {
		if !key.IsPublic() {
			t.Fatal("JWKS contains private key")
		}
		ids = append(ids, key.KeyID)
	}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("JWKS kids = %v, want %v", ids, want)
	}
	token, err := server.resignJWT(map[string]any{"aud": []string{"test"}}, spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1"), dsxidentity.SubjectClaims{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Headers[0].KeyID != active {
		t.Fatalf("signed kid = %q, want %q", parsed.Headers[0].KeyID, active)
	}
	var claims map[string]any
	if err := parsed.Claims(keys, &claims); err != nil {
		t.Fatalf("verify reloaded token: %v", err)
	}
}

func TestReloadPublishesNewKeysAndStopsRemovedLeases(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	first, second := keytest.TestJWK(t, "", false), keytest.TestJWK(t, "", false)
	firstID, secondID := keytest.SigningKID(t, first), keytest.SigningKID(t, second)
	keysetPath, activePath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{first}, firstID)
	reloader, err := signingkey.NewReloader(keysetPath, activePath)
	if err != nil {
		t.Fatal(err)
	}
	config := testNATSKVConfig("RELOAD")
	local := startTestSourceRegistry(t, broker.ClientURL(), "local", "local", reloader.Source(), time.Second, config, keydistribution.AuthConfig{Mode: keydistribution.AuthNone})
	peerKey := keytest.TestJWK(t, "peer", true)
	peer := startTestKeyRegistry(t, broker.ClientURL(), "peer", "peer", []jose.JSONWebKey{peerKey}, time.Second, config, keydistribution.AuthConfig{Mode: keydistribution.AuthNone})
	server := &identityKeysServer{signingKeys: reloader.Source(), registry: local, issuer: testIssuer}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := reloader.Run(ctx, watch.Composite(
			filewatch.Contents(keysetPath, watch.Composite(filewatch.FSNotify(keysetPath), watch.Polling(30*time.Millisecond))),
			filewatch.Contents(activePath, watch.Composite(filewatch.FSNotify(activePath), watch.Polling(30*time.Millisecond))),
		)); err != nil {
			t.Error(err)
		}
	}()
	defer func() { cancel(); <-done }()
	keytest.RequireKeyIDs(t, peer, firstID, "peer")
	newKeyset, newActive := keytest.WriteSigningFiles(t, []jose.JSONWebKey{first, second}, secondID)
	copySigningFile(t, keysetPath, newKeyset)
	copySigningFile(t, activePath, newActive)
	keytest.RequireKeyIDs(t, peer, firstID, secondID, "peer")
	keytest.RequireCondition(t, "new active signer", func() bool {
		token, err := server.resignJWT(map[string]any{"aud": []string{"test"}}, spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1"), dsxidentity.SubjectClaims{})
		if err != nil {
			return false
		}
		parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
		return err == nil && parsed.Headers[0].KeyID == secondID && keytest.VerifyTestToken(peer.Keyset(), token)
	})
	assertReloadedKeys(t, server, secondID, firstID, secondID, "peer")
	newKeyset, _ = keytest.WriteSigningFiles(t, []jose.JSONWebKey{second}, secondID)
	copySigningFile(t, keysetPath, newKeyset)
	keytest.RequireKeyIDs(t, peer, secondID, "peer")
	keytest.RequireKeyIDs(t, local, secondID, "peer")
	assertReloadedKeys(t, server, secondID, secondID, "peer")
}

func TestSigningDuringSourceSwaps(t *testing.T) {
	first, second := keytest.TestJWK(t, "", false), keytest.TestJWK(t, "", false)
	firstID, secondID := keytest.SigningKID(t, first), keytest.SigningKID(t, second)
	keysetPath, activePath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{first, second}, firstID)
	reloader, err := signingkey.NewReloader(keysetPath, activePath)
	if err != nil {
		t.Fatal(err)
	}
	registry := keydistribution.NewRegistry(reloader.Source())
	defer registry.Close()
	server := &identityKeysServer{signingKeys: reloader.Source(), registry: registry, issuer: testIssuer}
	subject := spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1")
	firstActive := keytest.WriteActiveKeysetFile(t, firstID, jose.ES256)
	secondActive := keytest.WriteActiveKeysetFile(t, secondID, jose.ES256)
	var readers errgroup.Group
	for range 8 {
		readers.Go(func() error {
			for range 100 {
				token, err := server.resignJWT(map[string]any{"aud": []string{"test"}}, subject, dsxidentity.SubjectClaims{})
				if err != nil {
					return err
				}
				response := httptest.NewRecorder()
				server.handleJWKS(response, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
				var keys jose.JSONWebKeySet
				if err := json.Unmarshal(response.Body.Bytes(), &keys); err != nil {
					return err
				}
				if !keytest.VerifyTestToken(keys, token) {
					return fmt.Errorf("token failed verification during reloader swap")
				}
			}
			return nil
		})
	}
	events := watch.Watcher[filewatch.Change](func(_ context.Context, notify func(filewatch.Change) error) error {
		for range 20 {
			for _, active := range []string{secondActive, firstActive} {
				rawActive, err := os.ReadFile(active)
				if err != nil {
					return err
				}
				if err := notify(filewatch.Change{Path: activePath, Contents: rawActive}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err := reloader.Run(t.Context(), events); err != nil {
		t.Error(err)
	}
	if err := readers.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSigningReloaderAcceptsObservedContentsBeforeWatcherAdvances(t *testing.T) {
	first, second := keytest.TestJWK(t, "", false), keytest.TestJWK(t, "", false)
	firstID, secondID := keytest.SigningKID(t, first), keytest.SigningKID(t, second)
	keysetPath, activePath := keytest.WriteSigningFiles(t, []jose.JSONWebKey{first, second}, firstID)
	reloader, err := signingkey.NewReloader(keysetPath, activePath)
	if err != nil {
		t.Fatal(err)
	}
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		t.Fatal(err)
	}
	firstActive, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := keytest.WriteActiveKeysetFile(t, secondID, jose.ES256)
	secondActive, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	// Disk differs from the observations that the watcher delivers below.
	copySigningFile(t, activePath, secondPath)
	events := watch.Watcher[filewatch.Change](func(_ context.Context, notify func(filewatch.Change) error) error {
		if err := notify(filewatch.Change{Path: keysetPath, Contents: rawKeyset}); err != nil {
			return err
		}
		for _, step := range []struct {
			data     []byte
			rejected bool
			want     string
		}{
			{firstActive, false, firstID},
			{secondActive, false, secondID},
			{[]byte(`{"active_kid":`), true, secondID},
			{nil, true, secondID},
			{firstActive, false, firstID},
		} {
			err := notify(filewatch.Change{Path: activePath, Contents: step.data})
			if (err != nil) != step.rejected {
				return fmt.Errorf("consumer reply = %v, want rejected %v", err, step.rejected)
			}
			if got := reloader.Source().Get().KeyID(); got != step.want {
				return fmt.Errorf("watcher resumed with signer %q, want %q", got, step.want)
			}
		}
		return nil
	})
	if err := reloader.Run(t.Context(), watch.Composite(events)); err != nil {
		t.Fatal(err)
	}
	if got := reloader.Source().Get().KeyID(); got != firstID {
		t.Fatalf("final signer = %q, want %q", got, firstID)
	}
}
