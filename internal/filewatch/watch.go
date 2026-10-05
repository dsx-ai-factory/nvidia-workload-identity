package filewatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sync/errgroup"
)

// Watcher reports changes to one file. It owns its resources until ctx ends.
type Watcher func(ctx context.Context, changed chan<- struct{}) error

func FSNotify(path string) Watcher {
	return func(ctx context.Context, changed chan<- struct{}) error {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return fmt.Errorf("create file watcher: %w", err)
		}
		defer watcher.Close()
		// Watch the parent so atomic replacements and Kubernetes ..data swaps
		// keep the watch attached. Check contents to ignore unrelated files.
		if err := watcher.Add(filepath.Dir(path)); err != nil {
			return fmt.Errorf("watch parent of %q: %w", path, err)
		}
		previous := readContents(path)
		// Read again through the consumer after the watch is installed so changes
		// between its initial load and watcher startup cannot be missed.
		notify(changed)
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
				// Backend errors can follow removal or lost events. Recheck
				// contents while polling continues to cover missed changes.
			}
			next := readContents(path)
			if !previous.equal(next) {
				previous = next
				notify(changed)
			}
		}
	}
}

func Polling(path string, interval time.Duration) Watcher {
	return func(ctx context.Context, changed chan<- struct{}) error {
		if interval <= 0 {
			return fmt.Errorf("file polling interval must be positive")
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		previous := readContents(path)
		notify(changed)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				next := readContents(path)
				if !previous.equal(next) {
					previous = next
					notify(changed)
				}
			}
		}
	}
}

func Composite(watchers ...Watcher) Watcher {
	return func(ctx context.Context, changed chan<- struct{}) error {
		group, watchContext := errgroup.WithContext(ctx)
		for _, watcher := range watchers {
			group.Go(func() error { return watcher(watchContext, changed) })
		}
		return group.Wait()
	}
}

type fileContents struct {
	data     []byte
	readable bool
}

func readContents(path string) fileContents {
	data, err := os.ReadFile(path)
	return fileContents{data: data, readable: err == nil}
}

func (c fileContents) equal(other fileContents) bool {
	return c.readable == other.readable && bytes.Equal(c.data, other.data)
}

func notify(changed chan<- struct{}) {
	select {
	case changed <- struct{}{}:
	default:
	}
}
