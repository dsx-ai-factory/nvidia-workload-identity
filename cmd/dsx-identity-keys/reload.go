package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/go-jose/go-jose/v4"
)

type signingKeys struct {
	signer    *tokenSigner
	public    map[string]jose.JSONWebKey
	rawKeyset []byte
	rawActive []byte
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
}

func newSigningKeySource(keysetPath, activeKIDPath string) (*signingKeySource, error) {
	source := &signingKeySource{
		keysetPath:    keysetPath,
		activeKIDPath: activeKIDPath,
		updated:       make(chan struct{}, 1),
	}
	if err := source.reload(); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *signingKeySource) reload() error {
	rawKeyset, err := os.ReadFile(s.keysetPath)
	if err != nil {
		return fmt.Errorf("read signing keyset: %w", err)
	}
	rawActive, err := os.ReadFile(s.activeKIDPath)
	if err != nil {
		return fmt.Errorf("read active kid: %w", err)
	}
	current := s.current.Load()
	if current != nil && bytes.Equal(current.rawKeyset, rawKeyset) && bytes.Equal(current.rawActive, rawActive) {
		return nil
	}
	signer, publicKeys, err := parseSigningKeys(rawKeyset, rawActive)
	if err != nil {
		return err
	}
	next := newSigningKeys(signer, publicKeys)
	next.rawKeyset, next.rawActive = rawKeyset, rawActive
	// Consumers share one immutable snapshot of the validated source pair.
	s.current.Store(next)
	// Coalesce notifications. The publisher always reads the current snapshot.
	select {
	case s.updated <- struct{}{}:
	default:
	}
	slog.Info("Loaded signing keys", "active_kid", signer.keyID, "public_key_count", len(publicKeys))
	return nil
}

func (s *signingKeySource) run(ctx context.Context, changed <-chan struct{}) {
	var lastError string
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			if ctx.Err() != nil {
				return
			}
			err := s.reload()
			if err != nil {
				if err.Error() != lastError {
					slog.WarnContext(ctx, "Signing key reload failed, retaining current keys", "error", err)
				}
				lastError = err.Error()
			} else {
				lastError = ""
			}
		}
	}
}
