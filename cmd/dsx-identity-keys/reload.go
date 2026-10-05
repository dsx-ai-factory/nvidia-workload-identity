package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"time"
)

func (s *identityKeysServer) reloadSigningKeys(ctx context.Context, keysetPath, activeKIDPath string, registry *keyRegistry, synchronizer *natsKeySynchronizer) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var keysetHash, activeHash [sha256.Size]byte
	var lastError string
	for {
		if ctx.Err() != nil {
			return
		}
		err := func() error {
			rawKeyset, err := os.ReadFile(keysetPath)
			if err != nil {
				return fmt.Errorf("read signing keyset: %w", err)
			}
			rawActive, err := os.ReadFile(activeKIDPath)
			if err != nil {
				return fmt.Errorf("read active kid: %w", err)
			}
			nextKeysetHash, nextActiveHash := sha256.Sum256(rawKeyset), sha256.Sum256(rawActive)
			if keysetHash == nextKeysetHash && activeHash == nextActiveHash {
				return nil
			}
			signer, publicKeys, err := parseSigningKeys(rawKeyset, rawActive)
			if err != nil {
				return err
			}
			// Both sources must validate before replacing any running state.
			s.signerMu.Lock()
			registry.replaceLocal(publicKeys)
			s.signer = signer
			s.signerMu.Unlock()
			if synchronizer != nil {
				synchronizer.replaceLocal(publicKeys)
			}
			keysetHash, activeHash = nextKeysetHash, nextActiveHash
			slog.InfoContext(ctx, "Reloaded signing keys", "active_kid", signer.keyID, "public_key_count", len(publicKeys))
			return nil
		}()
		if err != nil {
			if err.Error() != lastError {
				slog.WarnContext(ctx, "Signing key reload failed, retaining current keys", "error", err)
			}
			lastError = err.Error()
		} else {
			lastError = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
