package filewatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestFSNotifyTracksOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched")
	writeFile(t, path, "first")
	changed := startWatcher(t, FSNotify(path))
	waitNotification(t, changed)

	writeFile(t, filepath.Join(filepath.Dir(path), "unrelated"), "other")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	assertNoNotification(t, changed)

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "second")
	waitNotification(t, changed)

	replacement := path + ".next"
	writeFile(t, replacement, "third")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	waitNotification(t, changed)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitNotification(t, changed)
	writeFile(t, path, "fourth")
	waitNotification(t, changed)
}

func TestFSNotifyTracksProjectedSecret(t *testing.T) {
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
	changed := startWatcher(t, FSNotify(path))
	waitNotification(t, changed)
	if err := os.Symlink("second", filepath.Join(dir, "..data_tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	waitNotification(t, changed)
	if err := os.RemoveAll(filepath.Join(dir, "first")); err != nil {
		t.Fatal(err)
	}
	assertNoNotification(t, changed)
}

func TestPollingUsesConfiguredIntervalAndDetectsSymlinkTargetChanges(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	writeFile(t, target, "first")
	path := filepath.Join(t.TempDir(), "watched")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		changed := startWatcher(t, Polling(path, 30*time.Second))
		waitNotification(t, changed)
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
		waitNotification(t, changed)
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
		waitNotification(t, changed)
		writeFile(t, target, "third")
		time.Sleep(30 * time.Second)
		synctest.Wait()
		waitNotification(t, changed)
	})
}

func TestCompositeCancelsOtherWatchersOnFailure(t *testing.T) {
	stopped := make(chan struct{})
	expected := errors.New("watch failed")
	composite := Composite(
		func(ctx context.Context, _ chan<- struct{}) error {
			<-ctx.Done()
			close(stopped)
			return nil
		},
		func(context.Context, chan<- struct{}) error { return expected },
	)
	if err := composite(t.Context(), make(chan struct{}, 1)); !errors.Is(err, expected) {
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
		if err := Polling("unused", interval)(t.Context(), make(chan struct{}, 1)); err == nil {
			t.Fatalf("accepted interval %s", interval)
		}
	}
}

func startWatcher(t *testing.T, watcher Watcher) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- watcher(ctx, changed) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
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

func waitNotification(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("file change notification timed out")
	}
}

func assertNoNotification(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	select {
	case <-changed:
		t.Fatal("watcher notified for unchanged contents")
	case <-time.After(100 * time.Millisecond):
	}
}
