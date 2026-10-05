package filewatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestCompositeTracksOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched")
	writeFile(t, path, "first")
	changed := startWatcher(t, Contents(path, Composite(FSNotify(path), Polling(30*time.Millisecond))))
	waitContents(t, changed, "first")

	writeFile(t, filepath.Join(filepath.Dir(path), "unrelated"), "other")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	assertNoNotification(t, changed)

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "second")
	waitContents(t, changed, "second")

	replacement := path + ".next"
	writeFile(t, replacement, "third")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	waitContents(t, changed, "third")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitContents(t, changed, "")
	writeFile(t, path, "fourth")
	waitContents(t, changed, "fourth")
}

func TestCompositeTracksProjectedSecret(t *testing.T) {
	dir := t.TempDir()
	for _, generation := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(dir, generation), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, generation, "keyset.json"), generation)
	}
	if err := os.Symlink("first", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keyset.json")
	if err := os.Symlink("..data/keyset.json", path); err != nil {
		t.Fatal(err)
	}
	changed := startWatcher(t, Contents(path, Composite(FSNotify(path), Polling(30*time.Millisecond))))
	waitContents(t, changed, "first")
	if err := os.Symlink("second", filepath.Join(dir, "..data_tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	waitContents(t, changed, "second")
	if err := os.RemoveAll(filepath.Join(dir, "first")); err != nil {
		t.Fatal(err)
	}
	assertNoNotification(t, changed)
}

func TestPollingRecoversMissedEventsAtConfiguredInterval(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	writeFile(t, target, "first")
	path := filepath.Join(t.TempDir(), "watched")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		silent := Watcher[struct{}](func(ctx context.Context, _ func(struct{}) error) error { <-ctx.Done(); return nil })
		changed := startWatcher(t, Contents(path, Composite(silent, Polling(30*time.Second))))
		waitContents(t, changed, "first")
		synctest.Wait()
		writeFile(t, target, "second")
		time.Sleep(29 * time.Second)
		synctest.Wait()
		select {
		case <-changed:
			t.Fatal("poller notified before configured interval")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		waitContents(t, changed, "second")
		writeFile(t, target, "second")
		time.Sleep(30 * time.Second)
		synctest.Wait()
		select {
		case <-changed:
			t.Fatal("poller notified for unchanged contents")
		default:
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		waitContents(t, changed, "")
		writeFile(t, target, "third")
		time.Sleep(30 * time.Second)
		synctest.Wait()
		waitContents(t, changed, "third")
	})
}

func TestCompositeCancelsOtherWatchersOnFailure(t *testing.T) {
	stopped := make(chan struct{})
	expected := errors.New("watch failed")
	composite := Composite(
		Watcher[struct{}](func(ctx context.Context, _ func(struct{}) error) error { <-ctx.Done(); close(stopped); return nil }),
		Watcher[struct{}](func(context.Context, func(struct{}) error) error { return expected }),
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
		if err := Polling(interval)(t.Context(), func(struct{}) error { return nil }); err == nil {
			t.Fatalf("accepted interval %s", interval)
		}
	}
}

func TestCompositeReturnsConsumerErrorsToProducer(t *testing.T) {
	rejected := errors.New("change rejected")
	watcher := Watcher[int](func(_ context.Context, notify func(int) error) error {
		if err := notify(1); !errors.Is(err, rejected) {
			return fmt.Errorf("first reply = %v, want %v", err, rejected)
		}
		return notify(2)
	})
	var received []int
	err := Composite(watcher)(t.Context(), func(value int) error {
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
		watcher := Watcher[int](func(_ context.Context, notify func(int) error) error {
			for _, value := range []int{1, 2} {
				if err := notify(value); err != nil {
					return err
				}
				delivered <- value
			}
			return nil
		})
		go func() {
			done <- Composite(watcher)(t.Context(), func(value int) error {
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

func TestContentsRetriesRejectedChangesAndRestoredAcceptedContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched")
	writeFile(t, path, "first")
	events := Watcher[struct{}](func(_ context.Context, notify func(struct{}) error) error {
		if err := notify(struct{}{}); err != nil {
			return err
		}
		writeFile(t, path, "rejected")
		if err := notify(struct{}{}); err != nil {
			return err
		}
		if err := notify(struct{}{}); err != nil {
			return err
		}
		// Restoring accepted bytes must clear the consumer's rejected observation.
		writeFile(t, path, "first")
		if err := notify(struct{}{}); err != nil {
			return err
		}
		return notify(struct{}{})
	})
	var received []string
	err := Contents(path, events)(t.Context(), func(change Change) error {
		received = append(received, string(change.Contents))
		if string(change.Contents) == "rejected" {
			return errors.New("invalid content")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 4 || received[0] != "first" || received[1] != "rejected" || received[2] != "rejected" || received[3] != "first" {
		t.Fatalf("observations = %v", received)
	}
}

func TestContentsDeliversObservedBytesAcrossTransientChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched")
	writeFile(t, path, "first")
	events := Watcher[struct{}](func(_ context.Context, notify func(struct{}) error) error {
		if err := notify(struct{}{}); err != nil {
			return err
		}
		writeFile(t, path, "second")
		if err := notify(struct{}{}); err != nil {
			return err
		}
		return notify(struct{}{})
	})
	var received []string
	err := Contents(path, events)(t.Context(), func(change Change) error {
		received = append(received, string(change.Contents))
		writeFile(t, path, "transient")
		if string(change.Contents) != received[len(received)-1] {
			t.Fatal("observation changed with file contents")
		}
		writeFile(t, path, received[len(received)-1])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0] != "first" || received[1] != "second" {
		t.Fatalf("observations = %v", received)
	}
}

func startWatcher(t *testing.T, watcher Watcher[Change]) <-chan Change {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	changed := make(chan Change)
	done := make(chan error, 1)
	go func() {
		done <- watcher(ctx, func(change Change) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case changed <- change:
				return nil
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("watcher: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	return changed
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoNotification(t *testing.T, changed <-chan Change) {
	t.Helper()
	select {
	case <-changed:
		t.Fatal("watcher notified for unchanged contents")
	case <-time.After(100 * time.Millisecond):
	}
}

func waitContents(t *testing.T, changed <-chan Change, want string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case change := <-changed:
			if want == "" && change.Contents == nil {
				return
			}
			if want != "" && string(change.Contents) == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("file contents %q not observed", want)
		}
	}
}
