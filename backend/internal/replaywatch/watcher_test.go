package replaywatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rl-toolkit/backend/internal/bus"
)

// recordingBus captures every Broadcast call. Safe for concurrent use.
type recordingBus struct {
	mu     sync.Mutex
	events []bus.Event
}

func (r *recordingBus) Broadcast(evt bus.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
}

func (r *recordingBus) snapshot() []bus.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]bus.Event, len(r.events))
	copy(out, r.events)
	return out
}

// waitForEvent polls the recording bus until name appears or timeout.
// Returns the event, or fatals.
func waitForEvent(t *testing.T, rb *recordingBus, name string, timeout time.Duration) bus.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range rb.snapshot() {
			if e.Name == name {
				return e
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; events seen: %+v", name, rb.snapshot())
	return bus.Event{}
}

// assertNoEvent fails if name appears within timeout.
func assertNoEvent(t *testing.T, rb *recordingBus, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range rb.snapshot() {
			if e.Name == name {
				t.Fatalf("unexpected %s: %+v", name, e)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stableDelayForTests is short enough that tests run fast but long
// enough that a two-stage write completes inside one window.
const stableDelayForTests = 50 * time.Millisecond

// startWatcher boots a Watcher pointed at dir with a fresh bus.
// Caller must defer cancel().
func startWatcher(t *testing.T, dir string) (*Watcher, *recordingBus, context.CancelFunc) {
	t.Helper()
	rb := &recordingBus{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(dir); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(store, rb, WatcherOptions{StableDelay: stableDelayForTests})
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	// Give Run a moment to install fsnotify.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if w.State().Status == "active" {
			return w, rb, cancel
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	t.Fatalf("watcher never went active; state=%+v", w.State())
	return nil, nil, nil
}

func TestWatcher_FiresOnceForChunkedWrite(t *testing.T) {
	dir := t.TempDir()
	_, rb, cancel := startWatcher(t, dir)
	defer cancel()

	path := filepath.Join(dir, "ABCDEF1234.replay")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	// Second chunk inside the stable window — must NOT cause a second event.
	time.Sleep(stableDelayForTests / 2)
	if _, err := f.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	evt := waitForEvent(t, rb, "_SavedReplay", 2*time.Second)
	var payload map[string]any
	if err := json.Unmarshal(evt.Data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got := payload["matchGuid"]; got != "ABCDEF1234" {
		t.Errorf("matchGuid = %v, want ABCDEF1234", got)
	}
	if got := payload["fileName"]; got != "ABCDEF1234.replay" {
		t.Errorf("fileName = %v", got)
	}
	if got, _ := payload["sizeBytes"].(float64); got != float64(len("firstsecond")) {
		t.Errorf("sizeBytes = %v, want %d", got, len("firstsecond"))
	}
	if !strings.HasSuffix(payload["path"].(string), "ABCDEF1234.replay") {
		t.Errorf("path = %v", payload["path"])
	}
	// Wait extra long to confirm only one event fires.
	time.Sleep(2 * stableDelayForTests)
	count := 0
	for _, e := range rb.snapshot() {
		if e.Name == "_SavedReplay" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("got %d _SavedReplay events, want 1", count)
	}
}

func TestWatcher_IgnoresNonReplayFiles(t *testing.T) {
	dir := t.TempDir()
	_, rb, cancel := startWatcher(t, dir)
	defer cancel()

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertNoEvent(t, rb, "_SavedReplay", 2*stableDelayForTests)
}

func TestWatcher_SkipsPreexistingFiles(t *testing.T) {
	dir := t.TempDir()
	// Pre-create before the watcher starts.
	if err := os.WriteFile(filepath.Join(dir, "OLD.replay"), []byte("xxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rb, cancel := startWatcher(t, dir)
	defer cancel()
	assertNoEvent(t, rb, "_SavedReplay", 2*stableDelayForTests)
}

func TestWatcher_StateMissingDir(t *testing.T) {
	rb := &recordingBus{}
	store, _ := NewStore(t.TempDir())
	_ = store.Set("/nonexistent/replay/dir")
	w := NewWatcher(store, rb, WatcherOptions{StableDelay: stableDelayForTests})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	// Allow Run to attempt and fail.
	time.Sleep(50 * time.Millisecond)
	st := w.State()
	if st.Status != "inactive" {
		t.Errorf("Status = %q, want inactive", st.Status)
	}
	if st.StatusReason == "" {
		t.Errorf("expected non-empty StatusReason for missing dir")
	}
}
