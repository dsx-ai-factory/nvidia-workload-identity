// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package keydistribution

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"dsx-identity/internal/keytest"
	"dsx-identity/internal/signingkey"
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
	js := keytest.TestJetStream(t, serverURL, nats.NoReconnect())
	ctx, cancel := context.WithTimeout(t.Context(), natsOperationTimeout)
	defer cancel()
	bucket, err := js.KeyValue(ctx, DefaultBucket)
	if err != nil {
		t.Fatal(err)
	}
	keytest.RequireCondition(t, "deployed NATS KV keys", func() bool {
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
		return keytest.ContainsEveryKID(seen, expected)
	})
}

func TestNATSKVSynchronizesAndReconstructs(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "test-token")
	oauth := testNATSOAuthConfig(t, "test-token")
	ttl := 10 * time.Second
	kvConfig := testNATSKVConfig("SYNCHRONIZE")
	firstKey := keytest.TestJWK(t, "first", true)
	secondKey := keytest.TestJWK(t, "second", true)

	firstRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, oauth)
	if got := keytest.KeyIDs(firstRegistry); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("initial key IDs = %v, want [first]", got)
	}

	secondRegistry := startTestKeyRegistry(t, broker.ClientURL(), "second", "shard-two", []jose.JSONWebKey{secondKey}, ttl, kvConfig, oauth)
	keytest.RequireKeyIDs(t, firstRegistry, "first", "second")
	keytest.RequireKeyIDs(t, secondRegistry, "first", "second")
	js, err := jetstream.New(firstRegistry.connection)
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := js.KeyValue(t.Context(), kvConfig.Bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.Put(t.Context(), keyKVKey("shard-two", secondKey.KeyID), []byte("{")); err != nil {
		t.Fatal(err)
	}
	keytest.RequireKeyIDs(t, firstRegistry, "first", "second")
	keytest.RequireKeyIDs(t, secondRegistry, "first", "second")
	if err := secondRegistry.publisher.publish(t.Context(), bucket); err != nil {
		t.Fatal(err)
	}
	keytest.RequireKeyIDs(t, firstRegistry, "first", "second")
	keytest.RequireKeyIDs(t, secondRegistry, "first", "second")

	firstRegistry.Close()
	secondRegistry.Close()
	restartedRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first-restarted", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, oauth)
	if got := keytest.KeyIDs(restartedRegistry); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("replayed key IDs = %v, want [first second]", got)
	}

	rotatedKey := keytest.TestJWK(t, "rotated", true)
	rotatedRegistry := startTestKeyRegistry(t, broker.ClientURL(), "second-rotated", "shard-two", []jose.JSONWebKey{secondKey, rotatedKey}, ttl, kvConfig, oauth)
	keytest.RequireKeyIDs(t, restartedRegistry, "first", "rotated", "second")
	keytest.RequireKeyIDs(t, rotatedRegistry, "first", "rotated", "second")
}

func TestNATSKVRefreshesAndWatchesRemoval(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	ttl := 10 * time.Second
	kvConfig := testNATSKVConfig("LEASES")
	firstKey := keytest.TestJWK(t, "first", true)
	secondKey := keytest.TestJWK(t, "second", true)

	firstRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, AuthConfig{Mode: AuthNone})
	firstReplicaRegistry := startTestKeyRegistry(t, broker.ClientURL(), "first-replica", "shard-one", []jose.JSONWebKey{firstKey}, ttl, kvConfig, AuthConfig{Mode: AuthNone})
	secondRegistry := startTestKeyRegistry(t, broker.ClientURL(), "second", "shard-two", []jose.JSONWebKey{secondKey}, ttl, kvConfig, AuthConfig{Mode: AuthNone})
	keytest.RequireKeyIDs(t, secondRegistry, "first", "second")

	js := keytest.TestJetStream(t, broker.ClientURL())
	bucket, err := js.KeyValue(t.Context(), kvConfig.Bucket)
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
	keytest.RequireCondition(t, "republished local KV key", func() bool {
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
	keytest.RequireCondition(t, "surviving replica key refresh", func() bool {
		entry, err := bucket.Get(t.Context(), keyKVKey("shard-one", firstKey.KeyID))
		return err == nil && entry.Revision() > firstBefore.Revision()
	})
	keytest.RequireKeyIDs(t, secondRegistry, "first", "second")
	firstReplicaRegistry.Close()
	if err := bucket.Purge(t.Context(), keyKVKey("shard-one", firstKey.KeyID)); err != nil {
		t.Fatal(err)
	}
	keytest.RequireKeyIDs(t, secondRegistry, "second")
}

func TestNATSKVReconcilesBucketConfiguration(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	js := keytest.TestJetStream(t, broker.ClientURL())
	kvConfig := testNATSKVConfig("RECONCILE")
	if _, err := js.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{
		Bucket: kvConfig.Bucket, TTL: time.Hour, Storage: jetstream.MemoryStorage, LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	ttl := 10 * time.Second
	startTestKeyRegistry(t, broker.ClientURL(), "reconcile", "shard-one", []jose.JSONWebKey{
		keytest.TestJWK(t, "first", true),
	}, ttl, kvConfig, AuthConfig{Mode: AuthNone})
	bucket, err := js.KeyValue(t.Context(), kvConfig.Bucket)
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
	broker := keytest.RunTestNATSServer(t, "")
	kvConfig := testNATSKVConfig("RECONNECT")
	registry := startTestKeyRegistry(t, broker.ClientURL(), "reconnect", "shard-one", []jose.JSONWebKey{
		keytest.TestJWK(t, "first", true),
	}, 10*time.Second, kvConfig, AuthConfig{Mode: AuthNone})

	js := keytest.TestJetStream(t, broker.ClientURL())
	bucket, err := js.KeyValue(t.Context(), kvConfig.Bucket)
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
	keytest.RequireNATSStatus(t, statuses, nats.RECONNECTING)
	secondKey := keytest.TestJWK(t, "second", true)
	payload, err := json.Marshal(secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.Put(t.Context(), keyKVKey("shard-two", secondKey.KeyID), payload); err != nil {
		t.Fatal(err)
	}
	keytest.RequireNATSStatus(t, statuses, nats.CONNECTED)
	keytest.RequireKeyIDs(t, registry, "first", "second")
}

func TestNATSKVPublisherRefreshesOnTicker(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	key := keytest.TestJWK(t, "first", true)
	kvConfig := testNATSKVConfig("TICKER")
	startTestKeyRegistry(t, broker.ClientURL(), "ticker", "shard-one", []jose.JSONWebKey{key}, 400*time.Millisecond, kvConfig, AuthConfig{Mode: AuthNone})
	bucket, err := keytest.TestJetStream(t, broker.ClientURL()).KeyValue(t.Context(), kvConfig.Bucket)
	if err != nil {
		t.Fatal(err)
	}
	var firstRevision uint64
	keytest.RequireCondition(t, "initial local key publication", func() bool {
		entry, err := bucket.Get(t.Context(), keyKVKey("shard-one", key.KeyID))
		if err == nil {
			firstRevision = entry.Revision()
		}
		return err == nil
	})
	keytest.RequireCondition(t, "ticker local key refresh", func() bool {
		entry, err := bucket.Get(t.Context(), keyKVKey("shard-one", key.KeyID))
		return err == nil && entry.Revision() > firstRevision
	})
}

func TestNATSKVWatcherRebuildsAfterClosure(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	js := keytest.TestJetStream(t, broker.ClientURL())
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
	key := keytest.TestJWK(t, "remote", true)
	payload, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bucket.Put(t.Context(), keyKVKey("shard-two", key.KeyID), payload); err != nil {
		t.Fatal(err)
	}
	keytest.RequireKeyIDs(t, registry, "remote")
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

func testNATSKVConfig(bucket string) KVConfig {
	return KVConfig{Bucket: bucket, Replicas: 1}
}

func startTestKeyRegistry(t *testing.T, serverURL, clientName, shardID string, keys []jose.JSONWebKey, ttl time.Duration, kvConfig KVConfig, auth AuthConfig) *SynchronizedRegistry {
	t.Helper()
	return startTestSourceRegistry(t, serverURL, clientName, shardID, signingkey.NewSource("", nil, keys), ttl, kvConfig, auth)
}

func startTestSourceRegistry(t *testing.T, serverURL, clientName, shardID string, source *signingkey.Source, ttl time.Duration, kvConfig KVConfig, auth AuthConfig) *SynchronizedRegistry {
	t.Helper()
	registry := NewRegistry(source)
	synchronizer, err := NewSynchronizer(t.Context(), serverURL, clientName, shardID, source, ttl, kvConfig, registry, auth)
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	registryWithSync := &SynchronizedRegistry{Registry: registry, Synchronizer: synchronizer}
	t.Cleanup(registryWithSync.Close)
	return registryWithSync
}

func TestRegistryExpiresKeysAtTheirKVDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := newTestKeyRegistry(t, nil)
		key := keytest.TestJWK(t, "expiring-kid", true)
		if err := registry.ReplaceRemote(key.KeyID, key, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if len(registry.Keyset().Keys) != 1 {
			t.Fatal("unexpired key was not available")
		}
		time.Sleep(time.Minute + time.Nanosecond)
		synctest.Wait()
		if registry.remote.Len() != 0 {
			t.Fatal("background expiry did not remove the key")
		}
		if len(registry.Keyset().Keys) != 0 {
			t.Fatal("expired key remained available")
		}
	})
}

func newTestKeyRegistry(t *testing.T, local []jose.JSONWebKey) *Registry {
	t.Helper()
	r := NewRegistry(signingkey.NewSource("", nil, local))
	t.Cleanup(r.Close)
	return r
}
