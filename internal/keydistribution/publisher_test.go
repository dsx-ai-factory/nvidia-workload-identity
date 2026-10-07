// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package keydistribution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/nats-io/nats.go/jetstream"

	"dsx-identity/internal/keytest"
	"dsx-identity/internal/signingkey"
)

// failingWrites preserves real reads while controlling the write error.
type failingWrites struct {
	jetstream.KeyValue
	err error
}

func (b failingWrites) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	if b.err != nil {
		return 0, b.err
	}
	return b.KeyValue.Put(ctx, key, value)
}

func TestPublisherReturnsWriteFailureAndRecovers(t *testing.T) {
	broker := keytest.RunTestNATSServer(t, "")
	js := keytest.TestJetStream(t, broker.ClientURL())
	bucket, err := js.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{Bucket: "WRITE_RECOVERY", Storage: jetstream.MemoryStorage})
	if err != nil {
		t.Fatal(err)
	}
	key := keytest.TestJWK(t, "local", true)
	source := signingkey.NewSource("", nil, []jose.JSONWebKey{key})
	publisher := keyPublisher{shardID: "local", source: source, ttl: time.Minute}
	writeErr := errors.New("write unavailable")
	if err := publisher.publish(t.Context(), failingWrites{KeyValue: bucket, err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("publish error = %v, want %v", err, writeErr)
	}
	if _, err := bucket.Get(t.Context(), keyKVKey("local", key.KeyID)); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Fatalf("failed write left entry: %v", err)
	}
	if err := publisher.publish(t.Context(), failingWrites{KeyValue: bucket}); err != nil {
		t.Fatal(err)
	}
	entry, err := bucket.Get(t.Context(), keyKVKey("local", key.KeyID))
	if err != nil {
		t.Fatal(err)
	}
	registry := newTestKeyRegistry(t, nil)
	subscriber := keySubscriber{registry: registry, ttl: time.Minute}
	subscriber.receive(entry)
	if got := registry.Keyset().Keys; !keytest.VerifyJWKPresent(got, key) {
		t.Fatalf("published key missing from subscriber: %v", got)
	}
}
