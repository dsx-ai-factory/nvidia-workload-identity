package filewatch

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher owns its resources until iteration stops or ctx ends.
type Watcher[T any] func(ctx context.Context) iter.Seq2[T, error]

// Change contains immutable bytes from one read. Consumers must not modify them.
type Change struct {
	Path     string
	Contents []byte
}

func FSNotify(path string) Watcher[struct{}] {
	return func(ctx context.Context) iter.Seq2[struct{}, error] {
		return func(yield func(struct{}, error) bool) {
			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				yield(struct{}{}, fmt.Errorf("create file watcher: %w", err))
				return
			}
			defer watcher.Close()
			// Parent watches survive atomic replacements and Kubernetes ..data swaps.
			if err := watcher.Add(filepath.Dir(path)); err != nil {
				yield(struct{}{}, fmt.Errorf("watch parent of %q: %w", path, err))
				return
			}
			if !yield(struct{}{}, nil) {
				return
			}
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-watcher.Events:
					if !ok {
						yield(struct{}{}, fmt.Errorf("file watcher for %q closed", path))
						return
					}
				case _, ok := <-watcher.Errors:
					if !ok {
						yield(struct{}{}, fmt.Errorf("file watcher for %q closed", path))
						return
					}
					// Removal and backend overflow errors require another content check.
				}
				if !yield(struct{}{}, nil) {
					return
				}
			}
		}
	}
}

func Polling(interval time.Duration) Watcher[struct{}] {
	return func(ctx context.Context) iter.Seq2[struct{}, error] {
		return func(yield func(struct{}, error) bool) {
			if interval <= 0 {
				yield(struct{}{}, fmt.Errorf("file polling interval must be positive"))
				return
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			if !yield(struct{}{}, nil) {
				return
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !yield(struct{}{}, nil) {
						return
					}
				}
			}
		}
	}
}

func Composite[T any](watchers ...Watcher[T]) Watcher[T] {
	return func(ctx context.Context) iter.Seq2[T, error] {
		return func(yield func(T, error) bool) {
			ctx, cancel := context.WithCancel(ctx)
			type result struct {
				value  T
				err    error
				resume chan struct{}
			}
			results := make(chan result)
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			for _, watcher := range watchers {
				workers.Go(func() {
					for value, err := range watcher(ctx) {
						resume := make(chan struct{})
						select {
						case <-ctx.Done():
							return
						case results <- result{value, err, resume}:
						}
						// Keep the producer's yield suspended until the consumer finishes.
						select {
						case <-ctx.Done():
							return
						case <-resume:
						}
					}
				})
			}
			go func() { workers.Wait(); close(results) }()
			for {
				select {
				case <-ctx.Done():
					return
				case result, ok := <-results:
					if !ok || !yield(result.value, result.err) {
						return
					}
					close(result.resume)
				}
			}
		}
	}
}

// Contents serializes reads and comparisons after composing event watchers.
// Read errors include the file path. Watch failures end with an empty Change.
func Contents(path string, events Watcher[struct{}]) Watcher[Change] {
	return func(ctx context.Context) iter.Seq2[Change, error] {
		return func(yield func(Change, error) bool) {
			var previous []byte
			var previousError string
			first := true
			for _, err := range events(ctx) {
				if err != nil {
					yield(Change{}, err)
					return
				}
				data, readErr := os.ReadFile(path)
				var errorText string
				if readErr != nil {
					errorText = readErr.Error()
				}
				if !first && previousError == errorText && bytes.Equal(previous, data) {
					continue
				}
				if !yield(Change{Path: path, Contents: data}, readErr) {
					return
				}
				// The comparison baseline advances only after delivery to the consumer.
				previous, previousError, first = data, errorText, false
			}
		}
	}
}
