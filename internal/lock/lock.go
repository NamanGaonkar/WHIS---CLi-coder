// Package lock guarantees ONE whis session per device (any folder). A second
// window — even in a different project — finds the lock, sees who holds it
// and where, and can TAKE OVER: a takeover request file politely tells the
// running session to exit, the old window flushes its session and closes
// automatically, and the new window opens in its own folder.
package lock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"whis/internal/config"
)

// Lock is an acquired device-wide session lock.
type Lock struct {
	path    string // <lockdir>/device.json
	notify  string // <lockdir>/device.takeover (watched by the holder)
	Folder  string
	Info    Info
	watched bool
}

// Info describes the current holder (written into the lock file).
type Info struct {
	PID     int    `json:"pid"`
	Host    string `json:"host"`
	User    string `json:"user"`
	Folder  string `json:"folder"`
	Started string `json:"started"`
}

// dirOverride lets tests isolate the lock directory (nil in production).
var dirOverride func() string

// dir is the shared lock directory (~/.whis/locks).
func dir() string {
	if dirOverride != nil {
		d := dirOverride()
		_ = os.MkdirAll(d, 0o755)
		return d
	}
	d := filepath.Join(config.Dir(), "locks")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// lockName is the fixed device-wide lock file name: whis allows only ONE
// session on the whole machine, regardless of folder.
func lockName() string { return "device" }

// Try acquires the device-wide lock. The folder argument is recorded in the
// lock info for display. When another whis already holds the lock the
// returned error is *HeldError carrying the holder's info for the takeover UI.
func Try(folder string) (*Lock, error) {
	abs, _ := filepath.Abs(folder)
	k := lockName()
	l := &Lock{
		path:   filepath.Join(dir(), k+".json"),
		notify: filepath.Join(dir(), k+".takeover"),
		Folder: abs,
	}
	// stale takeover request from a previous round must not kill the new holder
	_ = os.Remove(l.notify)

	if b, err := os.ReadFile(l.path); err == nil {
		var holder Info
		if json.Unmarshal(b, &holder) == nil && processAlive(holder.PID) {
			return nil, &HeldError{Info: holder, Path: l.path}
		}
		// dead holder: sweep the stale lock and continue
		_ = os.Remove(l.path)
	}
	// exclusive-ish create: O_EXCL races resolve by re-check below
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		// lost a race with a starting window: report it as held
		var holder Info
		if b, rerr := os.ReadFile(l.path); rerr == nil {
			_ = json.Unmarshal(b, &holder)
		}
		return nil, &HeldError{Info: holder, Path: l.path}
	}
	l.Info = Info{
		PID:     os.Getpid(),
		Host:    hostname(),
		User:    currentUser(),
		Folder:  abs,
		Started: time.Now().Format(time.RFC3339),
	}
	b, _ := json.MarshalIndent(l.Info, "", "  ")
	_, werr := f.Write(b)
	_ = f.Close()
	if werr != nil {
		_ = os.Remove(l.path)
		return nil, werr
	}
	return l, nil
}

// HeldError says who holds the lock.
type HeldError struct {
	Info Info
	Path string
}

func (e *HeldError) Error() string {
	who := fmt.Sprintf("pid %d", e.Info.PID)
	if e.Info.User != "" {
		who = fmt.Sprintf("%s (pid %d)", e.Info.User, e.Info.PID)
	}
	where := ""
	if e.Info.Folder != "" {
		where = fmt.Sprintf(" in %s", e.Info.Folder)
	}
	return fmt.Sprintf("whis is already running: %s%s, started %s", who, where, e.Info.Started)
}

// RequestTakeover asks the current holder (wherever it runs) to exit, waiting
// up to wait for it to release the lock; then acquires it for the caller.
func RequestTakeover(folder string, wait time.Duration) (*Lock, error) {
	k := lockName()
	notify := filepath.Join(dir(), k+".takeover")
	lpath := filepath.Join(dir(), k+".json")
	if err := os.WriteFile(notify, []byte("takeover"), 0o600); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lpath); os.IsNotExist(err) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	return Try(folder)
}

// WatchForTakeover polls the takeover request file (holder side). When a
// request appears the callback runs ONCE and watching stops. Non-blocking
// ticker goroutine; stop by closing the returned channel's parent Lock via
// Release, or let process exit clean it up.
func (l *Lock) WatchForTakeover(onTakeover func()) {
	if l == nil || l.watched || onTakeover == nil {
		return
	}
	l.watched = true
	go func() {
		t := time.NewTicker(700 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			if _, err := os.Stat(l.notify); err == nil {
				_ = os.Remove(l.notify)
				onTakeover()
				return
			}
			if _, err := os.Stat(l.path); os.IsNotExist(err) {
				return // our lock is gone; stop watching
			}
		}
	}()
}

// Release removes the lock file (idempotent).
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = os.Remove(l.path)
	_ = os.Remove(l.notify)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func currentUser() string {
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return ""
}

// sysSig0 is a benign signal used to probe process liveness (unix fallback;
// windows overrides processAlive entirely).
var sysSig0 = syscallSig0()
