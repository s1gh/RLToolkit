package logging

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitCreatesLogsDir(t *testing.T) {
	dataDir := t.TempDir()
	var stderr bytes.Buffer
	w, closeFn := Init(dataDir, &stderr)
	defer closeFn()

	if w == nil {
		t.Fatal("Init returned nil writer")
	}

	logsDir := filepath.Join(dataDir, "logs")
	if _, err := os.Stat(logsDir); err != nil {
		t.Fatalf("logs dir not created: %v", err)
	}
}

func TestInitWritesToBothSinks(t *testing.T) {
	dataDir := t.TempDir()
	var stderr bytes.Buffer
	w, closeFn := Init(dataDir, &stderr)
	defer closeFn()

	const msg = "hello backend log\n"
	if _, err := io.WriteString(w, msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !strings.Contains(stderr.String(), "hello backend log") {
		t.Errorf("stderr did not receive message; got %q", stderr.String())
	}

	stamp := todayStamp()
	logPath := filepath.Join(dataDir, "logs", "backend-"+stamp+".log")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read backend log: %v", err)
	}
	if !strings.Contains(string(body), "hello backend log") {
		t.Errorf("file did not receive message; got %q", body)
	}
}

func TestInitDegradesGracefullyOnReadonlyDir(t *testing.T) {
	// Construct a path that can't be created (file under a file).
	parent := t.TempDir()
	blocker := filepath.Join(parent, "data")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	w, closeFn := Init(blocker, &stderr) // <blocker>/logs is invalid
	defer closeFn()

	if w == nil {
		t.Fatal("Init returned nil writer; expected fallback to extra")
	}
	// Should still be writable — degradation is silent on the writer side.
	if _, err := io.WriteString(w, "still works\n"); err != nil {
		t.Fatalf("write after degradation: %v", err)
	}
	if !strings.Contains(stderr.String(), "file logging disabled") {
		t.Errorf("expected degradation warning on stderr; got %q", stderr.String())
	}
}

func TestSweepOldRemovesAgedFiles(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "backend-2020-01-01.log")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate the mtime well past RetainDays.
	stale := time.Now().Add(-time.Duration(RetainDays+5) * 24 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	fresh := filepath.Join(dir, "backend-2999-01-01.log")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unrelated file should be untouched.
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	sweepOld(dir, &stderr)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("aged log not removed: err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh log was removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated file was removed: %v", err)
	}
}
