package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/go-jose/go-jose/v4"

	"dsx-identity/internal/filewatch"
)

type signingKeys struct {
	signer *tokenSigner
	public map[string]jose.JSONWebKey
}

func newSigningKeys(signer *tokenSigner, keys []jose.JSONWebKey) *signingKeys {
	public := make(map[string]jose.JSONWebKey, len(keys))
	for _, key := range keys {
		public[key.KeyID] = key
	}
	return &signingKeys{signer: signer, public: public}
}

// signingKeySource shares immutable snapshots and change notifications.
// File watchers and background workers belong to the reloader.
type signingKeySource struct {
	current atomic.Pointer[signingKeys]
	updated chan struct{}
}

func newSigningKeySource(initial *signingKeys) *signingKeySource {
	source := &signingKeySource{updated: make(chan struct{}, 1)}
	source.current.Store(initial)
	return source
}

// Get returns an immutable snapshot. Consumers must not modify it.
func (s *signingKeySource) Get() *signingKeys { return s.current.Load() }

// Watch supplies coalesced wake-ups for one observer. Read Get after each wake-up.
func (s *signingKeySource) Watch() <-chan struct{} { return s.updated }

func (s *signingKeySource) store(keys *signingKeys) {
	s.current.Store(keys)
	// A pending wake-up is sufficient because the observer reads Get after receiving it.
	select {
	case s.updated <- struct{}{}:
	default:
	}
}

// signingKeyReloader owns file-watching lifetimes and parsing state.
// Its source shares only snapshot data and notifications with readers.
type signingKeyReloader struct {
	keysetPath    string
	activeKIDPath string
	initialKeyset []byte
	initialActive []byte
	source        *signingKeySource
}

func newSigningKeyReloader(keysetPath, activeKIDPath string) (*signingKeyReloader, error) {
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		return nil, fmt.Errorf("read signing keyset: %w", err)
	}
	rawActive, err := os.ReadFile(activeKIDPath)
	if err != nil {
		return nil, fmt.Errorf("read active kid: %w", err)
	}
	signer, public, err := parseSigningKeys(rawKeyset, rawActive)
	if err != nil {
		return nil, err
	}
	return &signingKeyReloader{
		keysetPath:    keysetPath,
		activeKIDPath: activeKIDPath,
		initialKeyset: rawKeyset,
		initialActive: rawActive,
		source:        newSigningKeySource(newSigningKeys(signer, public)),
	}, nil
}

func (s *signingKeyReloader) load(rawKeyset, rawActive []byte) error {
	signer, publicKeys, err := parseSigningKeys(rawKeyset, rawActive)
	if err != nil {
		return err
	}
	s.source.store(newSigningKeys(signer, publicKeys))
	slog.Info("Loaded signing keys", "active_kid", signer.keyID, "public_key_count", len(publicKeys))
	return nil
}

func (s *signingKeyReloader) run(ctx context.Context, watcher filewatch.Watcher[filewatch.Change]) error {
	keyset, active := s.initialKeyset, s.initialActive
	var lastError string
	err := watcher(ctx, func(change filewatch.Change) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Keep latest observations so changing either file can repair a rejected pair.
		switch change.Path {
		case s.keysetPath:
			keyset = change.Contents
		case s.activeKIDPath:
			active = change.Contents
		}
		err := s.load(keyset, active)
		if err != nil {
			if err.Error() != lastError {
				slog.WarnContext(ctx, "Signing key reload failed, retaining current keys", "error", err)
			}
			lastError = err.Error()
		} else {
			lastError = ""
		}
		return err
	})
	if ctx.Err() != nil {
		return nil
	}
	return err
}
