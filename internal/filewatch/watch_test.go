package filewatch

import (
	"context"
	"errors"
	"iter"
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
		silent := Watcher[struct{}](func(ctx context.Context) iter.Seq2[struct{}, error] {
			return func(func(struct{}, error) bool) { <-ctx.Done() }
		})
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
		Watcher[struct{}](func(ctx context.Context) iter.Seq2[struct{}, error] {
			return func(func(struct{}, error) bool) { <-ctx.Done(); close(stopped) }
		}),
		Watcher[struct{}](func(context.Context) iter.Seq2[struct{}, error] {
			return func(yield func(struct{}, error) bool) { yield(struct{}{}, expected) }
		}),
	)
	for _, err := range composite(t.Context()) {
		if !errors.Is(err, expected) {
			t.Fatalf("composite error = %v, want %v", err, expected)
		}
		break
	}
	select {
	case <-stopped:
	default:
		t.Fatal("composite left watcher running")
	}
}

func TestPollingRejectsNonpositiveInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		for _, err := range Polling(interval)(t.Context()) {
			if err == nil {
				t.Fatalf("accepted interval %s", interval)
			}
		}
	}
}

func TestCompositeDeliversRapidChangesWithBackpressure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		delivered := make(chan int, 2)
		watcher := Watcher[int](func(context.Context) iter.Seq2[int, error] {
			return func(yield func(int, error) bool) {
				for _, value := range []int{1, 2} {
					if !yield(value, nil) {
						return
					}
					delivered <- value
				}
			}
		})
		next, stop := iter.Pull2(Composite(watcher)(t.Context()))
		defer stop()
		value, err, ok := next()
		if !ok || err != nil || value != 1 {
			t.Fatalf("first result = %v, %v, %v", value, err, ok)
		}
		synctest.Wait()
		select {
		case <-delivered:
			t.Fatal("watcher advanced before consumer accepted first change")
		default:
		}
		value, err, ok = next()
		if !ok || err != nil || value != 2 {
			t.Fatalf("second result = %v, %v, %v", value, err, ok)
		}
		synctest.Wait()
		if value := <-delivered; value != 1 {
			t.Fatalf("delivered = %d", value)
		}
		select {
		case <-delivered:
			t.Fatal("watcher advanced before consumer accepted second change")
		default:
		}
		if _, _, ok := next(); ok {
			t.Fatal("unexpected third result")
		}
	})
}

func TestContentsDeliversObservedBytesAcrossTransientChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched")
	writeFile(t, path, "first")
	events := Watcher[struct{}](func(context.Context) iter.Seq2[struct{}, error] {
		return func(yield func(struct{}, error) bool) {
			if !yield(struct{}{}, nil) {
				return
			}
			writeFile(t, path, "second")
			if !yield(struct{}{}, nil) {
				return
			}
			// The consumer temporarily changed the file while handling this observation.
			if !yield(struct{}{}, nil) {
				return
			}
		}
	})
	var received []string
	for change, err := range Contents(path, events)(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		received = append(received, string(change.Contents))
		writeFile(t, path, "transient")
		if string(change.Contents) != received[len(received)-1] {
			t.Fatal("observation changed with file contents")
		}
		writeFile(t, path, received[len(received)-1])
	}
	if len(received) != 2 || received[0] != "first" || received[1] != "second" {
		t.Fatalf("observations = %v", received)
	}
}

type observation struct {
	change Change
	err    error
}

func startWatcher(t *testing.T, watcher Watcher[Change]) <-chan observation {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	changed := make(chan observation)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for change, err := range watcher(ctx) {
			select {
			case <-ctx.Done():
				return
			case changed <- observation{change, err}:
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
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

func assertNoNotification(t *testing.T, changed <-chan observation) {
	t.Helper()
	select {
	case <-changed:
		t.Fatal("watcher notified for unchanged contents")
	case <-time.After(100 * time.Millisecond):
	}
}

func waitContents(t *testing.T, changed <-chan observation, want string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case observed := <-changed:
			if want == "" && errors.Is(observed.err, os.ErrNotExist) {
				return
			}
			if want != "" && observed.err == nil && string(observed.change.Contents) == want {
				return
			}
			t.Logf("intermediate observation: %q, %v", observed.change.Contents, observed.err)
		case <-deadline.C:
			t.Fatalf("file contents %q not observed", want)
		}
	}
}
