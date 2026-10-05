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
}

func newSigningKeySource(keysetPath, activeKIDPath string) (*signingKeySource, error) {
	source := &signingKeySource{
		keysetPath:    keysetPath,
		activeKIDPath: activeKIDPath,
		updated:       make(chan struct{}, 1),
	}
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		return nil, fmt.Errorf("read signing keyset: %w", err)
	}
	rawActive, err := os.ReadFile(activeKIDPath)
	if err != nil {
		return nil, fmt.Errorf("read active kid: %w", err)
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
	type observedFile struct {
		data []byte
		err  error
	}
	var keyset, active *observedFile
	var lastError string
	for change, err := range watcher(ctx) {
		if ctx.Err() != nil {
			return nil
		}
		if change.Path == "" {
			return err
		}
		observed := &observedFile{change.Contents, err}
		switch change.Path {
		case s.keysetPath:
			keyset = observed
		case s.activeKIDPath:
			active = observed
		}
		if keyset == nil || active == nil {
			continue
		}
		switch {
		case keyset.err != nil:
			err = fmt.Errorf("read signing keyset: %w", keyset.err)
		case active.err != nil:
			err = fmt.Errorf("read active kid: %w", active.err)
		default:
			err = s.load(keyset.data, active.data)
		}
		if err != nil {
			if err.Error() != lastError {
				slog.WarnContext(ctx, "Signing key reload failed, retaining current keys", "error", err)
			}
			lastError = err.Error()
		} else {
			lastError = ""
		}
	}
	return nil
}
