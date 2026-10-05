package watch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Watcher delivers values synchronously. A nil callback error accepts a value.
type Watcher[T any] func(ctx context.Context, notify func(T) error) error

// Polling triggers periodic checks even when filesystem events are lost.
// Each check can retry rejected contents. Versions replaced between reads may
// never be observed.
func Polling(interval time.Duration) Watcher[struct{}] {
	return func(ctx context.Context, notify func(struct{}) error) error {
		if interval <= 0 {
			return fmt.Errorf("polling interval must be positive")
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
