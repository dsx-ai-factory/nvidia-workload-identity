// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package signingkey

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"dsx-identity/internal/filewatch"
	"dsx-identity/internal/watch"
)

// Reloader owns file-watching lifetimes and parsing state.
// Its source shares only snapshot data and notifications with readers.
type Reloader struct {
	keysetPath    string
	activeKIDPath string
	initialKeyset []byte
	initialActive []byte
	source        *Source
}

func NewReloader(keysetPath, activeKIDPath string) (*Reloader, error) {
	rawKeyset, err := os.ReadFile(keysetPath)
	if err != nil {
		return nil, fmt.Errorf("read signing keyset: %w", err)
	}
	rawActive, err := os.ReadFile(activeKIDPath)
	if err != nil {
		return nil, fmt.Errorf("read active kid: %w", err)
	}
	snapshot, err := parseSigningKeys(rawKeyset, rawActive)
	if err != nil {
		return nil, err
	}
	return &Reloader{
		keysetPath:    keysetPath,
		activeKIDPath: activeKIDPath,
		initialKeyset: rawKeyset,
		initialActive: rawActive,
		source:        newSource(snapshot),
	}, nil
}

func (s *Reloader) load(rawKeyset, rawActive []byte) error {
	snapshot, err := parseSigningKeys(rawKeyset, rawActive)
	if err != nil {
		return err
	}
	s.source.store(snapshot)
	slog.Info("Loaded signing keys", "active_kid", snapshot.keyID, "public_key_count", len(snapshot.public))
	return nil
}

func (s *Reloader) Run(ctx context.Context, watcher watch.Watcher[filewatch.Change]) error {
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

// Source shares snapshots without exposing the reloader runtime.
func (s *Reloader) Source() *Source { return s.source }
