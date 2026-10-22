// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package keydistribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"dsx-identity/internal/signingkey"
)

const (
	DefaultBucket = "DSX_IDENTITY_KEYS"
	// Stored keys use shard.<shard-id>.kid.<kid>.
	keyKVPrefix = "shard."
	// The watch filter shard.> receives every shard's stored keys.
	keyKVWildcard           = keyKVPrefix + ">"
	natsReconnectWait       = time.Second
	natsKVBucketDescription = "NVIDIA Workload Identity Keys public signing keys"
)

type Synchronizer struct {
	connection *nats.Conn
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	publisher  keyPublisher
	kv         KVConfig
}

// SigningSource supplies snapshots and publication wake-ups.
type SigningSource interface {
	Get() *signingkey.Snapshot
	Watch() <-chan struct{}
}

type keyPublisher struct {
	shardID string
	source  SigningSource
	ttl     time.Duration
}

type remoteKeyRegistry interface {
	ReplaceRemote(string, jose.JSONWebKey, time.Time) error
	RemoveRemote(string)
}

type keySubscriber struct {
	registry remoteKeyRegistry
	ttl      time.Duration
}

type keyWatcher struct {
	bucket     jetstream.KeyValue
	subscriber *keySubscriber
	watcher    jetstream.KeyWatcher
}

// SynchronizedRegistry composes key storage with its independent NATS
// lifecycle. The embedded registry remains responsible only for local and
// remote key aggregation.
type SynchronizedRegistry struct {
	*Registry
	*Synchronizer
}

func (r *SynchronizedRegistry) Ready() bool {
	return r.Synchronizer.Ready()
}

func (r *SynchronizedRegistry) Close() {
	r.Synchronizer.Close()
	r.Registry.Close()
}

type KVConfig struct {
	Bucket   string
	Replicas int
}

func NewSynchronizer(ctx context.Context, serverURL, clientName, shardID string, source SigningSource, ttl time.Duration, kvConfig KVConfig, registry remoteKeyRegistry, auth AuthConfig) (*Synchronizer, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("NATS server URL is required")
	}
	if clientName == "" {
		return nil, fmt.Errorf("NATS client name is required")
	}
	if len(source.Get().PublicKeys()) == 0 {
		return nil, fmt.Errorf("at least one public key is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("public key KV TTL must be positive")
	}
	if kvConfig.Bucket == "" {
		return nil, fmt.Errorf("NATS KV bucket is required")
	}
	if kvConfig.Replicas <= 0 {
		return nil, fmt.Errorf("NATS KV replicas must be positive")
	}
	syncContext, cancel := context.WithCancel(ctx)
	connection, err := connectNATS(syncContext, serverURL, clientName, auth)
	if err != nil {
		cancel()
		return nil, err
	}
	synchronizer := &Synchronizer{
		connection: connection,
		cancel:     cancel,
		publisher: keyPublisher{
			shardID: shardID,
			source:  source,
			ttl:     ttl,
		},
		kv: kvConfig,
	}
	js, err := jetstream.New(connection)
	if err != nil {
		synchronizer.Close()
		return nil, fmt.Errorf("create NATS JetStream client: %w", err)
	}
	bucket, err := synchronizer.openBucket(syncContext, js)
	if err != nil {
		synchronizer.Close()
		return nil, err
	}
	// Publication is independent. Startup neither waits for nor observes its
	// first write.
	synchronizer.workers.Go(func() { synchronizer.publishLocalKeys(syncContext, bucket) })
	watcher, err := newKeyWatcher(syncContext, bucket, &keySubscriber{registry: registry, ttl: ttl})
	if err != nil {
		synchronizer.Close()
		return nil, err
	}
	synchronizer.workers.Go(func() { watcher.run(syncContext) })
	return synchronizer, nil
}

func connectNATS(ctx context.Context, serverURL, clientName string, auth AuthConfig) (*nats.Conn, error) {
	authOptions, err := auth.options(ctx)
	if err != nil {
		return nil, err
	}
	options := []nats.Option{
		nats.Name(clientName),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(natsReconnectWait),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			slog.Warn("NATS KV synchronization error", "error", err)
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				slog.Warn("NATS KV synchronization disconnected", "error", err)
			}
		}),
	}
	connection, err := nats.Connect(serverURL, append(options, authOptions...)...)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	return connection, nil
}

func (s *Synchronizer) openBucket(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	// Expiration markers notify watchers promptly. NATS requires them to live
	// for at least one second, even when focused tests use a shorter key TTL.
	config := jetstream.KeyValueConfig{
		Bucket:         s.kv.Bucket,
		Description:    natsKVBucketDescription,
		History:        1,
		TTL:            s.publisher.ttl,
		Storage:        jetstream.MemoryStorage,
		Replicas:       s.kv.Replicas,
		LimitMarkerTTL: max(s.publisher.ttl, time.Second),
	}
	// Every instance owns the same bucket contract. Reconcile an existing
	// bucket so configuration drift does not prevent synchronization startup.
	bucket, err := js.CreateOrUpdateKeyValue(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open NATS KV bucket %q: %w", s.kv.Bucket, err)
	}
	return bucket, nil
}

func newKeyWatcher(ctx context.Context, bucket jetstream.KeyValue, subscriber *keySubscriber) (*keyWatcher, error) {
	watcher := &keyWatcher{
		bucket:     bucket,
		subscriber: subscriber,
	}
	if err := watcher.replaceWatcher(ctx); err != nil {
		return nil, err
	}
	return watcher, nil
}

func (w *keyWatcher) watchAndLoad(ctx context.Context) (jetstream.KeyWatcher, error) {
	// Start the steady-state watcher before History so concurrent writes queue
	// while the current values are loaded.
	watcher, err := w.bucket.Watch(ctx, keyKVWildcard, jetstream.UpdatesOnly())
	if err != nil {
		return nil, fmt.Errorf("watch NATS KV bucket %q: %w", w.bucket.Bucket(), err)
	}
	entries, err := w.bucket.History(ctx, keyKVWildcard)
	if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
		stopKeyWatcher(watcher)
		return nil, fmt.Errorf("load NATS KV bucket %q history: %w", w.bucket.Bucket(), err)
	}
	for _, entry := range entries {
		w.subscriber.receive(entry)
	}
	return watcher, nil
}

func (w *keyWatcher) replaceWatcher(ctx context.Context) error {
	next, err := w.watchAndLoad(ctx)
	if err != nil {
		return err
	}
	stopKeyWatcher(w.watcher)
	w.watcher = next
	return nil
}

func (s *Synchronizer) publishLocalKeys(ctx context.Context, bucket jetstream.KeyValue) {
	// Refreshing every quarter TTL lets a live replica miss three consecutive
	// writes before its local keys can expire from the shared bucket.
	refresh := time.NewTicker(max(s.publisher.ttl/4, time.Nanosecond))
	defer refresh.Stop()
	updated := s.publisher.source.Watch()
	for {
		if err := s.publisher.publish(ctx, bucket); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("NATS KV publication failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
		case <-updated:
		}
	}
}

func (w *keyWatcher) run(ctx context.Context) {
	defer func() { stopKeyWatcher(w.watcher) }()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-w.watcher.Updates():
			if !ok {
				slog.Warn("NATS KV watcher closed, rebuilding")
				if !w.rebuild(ctx) {
					return
				}
				continue
			}
			w.subscriber.receive(entry)
		}
	}
}

func stopKeyWatcher(watcher jetstream.KeyWatcher) {
	if watcher == nil {
		return
	}
	_ = watcher.Stop()
	// Watch callbacks block when their update buffer is full. Drain through
	// closure so replacing or stopping a busy watcher cannot strand its sender.
	for range watcher.Updates() {
	}
}

func (w *keyWatcher) rebuild(ctx context.Context) bool {
	for {
		if err := w.replaceWatcher(ctx); err == nil {
			return true
		} else {
			slog.Warn("NATS KV watcher rebuild failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(natsReconnectWait):
		}
	}
}

func (s *Synchronizer) Ready() bool {
	return s.connection.IsConnected()
}

func (s *Synchronizer) Close() {
	// The background loop owns its watcher. Let it stop the watcher before the
	// connection it uses is closed.
	s.cancel()
	s.workers.Wait()
	s.connection.Close()
}

func (p *keyPublisher) publish(ctx context.Context, bucket jetstream.KeyValue) error {
	for _, key := range p.source.Get().PublicKeys() {
		payload, err := json.Marshal(key)
		if err != nil {
			return fmt.Errorf("serialize public key %q: %w", key.KeyID, err)
		}
		if _, err := bucket.Put(ctx, keyKVKey(p.shardID, key.KeyID), payload); err != nil {
			return fmt.Errorf("put local public key %q: %w", key.KeyID, err)
		}
	}
	return nil
}

func (s *keySubscriber) receive(entry jetstream.KeyValueEntry) {
	keyID, ok := parseKeyKVKey(entry.Key())
	if !ok {
		slog.Warn("Ignoring NATS KV entry with invalid key", "key", entry.Key())
		return
	}
	if entry.Operation() != jetstream.KeyValuePut {
		s.registry.RemoveRemote(keyID)
		return
	}
	var key jose.JSONWebKey
	if err := json.Unmarshal(entry.Value(), &key); err != nil {
		slog.Warn("Ignoring invalid public JWK", "key", entry.Key(), "error", err)
		return
	}
	if err := signingkey.Validate(&key, true); err != nil {
		slog.Warn("Ignoring invalid public JWK", "key", entry.Key(), "error", err)
		return
	}
	if err := s.registry.ReplaceRemote(keyID, key, entry.Created().Add(s.ttl)); err != nil {
		slog.Warn("Ignoring public JWK", "key", entry.Key(), "error", err)
		return
	}
	slog.Info("Accepted public JWK", "key", entry.Key(), "revision", entry.Revision())
}

func keyKVKey(shardID, keyID string) string {
	return keyKVPrefix + shardID + ".kid." + keyID
}

func parseKeyKVKey(key string) (string, bool) {
	// A valid stored key has four tokens: shard.<shard-id>.kid.<kid>.
	parts := strings.Split(key, ".")
	if len(parts) != 4 || parts[0] != "shard" || parts[1] == "" || parts[2] != "kid" {
		return "", false
	}
	keyID := parts[3]
	if signingkey.ValidateKeyID(keyID) != nil {
		return "", false
	}
	return keyID, true
}
