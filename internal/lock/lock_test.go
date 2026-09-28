package lock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTryHoldRelease(t *testing.T) {
	folder := t.TempDir()

	l, err := Try(folder)
	if err != nil {
		t.Fatalf("first Try failed: %v", err)
	}
	if l.Info.PID != os.Getpid() {
		t.Fatalf("lock info pid = %d, want %d", l.Info.PID, os.Getpid())
	}
	if _, err := os.Stat(l.path); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}

	// second window in the same folder must be told who holds it
	l2, err := Try(folder)
	if err == nil {
		l2.Release()
		t.Fatal("second Try should have been rejected")
	}
	he, ok := err.(*HeldError)
	if !ok {
		t.Fatalf("expected *HeldError, got %T: %v", err, err)
	}
	if he.Info.PID != os.Getpid() {
		t.Fatalf("holder pid = %d, want %d", he.Info.PID, os.Getpid())
	}
	if he.Error() == "" {
		t.Fatal("HeldError message is empty")
	}

	// a different folder must never be blocked
	l3, err := Try(filepath.Join(folder, "other"))
	if err != nil {
		t.Fatalf("different folder blocked: %v", err)
	}
	l3.Release()

	l.Release()
	if _, err := os.Stat(l.path); !os.IsNotExist(err) {
		t.Fatal("Release did not remove the lock file")
	}

	// and now the folder is free again
	l4, err := Try(folder)
	if err != nil {
		t.Fatalf("Try after release failed: %v", err)
	}
	l4.Release()
}

func TestStaleLockSwept(t *testing.T) {
	folder := t.TempDir()
	l0, err := Try(folder)
	if err != nil {
		t.Fatal(err)
	}

	// kill the holder: a dead pid must not block the next window
	ghost := []byte(`{"pid": 2147483000, "user": "ghost", "folder": "x", "started": "2020-01-01T00:00:00Z"}`)
	if err := os.WriteFile(l0.path, ghost, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Try(folder)
	if err != nil {
		t.Fatalf("stale lock was not swept: %v", err)
	}
	l.Release()
	l0.Release()
}

func TestTakeoverRoundtrip(t *testing.T) {
	folder := t.TempDir()
	holder, err := Try(folder)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan struct{}, 1)
	holder.WatchForTakeover(func() { got <- struct{}{} })

	// second window requests takeover with a short wait; the holder must
	// notice the request file, release, and let the new window in.
	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := RequestTakeover(folder, 5*time.Second); err != nil {
			t.Errorf("takeover failed: %v", err)
		}
	}()

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("holder never saw the takeover request")
	}
	holder.Release()

	// the request file must be gone so the NEW holder doesn't insta-quit
	if _, err := os.Stat(holder.notify); !os.IsNotExist(err) {
		t.Fatal("takeover request file still present after handling")
	}
}
