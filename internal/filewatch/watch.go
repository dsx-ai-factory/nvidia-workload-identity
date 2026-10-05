package filewatch

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sync/errgroup"
)

// Watcher delivers values synchronously. A nil callback error accepts a value.
type Watcher[T any] func(ctx context.Context, notify func(T) error) error

// Change holds bytes from one read. Nil contents invalidate an unavailable file.
// Consumers must not modify the contents.
type Change struct {
	Path     string
	Contents []byte
}

// FSNotify reports parent directory events. Events can be lost, so compose it
// with Polling to recover the file's current contents.
func FSNotify(path string) Watcher[struct{}] {
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

// Polling triggers periodic checks even when filesystem events are lost.
// Each check can retry rejected contents. Versions replaced between reads may
// never be observed.
func Polling(interval time.Duration) Watcher[struct{}] {
	return func(ctx context.Context, notify func(struct{}) error) error {
		if interval <= 0 {
			return fmt.Errorf("file polling interval must be positive")
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if err := notify(struct{}{}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := notify(struct{}{}); err != nil {
					return err
				}
			}
		}
	}
}

// Composite serializes callbacks. Each producer waits for its callback result
// before advancing and receives the consumer's error unchanged.
func Composite[T any](watchers ...Watcher[T]) Watcher[T] {
	return func(ctx context.Context, notify func(T) error) error {
		group, watchContext := errgroup.WithContext(ctx)
		var callbacks sync.Mutex
		for _, watcher := range watchers {
			group.Go(func() error {
				return watcher(watchContext, func(value T) error {
					callbacks.Lock()
					defer callbacks.Unlock()
					if err := watchContext.Err(); err != nil {
						return err
					}
					return notify(value)
				})
			})
		}
		return group.Wait()
	}
}

// Contents advances its comparison baseline only after consumer acceptance.
// Read failures and rejected changes retry on later events or polls, including
// unchanged contents. Delivery uses captured bytes even if the file changes
// while the consumer processes them.
func Contents(path string, events Watcher[struct{}]) Watcher[Change] {
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
