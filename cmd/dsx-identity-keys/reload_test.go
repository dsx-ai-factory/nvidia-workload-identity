package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
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

	"dsx-identity/internal/filewatch"
)

func TestReloadSigningSources(t *testing.T) {
	for _, projected := range []bool{false, true} {
		name := "regular files"
		if projected {
			name = "projected Secret symlinks"
		}
		t.Run(name, func(t *testing.T) {
			first := testJWK(t, "", false)
			second := testJWK(t, "", false)
			firstID, secondID := signingKID(t, first), signingKID(t, second)
			keysetPath, activePath := writeSigningFiles(t, []jose.JSONWebKey{first}, firstID)
			sourceKeyset, sourceActive := keysetPath, activePath
			if projected {
				keysetPath = filepath.Join(t.TempDir(), "keyset.json")
				activePath = filepath.Join(t.TempDir(), "active-kid.json")
				replaceSigningSymlink(t, keysetPath, sourceKeyset)
				replaceSigningSymlink(t, activePath, sourceActive)
			}
			source, err := newSigningKeySource(keysetPath, activePath)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				registry := newKeyRegistry(source)
				defer registry.Close()
				server := &identityKeysServer{signingKeys: source, registry: registry, issuer: testIssuer}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() {
					defer close(done)
					if err := source.run(ctx, filewatch.Composite(
						filewatch.Contents(keysetPath, filewatch.Polling(30*time.Second)),
						filewatch.Contents(activePath, filewatch.Polling(30*time.Second)),
					)); err != nil {
						t.Error(err)
					}
				}()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				assertReloadedKeys(t, server, firstID, firstID)
				update := func(keys []jose.JSONWebKey, active string) {
					newKeyset, newActive := writeSigningFiles(t, keys, active)
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

func replaceSigningSymlink(t *testing.T, path, source string) {
	t.Helper()
	next := path + ".next"
	if err := os.Symlink(source, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
}

func copySigningFile(t *testing.T, path, source string) {
	t.Helper()
	raw, err := os.ReadFile(source)
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
	token, err := server.resignJWT(map[string]any{"aud": []string{"test"}}, spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1"))
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
	broker := runTestNATSServer(t, "")
	first, second := testJWK(t, "", false), testJWK(t, "", false)
	firstID, secondID := signingKID(t, first), signingKID(t, second)
	keysetPath, activePath := writeSigningFiles(t, []jose.JSONWebKey{first}, firstID)
	source, err := newSigningKeySource(keysetPath, activePath)
	if err != nil {
		t.Fatal(err)
	}
	config := testNATSKVConfig("RELOAD")
	local := startTestSourceRegistry(t, broker.ClientURL(), "local", "local", source, time.Second, config, natsAuthConfig{mode: natsAuthNone})
	peerKey := testJWK(t, "peer", true)
	peer := startTestKeyRegistry(t, broker.ClientURL(), "peer", "peer", []jose.JSONWebKey{peerKey}, time.Second, config, natsAuthConfig{mode: natsAuthNone})
	server := &identityKeysServer{signingKeys: source, registry: local, issuer: testIssuer}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := source.run(ctx, filewatch.Composite(
			filewatch.Contents(keysetPath, filewatch.Composite(filewatch.FSNotify(keysetPath), filewatch.Polling(30*time.Millisecond))),
			filewatch.Contents(activePath, filewatch.Composite(filewatch.FSNotify(activePath), filewatch.Polling(30*time.Millisecond))),
		)); err != nil {
			t.Error(err)
		}
	}()
	defer func() { cancel(); <-done }()
	requireKeyIDs(t, peer, firstID, "peer")
	newKeyset, newActive := writeSigningFiles(t, []jose.JSONWebKey{first, second}, secondID)
	copySigningFile(t, keysetPath, newKeyset)
	copySigningFile(t, activePath, newActive)
	requireKeyIDs(t, peer, firstID, secondID, "peer")
	requireCondition(t, "new active signer", func() bool {
		token, err := server.resignJWT(map[string]any{"aud": []string{"test"}}, spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1"))
		if err != nil {
			return false
		}
		parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
		return err == nil && parsed.Headers[0].KeyID == secondID && verifyTestToken(peer.keyset(), token)
	})
	assertReloadedKeys(t, server, secondID, firstID, secondID, "peer")
	newKeyset, _ = writeSigningFiles(t, []jose.JSONWebKey{second}, secondID)
	copySigningFile(t, keysetPath, newKeyset)
	requireKeyIDs(t, peer, secondID, "peer")
	requireKeyIDs(t, local, secondID, "peer")
	assertReloadedKeys(t, server, secondID, secondID, "peer")
}

func TestSigningDuringSourceSwaps(t *testing.T) {
	first, second := testJWK(t, "", false), testJWK(t, "", false)
	firstID, secondID := signingKID(t, first), signingKID(t, second)
	keysetPath, activePath := writeSigningFiles(t, []jose.JSONWebKey{first, second}, firstID)
	source, err := newSigningKeySource(keysetPath, activePath)
	if err != nil {
		t.Fatal(err)
	}
	registry := newKeyRegistry(source)
	defer registry.Close()
	server := &identityKeysServer{signingKeys: source, registry: registry, issuer: testIssuer}
	subject := spiffeid.RequireFromString("spiffe://operator-1.ds9.dsx.nvid.id/bm/host-1")
	firstActive := writeActiveKeysetFile(t, firstID, jose.ES256)
	secondActive := writeActiveKeysetFile(t, secondID, jose.ES256)
	var readers errgroup.Group
	for range 8 {
		readers.Go(func() error {
			for range 100 {
				token, err := server.resignJWT(map[string]any{"aud": []string{"test"}}, subject)
				if err != nil {
					return err
				}
				response := httptest.NewRecorder()
				server.handleJWKS(response, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
				var keys jose.JSONWebKeySet
				if err := json.Unmarshal(response.Body.Bytes(), &keys); err != nil {
					return err
				}
				if !verifyTestToken(keys, token) {
					return fmt.Errorf("token failed verification during source swap")
				}
			}
			return nil
		})
	}
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		for _, active := range []string{secondActive, firstActive} {
			copySigningFile(t, activePath, active)
			rawActive, err := os.ReadFile(activePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := source.load(rawKeyset, rawActive); err != nil {
				t.Error(err)
			}
		}
	}
	if err := readers.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSigningSourceAcceptsObservedContentsBeforeWatcherAdvances(t *testing.T) {
	first, second := testJWK(t, "", false), testJWK(t, "", false)
	firstID, secondID := signingKID(t, first), signingKID(t, second)
	keysetPath, activePath := writeSigningFiles(t, []jose.JSONWebKey{first, second}, firstID)
	source, err := newSigningKeySource(keysetPath, activePath)
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
	secondPath := writeActiveKeysetFile(t, secondID, jose.ES256)
	secondActive, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	// Disk differs from the observations that the watcher delivers below.
	copySigningFile(t, activePath, secondPath)
	events := filewatch.Watcher[filewatch.Change](func(context.Context) iter.Seq2[filewatch.Change, error] {
		return func(yield func(filewatch.Change, error) bool) {
			if !yield(filewatch.Change{Path: keysetPath, Contents: rawKeyset}, nil) {
				return
			}
			for _, step := range []struct {
				data    []byte
				readErr error
				want    string
			}{
				{firstActive, nil, firstID},
				{secondActive, nil, secondID},
				{[]byte(`{"active_kid":`), nil, secondID},
				{nil, os.ErrNotExist, secondID},
				{firstActive, nil, firstID},
			} {
				if !yield(filewatch.Change{Path: activePath, Contents: step.data}, step.readErr) {
					return
				}
				if got := source.current.Load().signer.keyID; got != step.want {
					yield(filewatch.Change{}, fmt.Errorf("watcher resumed with signer %q, want %q", got, step.want))
					return
				}
			}
		}
	})
	if err := source.run(t.Context(), filewatch.Composite(events)); err != nil {
		t.Fatal(err)
	}
	if got := source.current.Load().signer.keyID; got != firstID {
		t.Fatalf("final signer = %q, want %q", got, firstID)
	}
}
