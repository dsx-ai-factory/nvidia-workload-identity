package watch_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"dsx-identity/internal/watch"
)

func TestCompositeCancelsOtherWatchersOnFailure(t *testing.T) {
	stopped := make(chan struct{})
	expected := errors.New("watch failed")
	composite := watch.Composite(
		watch.Watcher[struct{}](func(ctx context.Context, _ func(struct{}) error) error { <-ctx.Done(); close(stopped); return nil }),
		watch.Watcher[struct{}](func(context.Context, func(struct{}) error) error { return expected }),
	)
	if err := composite(t.Context(), func(struct{}) error { return nil }); !errors.Is(err, expected) {
		t.Fatalf("composite error = %v, want %v", err, expected)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("composite left watcher running")
	}
}

func TestPollingRejectsNonpositiveInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		if err := watch.Polling(interval)(t.Context(), func(struct{}) error { return nil }); err == nil {
			t.Fatalf("accepted interval %s", interval)
		}
	}
}

func TestCompositeReturnsConsumerErrorsToProducer(t *testing.T) {
	rejected := errors.New("change rejected")
	watcher := watch.Watcher[int](func(_ context.Context, notify func(int) error) error {
		if err := notify(1); !errors.Is(err, rejected) {
			return fmt.Errorf("first reply = %v, want %v", err, rejected)
		}
		return notify(2)
	})
	var received []int
	err := watch.Composite(watcher)(t.Context(), func(value int) error {
		received = append(received, value)
		if value == 1 {
			return rejected
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0] != 1 || received[1] != 2 {
		t.Fatalf("received = %v", received)
	}
}

func TestCompositeDeliversRapidChangesWithBackpressure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		delivered := make(chan int, 2)
		received := make(chan int)
		accept := make(chan struct{})
		done := make(chan error, 1)
		watcher := watch.Watcher[int](func(_ context.Context, notify func(int) error) error {
			for _, value := range []int{1, 2} {
				if err := notify(value); err != nil {
					return err
				}
				delivered <- value
			}
			return nil
		})
		go func() {
			done <- watch.Composite(watcher)(t.Context(), func(value int) error {
				received <- value
				<-accept
				return nil
			})
		}()
		if value := <-received; value != 1 {
			t.Fatalf("first result = %d", value)
		}
		synctest.Wait()
		select {
		case <-delivered:
			t.Fatal("producer advanced before first acceptance")
		default:
		}
		accept <- struct{}{}
		if value := <-received; value != 2 {
			t.Fatalf("second result = %d", value)
		}
		synctest.Wait()
		if value := <-delivered; value != 1 {
			t.Fatalf("delivered = %d", value)
		}
		select {
		case <-delivered:
			t.Fatal("producer advanced before second acceptance")
		default:
		}
		accept <- struct{}{}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if value := <-delivered; value != 2 {
			t.Fatalf("delivered = %d", value)
		}
	})
}
