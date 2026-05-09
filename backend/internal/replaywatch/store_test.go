package replaywatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreNew_AbsentFile(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := s.Get(); got != "" {
		t.Errorf("expected empty configured, got %q", got)
	}
}

func TestStoreSet_PersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("/some/path"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get(); got != "/some/path" {
		t.Errorf("after reload: %q", got)
	}
}

func TestStoreSet_EmptyClears(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	_ = s.Set("/x")
	if err := s.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got != "" {
		t.Errorf("after clear: %q", got)
	}
	s2, _ := NewStore(dir)
	if got := s2.Get(); got != "" {
		t.Errorf("after reload of cleared store: %q", got)
	}
}

func TestStoreSet_RejectsNULBytes(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if err := s.Set("/has\x00nul"); err == nil {
		t.Errorf("expected error for NUL byte")
	}
}

func TestStoreNotifyFires(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	called := 0
	s.Notify = func() { called++ }
	_ = s.Set("/a")
	_ = s.Set("")
	if called != 2 {
		t.Errorf("Notify fired %d times, want 2", called)
	}
}

func TestStoreNew_QuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replay-watcher.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore on corrupt: %v", err)
	}
	if got := s.Get(); got != "" {
		t.Errorf("expected empty after quarantine, got %q", got)
	}
	if _, err := os.Stat(path + ".broken"); err != nil {
		t.Errorf("corrupt file not quarantined: %v", err)
	}
}
