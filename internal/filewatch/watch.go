// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package filewatch

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"dsx-identity/internal/watch"
)

// Change holds bytes from one read. Nil contents invalidate an unavailable file.
// Consumers must not modify the contents.
type Change struct {
	Path     string
	Contents []byte
}

// FSNotify reports parent directory events. Events can be lost, so compose it
// with Polling to recover the file's current contents.
func FSNotify(path string) watch.Watcher[struct{}] {
	return func(ctx context.Context, notify func(struct{}) error) error {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return fmt.Errorf("create file watcher: %w", err)
		}
		defer watcher.Close()
		// Parent watches survive atomic replacements and Kubernetes ..data swaps.
		if err := watcher.Add(filepath.Dir(path)); err != nil {
			return fmt.Errorf("watch parent of %q: %w", path, err)
		}
		if err := notify(struct{}{}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case _, ok := <-watcher.Events:
				if !ok {
					return fmt.Errorf("file watcher for %q closed", path)
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return fmt.Errorf("file watcher for %q closed", path)
				}
				// Removal and backend overflow errors require another content check.
			}
			if err := notify(struct{}{}); err != nil {
				return err
			}
		}
	}
}

// Contents advances its comparison baseline only after consumer acceptance.
// Read failures and rejected changes retry on later events or polls, including
// unchanged contents. Delivery uses captured bytes even if the file changes
// while the consumer processes them.
func Contents(path string, events watch.Watcher[struct{}]) watch.Watcher[Change] {
	return func(ctx context.Context, notify func(Change) error) error {
		var previous []byte
		var lastReadError string
		retry := true
		return events(ctx, func(struct{}) error {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				if readErr.Error() != lastReadError {
					slog.WarnContext(ctx, "Failed to read watched file", "path", path, "error", readErr)
					lastReadError = readErr.Error()
				}
				data = nil
			} else {
				lastReadError = ""
			}
			if !retry && readErr == nil && bytes.Equal(previous, data) {
				return nil
			}
			// Bypass equality after rejection so restoring previously accepted bytes
			// also clears the consumer's rejected observation.
			retry = true
			if err := notify(Change{Path: path, Contents: data}); err == nil && readErr == nil {
				previous, retry = data, false
			}
			// Keep event watchers running after rejection so polling can retry.
			return nil
		})
	}
}
