package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jellydator/ttlcache/v3"
)

type keyRegistry struct {
	localMu sync.RWMutex
	local   map[string]jose.JSONWebKey
	remote  *ttlcache.Cache[string, jose.JSONWebKey]
}

func newKeyRegistry(localKeys []jose.JSONWebKey) *keyRegistry {
	local := make(map[string]jose.JSONWebKey, len(localKeys))
	for _, key := range localKeys {
		local[key.KeyID] = key
	}
	registry := &keyRegistry{
		local:  local,
		remote: ttlcache.New[string, jose.JSONWebKey](),
	}
	go registry.remote.Start()
	return registry
}

func (r *keyRegistry) Close() {
	r.remote.Stop()
}

func (r *keyRegistry) isReady() bool {
	return true
}

func (r *keyRegistry) replaceRemote(keyID string, key jose.JSONWebKey, expiresAt time.Time) error {
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

func (r *keyRegistry) removeRemote(keyID string) {
	r.remote.Delete(keyID)
}

func (r *keyRegistry) keyset() jose.JSONWebKeySet {
	r.localMu.RLock()
	defer r.localMu.RUnlock()
	keys := slices.Collect(maps.Values(r.local))
	for _, item := range r.remote.Items() {
		if _, local := r.local[item.Key()]; !local {
			keys = append(keys, item.Value())
		}
	}
	slices.SortFunc(keys, func(a, b jose.JSONWebKey) int {
		return strings.Compare(a.KeyID, b.KeyID)
	})
	return jose.JSONWebKeySet{Keys: keys}
}

func signingAlgorithms(keys jose.JSONWebKeySet) []string {
	algorithms := make(map[string]struct{}, len(keys.Keys))
	for _, key := range keys.Keys {
		algorithms[key.Algorithm] = struct{}{}
	}
	return slices.Sorted(maps.Keys(algorithms))
}

func validateKeyID(keyID string) error {
	// A kid occupies one NATS subject token. Wildcards and separators would
	// change subscription scope instead of naming a key.
	if keyID == "" || strings.ContainsAny(keyID, ".>* \t\r\n") {
		return fmt.Errorf("kid %q is not a valid NATS subject token", keyID)
	}
	return nil
}

func (r *keyRegistry) replaceLocal(keys []jose.JSONWebKey) {
	local := make(map[string]jose.JSONWebKey, len(keys))
	for _, key := range keys {
		local[key.KeyID] = key
	}
	r.localMu.Lock()
	r.local = local
	r.localMu.Unlock()
}
