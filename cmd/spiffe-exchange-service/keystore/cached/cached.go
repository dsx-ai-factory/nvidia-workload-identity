// Package cached provides a caching wrapper for KeyStore implementations.
package cached

import (
	"fmt"
	"io"
	"time"

	"github.com/jellydator/ttlcache/v3"

	"spiffe-jwt-issuer/cmd/spiffe-exchange-service/keystore"
)

// DefaultTTL is the default time-to-live for cached entries.
const DefaultTTL = 60 * time.Second

// KeyStore wraps a keystore.KeyStore with an in-memory TTL cache and singleflight
// deduplication to reduce backend load.
type KeyStore struct {
	backend   keystore.KeyStore
	keyCache  *ttlcache.Cache[string, keystore.DomainKey]
	keysCache *ttlcache.Cache[string, []keystore.DomainKey]
}

// New creates a new cached key store wrapping the given backend.
// The cache uses singleflight to deduplicate concurrent requests for the same
// trust domain. If ttl is <= 0, DefaultTTL (60s) is used.
func New(backend keystore.KeyStore, ttl time.Duration) *KeyStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	c := &KeyStore{
		backend: backend,
		keyCache: ttlcache.New(
			ttlcache.WithTTL[string, keystore.DomainKey](ttl),
			ttlcache.WithDisableTouchOnHit[string, keystore.DomainKey](),
			ttlcache.WithLoader(ttlcache.NewSuppressedLoader(ttlcache.LoaderFunc[string, keystore.DomainKey](
				func(cache *ttlcache.Cache[string, keystore.DomainKey], trustDomain string) *ttlcache.Item[string, keystore.DomainKey] {
					dk, err := backend.GetOrCreateKey(trustDomain)
					if err != nil {
						return nil
					}
					return cache.Set(trustDomain, dk, ttlcache.DefaultTTL)
				},
			), nil)),
		),
		keysCache: ttlcache.New(
			ttlcache.WithTTL[string, []keystore.DomainKey](ttl),
			ttlcache.WithDisableTouchOnHit[string, []keystore.DomainKey](),
			ttlcache.WithLoader(ttlcache.NewSuppressedLoader(ttlcache.LoaderFunc[string, []keystore.DomainKey](
				func(cache *ttlcache.Cache[string, []keystore.DomainKey], trustDomain string) *ttlcache.Item[string, []keystore.DomainKey] {
					keys, err := backend.GetKeys(trustDomain)
					if err != nil {
						return nil
					}
					return cache.Set(trustDomain, keys, ttlcache.DefaultTTL)
				},
			), nil)),
		),
	}

	go c.keyCache.Start()
	go c.keysCache.Start()

	return c
}

// GetOrCreateKey returns the key for a trust domain.
// Results are cached with TTL. Concurrent requests for the same trust domain
// are deduplicated via singleflight.
func (c *KeyStore) GetOrCreateKey(trustDomain string) (keystore.DomainKey, error) {
	item := c.keyCache.Get(trustDomain)
	if item == nil {
		return keystore.DomainKey{}, fmt.Errorf("failed to get or create key for trust domain %s", trustDomain)
	}
	return item.Value(), nil
}

// GetKeys returns all keys for a trust domain.
// Results are cached with TTL. Concurrent requests for the same trust domain
// are deduplicated via singleflight.
func (c *KeyStore) GetKeys(trustDomain string) ([]keystore.DomainKey, error) {
	item := c.keysCache.Get(trustDomain)
	if item == nil {
		return nil, fmt.Errorf("failed to get keys for trust domain %s", trustDomain)
	}
	return item.Value(), nil
}

// Close stops the cache cleanup goroutines and closes the backend if it
// implements io.Closer.
func (c *KeyStore) Close() error {
	c.keyCache.Stop()
	c.keysCache.Stop()
	if closer, ok := c.backend.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
