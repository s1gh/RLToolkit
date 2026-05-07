package datastore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_SetGetDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := s.Set("p1", "k", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get("p1", "k")
	if err != nil || !ok || string(got) != `{"v":1}` {
		t.Fatalf("Get: got %s ok=%v err=%v", got, ok, err)
	}

	if err := s.Delete("p1", "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get("p1", "k"); ok {
		t.Fatal("Get after Delete returned ok=true")
	}
}

func TestStore_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	s1, _ := New(dir)
	if err := s1.Set("p1", "x", json.RawMessage(`"hello"`)); err != nil {
		t.Fatal(err)
	}

	s2, _ := New(dir)
	got, ok, err := s2.Get("p1", "x")
	if err != nil || !ok || string(got) != `"hello"` {
		t.Fatalf("reopen: got %s ok=%v err=%v", got, ok, err)
	}
}

func TestStore_RejectsBadName(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.Set("../etc/passwd", "k", json.RawMessage(`1`)); err == nil {
		t.Fatal("expected error for invalid plugin name")
	}
}

func TestValidName(t *testing.T) {
	for _, in := range []string{"hello", "hello-world", "hello_world", "abc123"} {
		if !ValidName(in) {
			t.Errorf("ValidName(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "../oops", "a/b", "a b", "a.b"} {
		if ValidName(in) {
			t.Errorf("ValidName(%q) = true, want false", in)
		}
	}
}

// TestStore_AtomicWrite confirms the temp+rename strategy leaves no
// .tmp file on success.
func TestStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if err := s.Set("p", "k", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(matches) != 0 {
		t.Errorf("leftover .tmp files: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(dir, "p.json")); err != nil {
		t.Fatalf("expected p.json: %v", err)
	}
}
