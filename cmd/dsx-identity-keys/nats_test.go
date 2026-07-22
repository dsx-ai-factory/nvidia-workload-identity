package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const natsOperationTimeout = 10 * time.Second

func TestLiveNATSKV(t *testing.T) {
	serverURL := os.Getenv("DSX_E2E_NATS_URL")
	if serverURL == "" {
		t.Skip("DSX_E2E_NATS_URL is not set")
	}
	var expected []string
	if err := json.Unmarshal([]byte(os.Getenv("DSX_E2E_NATS_EXPECTED_KIDS")), &expected); err != nil {
		t.Fatalf("parse DSX_E2E_NATS_EXPECTED_KIDS: %v", err)
	}
	js := testJetStream(t, serverURL, nats.NoReconnect())
	ctx, cancel := context.WithTimeout(t.Context(), natsOperationTimeout)
	defer cancel()
	bucket, err := js.KeyValue(ctx, defaultNATSKVBucket)
	if err != nil {
		t.Fatal(err)
	}
	requireCondition(t, "deployed NATS KV keys", func() bool {
		keys, err := bucket.Keys(ctx)
		if err != nil {
			return false
		}
		seen := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			if keyID, ok := parseKeyKVKey(key); ok {
				seen[keyID] = struct{}{}
			}
		}
		return containsEveryKID(seen, expected)
	})
}

func TestNATSKVSynchronizesAndReconstructs(t *testing.T) {
	broker := runTestNATSServer(t, "test-token")
	oauth := testNATSOAuthConfig(t, "test-token")
	ttl := 10 * time.Second
	kvConfig := testNATSKVConfig("SYNCHRONIZE")
	firstKey := testJWK(t, "first", true)
	secondKey := testJWK(t, "second", true)

	firstRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, oauth)
	if got := keyIDs(firstRegistry); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("initial key IDs = %v, want [first]", got)
	}

	secondRegistry := startTestKeyRegistry(t, broker.ClientURL(), "second", "shard-two", []jose.JSONWebKey{secondKey}, ttl, kvConfig, oauth)
	requireKeyIDs(t, firstRegistry, "first", "second")
	requireKeyIDs(t, secondRegistry, "first", "second")
	js, err := jetstream.New(firstRegistry.connection)
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := js.KeyValue(t.Context(), kvConfig.bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.Put(t.Context(), keyKVKey("shard-two", secondKey.KeyID), []byte("{")); err != nil {
		t.Fatal(err)
	}
	requireKeyIDs(t, firstRegistry, "first", "second")
	requireKeyIDs(t, secondRegistry, "first", "second")
	if err := secondRegistry.publisher.publish(t.Context(), bucket); err != nil {
		t.Fatal(err)
	}
	requireKeyIDs(t, firstRegistry, "first", "second")
	requireKeyIDs(t, secondRegistry, "first", "second")

	firstRegistry.Close()
	secondRegistry.Close()
	restartedRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first-restarted", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, oauth)
	if got := keyIDs(restartedRegistry); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("replayed key IDs = %v, want [first second]", got)
	}

	rotatedKey := testJWK(t, "rotated", true)
	rotatedRegistry := startTestKeyRegistry(t, broker.ClientURL(), "second-rotated", "shard-two", []jose.JSONWebKey{secondKey, rotatedKey}, ttl, kvConfig, oauth)
	requireKeyIDs(t, restartedRegistry, "first", "rotated", "second")
	requireKeyIDs(t, rotatedRegistry, "first", "rotated", "second")
}

func TestNATSKVRefreshesAndWatchesRemoval(t *testing.T) {
	broker := runTestNATSServer(t, "")
	ttl := 10 * time.Second
	kvConfig := testNATSKVConfig("LEASES")
	firstKey := testJWK(t, "first", true)
	secondKey := testJWK(t, "second", true)

	firstRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, natsAuthConfig{mode: natsAuthNone})
	firstReplicaRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first-replica", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, natsAuthConfig{mode: natsAuthNone})
	secondRegistry := startTestKeyRegistry(t, broker.ClientURL(), "second", "shard-two", []jose.JSONWebKey{secondKey}, ttl, kvConfig, natsAuthConfig{mode: natsAuthNone})
	requireKeyIDs(t, secondRegistry, "first", "second")

	js := testJetStream(t, broker.ClientURL())
	bucket, err := js.KeyValue(t.Context(), kvConfig.bucket)
	if err != nil {
		t.Fatal(err)
	}
	status, err := bucket.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.TTL() != ttl || status.History() != 1 || status.LimitMarkerTTL() != ttl {
		t.Fatalf("KV configuration = ttl %s, history %d, marker TTL %s", status.TTL(), status.History(), status.LimitMarkerTTL())
	}
	before, err := bucket.Get(t.Context(), keyKVKey("shard-two", secondKey.KeyID))
	if err != nil {
		t.Fatal(err)
	}
	if err := secondRegistry.publisher.publish(t.Context(), bucket); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, "republished local KV key", func() bool {
		after, err := bucket.Get(t.Context(), keyKVKey("shard-two", secondKey.KeyID))
		return err == nil && after.Revision() > before.Revision()
	})

	firstBefore, err := bucket.Get(t.Context(), keyKVKey("shard-one", firstKey.KeyID))
	if err != nil {
		t.Fatal(err)
	}
	firstRegistry.Close()
	if err := firstReplicaRegistry.publisher.publish(t.Context(), bucket); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, "surviving replica key refresh", func() bool {
		entry, err := bucket.Get(t.Context(), keyKVKey("shard-one", firstKey.KeyID))
		return err == nil && entry.Revision() > firstBefore.Revision()
	})
	requireKeyIDs(t, secondRegistry, "first", "second")
	firstReplicaRegistry.Close()
	if err := bucket.Purge(t.Context(), keyKVKey("shard-one", firstKey.KeyID)); err != nil {
		t.Fatal(err)
	}
	requireKeyIDs(t, secondRegistry, "second")
}

func TestNATSKVReconcilesBucketConfiguration(t *testing.T) {
	broker := runTestNATSServer(t, "")
	js := testJetStream(t, broker.ClientURL())
	kvConfig := testNATSKVConfig("RECONCILE")
	if _, err := js.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: kvConfig.bucket, TTL: time.Hour, Storage: jetstream.MemoryStorage, LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	ttl := 10 * time.Second
	startTestKeyRegistry(t, broker.ClientURL(), "reconcile", "shard-one", []jose.JSONWebKey{
		testJWK(t, "first", true),
	}, ttl, kvConfig, natsAuthConfig{mode: natsAuthNone})
	bucket, err := js.KeyValue(t.Context(), kvConfig.bucket)
	if err != nil {
		t.Fatal(err)
	}
	status, err := bucket.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.TTL() != ttl || status.LimitMarkerTTL() != ttl {
		t.Fatalf("reconciled KV configuration = ttl %s, marker TTL %s", status.TTL(), status.LimitMarkerTTL())
	}
}

func TestNATSKVReconnectReceivesMissedUpdates(t *testing.T) {
	broker := runTestNATSServer(t, "")
	kvConfig := testNATSKVConfig("RECONNECT")
	registry := startTestKeyRegistry(t, broker.ClientURL(), "reconnect", "shard-one", []jose.JSONWebKey{
		testJWK(t, "first", true),
	}, 10*time.Second, kvConfig, natsAuthConfig{mode: natsAuthNone})

	js := testJetStream(t, broker.ClientURL())
	bucket, err := js.KeyValue(t.Context(), kvConfig.bucket)
	if err != nil {
		t.Fatal(err)
	}
	statuses := registry.connection.StatusChanged(nats.RECONNECTING, nats.CONNECTED)
	clientID, err := registry.connection.GetClientID()
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.DisconnectClientByID(clientID); err != nil {
		t.Fatal(err)
	}
	requireNATSStatus(t, statuses, nats.RECONNECTING)
	secondKey := testJWK(t, "second", true)
	payload, err := json.Marshal(secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.Put(t.Context(), keyKVKey("shard-two", secondKey.KeyID), payload); err != nil {
		t.Fatal(err)
	}
	requireNATSStatus(t, statuses, nats.CONNECTED)
	requireKeyIDs(t, registry, "first", "second")
}

func TestNATSKVPublisherRefreshesOnTicker(t *testing.T) {
	broker := runTestNATSServer(t, "")
	key := testJWK(t, "first", true)
	kvConfig := testNATSKVConfig("TICKER")
	startTestKeyRegistry(t, broker.ClientURL(), "ticker", "shard-one", []jose.JSONWebKey{key}, 400*time.Millisecond, kvConfig, natsAuthConfig{mode: natsAuthNone})
	bucket, err := testJetStream(t, broker.ClientURL()).KeyValue(t.Context(), kvConfig.bucket)
	if err != nil {
		t.Fatal(err)
	}
	var firstRevision uint64
	requireCondition(t, "initial local key publication", func() bool {
		entry, err := bucket.Get(t.Context(), keyKVKey("shard-one", key.KeyID))
		if err == nil {
			firstRevision = entry.Revision()
		}
		return err == nil
	})
	requireCondition(t, "ticker local key refresh", func() bool {
		entry, err := bucket.Get(t.Context(), keyKVKey("shard-one", key.KeyID))
		return err == nil && entry.Revision() > firstRevision
	})
}

func TestNATSKVWatcherRebuildsAfterClosure(t *testing.T) {
	broker := runTestNATSServer(t, "")
	js := testJetStream(t, broker.ClientURL())
	bucket, err := js.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: "WATCHER_REBUILD", TTL: 10 * time.Second, Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestKeyRegistry(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	watcher, err := newKeyWatcher(ctx, bucket, &keySubscriber{registry: registry, ttl: 10 * time.Second})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		watcher.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	if err := watcher.watcher.Stop(); err != nil {
		t.Fatal(err)
	}
	key := testJWK(t, "remote", true)
	payload, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.Put(t.Context(), keyKVKey("shard-two", key.KeyID), payload); err != nil {
		t.Fatal(err)
	}
	requireKeyIDs(t, registry, "remote")
}

func TestKeyRegistryReadinessOwnsSynchronization(t *testing.T) {
	broker := runTestNATSServer(t, "")
	registry := startTestKeyRegistry(t, broker.ClientURL(), "readiness", "shard-one", []jose.JSONWebKey{
		testJWK(t, "first", true),
	}, 10*time.Second, testNATSKVConfig("READINESS"), natsAuthConfig{mode: natsAuthNone})
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

func TestNATSKVKey(t *testing.T) {
	key := keyKVKey("shard-one", "kid-one")
	if keyID, ok := parseKeyKVKey(key); !ok || keyID != "kid-one" {
		t.Fatalf("parseKeyKVKey(%q) = %q, %t", key, keyID, ok)
	}
	for _, invalid := range []string{"", "shard.bad", "shard.one.kid.", "shard.one.kid.bad.kid"} {
		if _, ok := parseKeyKVKey(invalid); ok {
			t.Fatalf("invalid NATS KV key %q was accepted", invalid)
		}
	}
}

func runTestNATSServer(t *testing.T, token string) *server.Server {
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

func testNATSKVConfig(bucket string) natsKVConfig {
	return natsKVConfig{bucket: bucket, replicas: 1}
}

func testJetStream(t *testing.T, serverURL string, options ...nats.Option) jetstream.JetStream {
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

func startTestKeyRegistry(t *testing.T, serverURL, clientName, shardID string, keys []jose.JSONWebKey, ttl time.Duration, kvConfig natsKVConfig, auth natsAuthConfig) *synchronizedKeyRegistry {
	t.Helper()
	registry := newKeyRegistry(keys)
	synchronizer, err := newNATSKeySynchronizer(t.Context(), serverURL, clientName, shardID, keys, ttl, kvConfig, registry, auth)
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	registryWithSync := &synchronizedKeyRegistry{keyRegistry: registry, natsKeySynchronizer: synchronizer}
	t.Cleanup(registryWithSync.Close)
	return registryWithSync
}

func requireKeyIDs(t *testing.T, registry interface{ keyset() jose.JSONWebKeySet }, want ...string) {
	t.Helper()
	slices.Sort(want)
	requireCondition(t, fmt.Sprintf("key IDs %v", want), func() bool {
		return slices.Equal(keyIDs(registry), want)
	})
}

func keyIDs(registry interface{ keyset() jose.JSONWebKeySet }) []string {
	keys := registry.keyset().Keys
	keyIDs := make([]string, len(keys))
	for i, key := range keys {
		keyIDs[i] = key.KeyID
	}
	slices.Sort(keyIDs)
	return keyIDs
}

func requireCondition(t *testing.T, description string, ready func() bool) {
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

func requireNATSStatus(t *testing.T, statuses <-chan nats.Status, want nats.Status) {
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

func containsEveryKID(seen map[string]struct{}, keyIDs []string) bool {
	for _, keyID := range keyIDs {
		if _, ok := seen[keyID]; !ok {
			return false
		}
	}
	return true
}
