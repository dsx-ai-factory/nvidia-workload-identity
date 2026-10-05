package signingkey

import (
	"maps"
	"sync/atomic"

	"github.com/go-jose/go-jose/v4"
)

// Snapshot holds an immutable active signer and public keys.
type Snapshot struct {
	keyID  string
	signer jose.Signer
	public map[string]jose.JSONWebKey
}

func newSnapshot(keyID string, signer jose.Signer, keys []jose.JSONWebKey) *Snapshot {
	public := make(map[string]jose.JSONWebKey, len(keys))
	for _, key := range keys {
		public[key.KeyID] = key
	}
	return &Snapshot{keyID: keyID, signer: signer, public: public}
}

func (s *Snapshot) KeyID() string       { return s.keyID }
func (s *Snapshot) Signer() jose.Signer { return s.signer }

// PublicKeys returns a copy so callers cannot change the snapshot's key map.
func (s *Snapshot) PublicKeys() map[string]jose.JSONWebKey { return maps.Clone(s.public) }

// Source shares immutable snapshots and change notifications.
// File watchers and background workers belong to the reloader.
type Source struct {
	current atomic.Pointer[Snapshot]
	updated chan struct{}
}

// NewSource creates a source with a fixed initial snapshot.
func NewSource(keyID string, signer jose.Signer, keys []jose.JSONWebKey) *Source {
	return newSource(newSnapshot(keyID, signer, keys))
}

func newSource(initial *Snapshot) *Source {
	source := &Source{updated: make(chan struct{}, 1)}
	source.current.Store(initial)
	return source
}

// Get returns an immutable snapshot. Consumers must not modify it.
func (s *Source) Get() *Snapshot { return s.current.Load() }

// Watch supplies coalesced wake-ups for one observer. Read Get after each wake-up.
func (s *Source) Watch() <-chan struct{} { return s.updated }

func (s *Source) store(keys *Snapshot) {
	s.current.Store(keys)
	// A pending wake-up is sufficient because the observer reads Get after receiving it.
	select {
	case s.updated <- struct{}{}:
	default:
	}
}
