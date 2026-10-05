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

type signingKeySource struct {
	keysetPath    string
	activeKIDPath string
	current       atomic.Pointer[signingKeys]
	updated       chan struct{}
	initialKeyset []byte
	initialActive []byte
}

func newSigningKeySource(keysetPath, activeKIDPath string) (*signingKeySource, error) {
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		return nil, fmt.Errorf("read signing keyset: %w", err)
	}
	rawActive, err := os.ReadFile(activeKIDPath)
	if err != nil {
		return nil, fmt.Errorf("read active kid: %w", err)
	}
	source := &signingKeySource{
		keysetPath:    keysetPath,
		activeKIDPath: activeKIDPath,
		initialKeyset: rawKeyset,
		initialActive: rawActive,
		updated:       make(chan struct{}, 1),
	}
	if err := source.load(rawKeyset, rawActive); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *signingKeySource) load(rawKeyset, rawActive []byte) error {
	signer, publicKeys, err := parseSigningKeys(rawKeyset, rawActive)
	if err != nil {
		return err
	}
	// Consumers share one immutable snapshot of the validated source pair.
	s.current.Store(newSigningKeys(signer, publicKeys))
	// Coalesce notifications. The publisher always reads the current snapshot.
	select {
	case s.updated <- struct{}{}:
	default:
	}
	slog.Info("Loaded signing keys", "active_kid", signer.keyID, "public_key_count", len(publicKeys))
	return nil
}

func (s *signingKeySource) run(ctx context.Context, watcher filewatch.Watcher[filewatch.Change]) error {
	keyset, active := s.initialKeyset, s.initialActive
	var lastError string
	err := watcher(ctx, func(change filewatch.Change) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
