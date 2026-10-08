// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package keydistribution

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jellydator/ttlcache/v3"

	"dsx-identity/internal/signingkey"
)

// LocalSource supplies the current local public keys.
type LocalSource interface {
	Get() *signingkey.Snapshot
}

type Registry struct {
	local  LocalSource
	remote *ttlcache.Cache[string, jose.JSONWebKey]
}

func NewRegistry(local LocalSource) *Registry {
	registry := &Registry{
		local:  local,
		remote: ttlcache.New[string, jose.JSONWebKey](),
	}
	go registry.remote.Start()
	return registry
}

func (r *Registry) Close() {
	r.remote.Stop()
}

func (r *Registry) Ready() bool {
	return true
}

func (r *Registry) ReplaceRemote(keyID string, key jose.JSONWebKey, expiresAt time.Time) error {
	if key.KeyID != keyID {
		return fmt.Errorf("KV key kid %q does not match JWK kid %q", keyID, key.KeyID)
	}
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return fmt.Errorf("public key %q KV entry has expired", keyID)
	}
	// Keep leases for local keys too so removal honors other replicas until
	// their publications expire. Local keys take precedence in keyset().
	r.remote.Set(keyID, key, ttl)
	return nil
}

func (r *Registry) RemoveRemote(keyID string) {
	r.remote.Delete(keyID)
}

func (r *Registry) Keyset() jose.JSONWebKeySet {
	local := r.local.Get().PublicKeys()
	keys := slices.Collect(maps.Values(local))
	for _, item := range r.remote.Items() {
		if _, present := local[item.Key()]; !present {
			keys = append(keys, item.Value())
		}
	}
	slices.SortFunc(keys, func(a, b jose.JSONWebKey) int {
		return strings.Compare(a.KeyID, b.KeyID)
	})
	return jose.JSONWebKeySet{Keys: keys}
}

func SigningAlgorithms(keys jose.JSONWebKeySet) []string {
	algorithms := make(map[string]struct{}, len(keys.Keys))
	for _, key := range keys.Keys {
		algorithms[key.Algorithm] = struct{}{}
	}
	return slices.Sorted(maps.Keys(algorithms))
}
