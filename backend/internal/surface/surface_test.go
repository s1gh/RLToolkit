package surface

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew_AbsentFile(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.Get(); got != nil {
		t.Errorf("expected nil configured, got %+v", got)
	}
}

func TestSet_PersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(&Size{W: 2560, H: 1440}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get()
	if got == nil || got.W != 2560 || got.H != 1440 {
		t.Errorf("after reload: %+v", got)
	}
}

func TestSet_NilClears(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_ = s.Set(&Size{W: 1920, H: 1080})
	if err := s.Set(nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got != nil {
		t.Errorf("after clear: %+v", got)
	}
	// File should be removed (or at least not yield a non-nil Get on reload).
	s2, _ := New(dir)
	if got := s2.Get(); got != nil {
		t.Errorf("after reload of cleared store: %+v", got)
	}
}

func TestSet_RejectsBadValues(t *testing.T) {
	s, _ := New(t.TempDir())
	cases := []Size{{W: 0, H: 1080}, {W: 1920, H: 0}, {W: -1, H: 1080}, {W: 16385, H: 1080}, {W: 1920, H: 16385}}
	for i, c := range cases {
		if err := s.Set(&c); err == nil {
			t.Errorf("case %d: expected validation error for %+v", i, c)
		}
	}
}

func TestSetGetDetected(t *testing.T) {
	s, _ := New(t.TempDir())
	if got := s.Detected(); got != nil {
		t.Errorf("expected nil detected initially, got %+v", got)
	}
	if err := s.SetDetected(&Size{W: 3840, H: 2160}); err != nil {
		t.Fatal(err)
	}
	got := s.Detected()
	if got == nil || got.W != 3840 || got.H != 2160 {
		t.Errorf("Detected: %+v", got)
	}
	// Detected is in-memory: a new Store has no detected value.
	dir := t.TempDir()
	s1, _ := New(dir)
	_ = s1.SetDetected(&Size{W: 1920, H: 1080})
	s2, _ := New(dir)
	if got := s2.Detected(); got != nil {
		t.Errorf("expected detected to reset on reload, got %+v", got)
	}
}

func TestEffective_PriorityOrder(t *testing.T) {
	s, _ := New(t.TempDir())
	// Fallback when neither set.
	eff := s.Effective()
	if eff.W != 1920 || eff.H != 1080 {
		t.Errorf("fallback: %+v", eff)
	}
	// Detected only.
	_ = s.SetDetected(&Size{W: 2560, H: 1440})
	eff = s.Effective()
	if eff.W != 2560 || eff.H != 1440 {
		t.Errorf("detected: %+v", eff)
	}
	// Configured wins over detected.
	_ = s.Set(&Size{W: 1280, H: 720})
	eff = s.Effective()
	if eff.W != 1280 || eff.H != 720 {
		t.Errorf("configured wins: %+v", eff)
	}
}

func TestNotifyFires(t *testing.T) {
	s, _ := New(t.TempDir())
	called := 0
	s.Notify = func() { called++ }
	_ = s.Set(&Size{W: 1920, H: 1080})
	_ = s.Set(nil)
	// SetDetected does NOT fire Notify (see surface.go comment).
	_ = s.SetDetected(&Size{W: 2560, H: 1440})
	if called != 2 {
		t.Errorf("Notify fired %d times, want 2", called)
	}
}

func TestNew_QuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay-surface.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New on corrupt: %v", err)
	}
	if got := s.Get(); got != nil {
		t.Errorf("expected nil configured after quarantine, got %+v", got)
	}
	if _, err := os.Stat(path + ".broken"); err != nil {
		t.Errorf("corrupt file not quarantined: %v", err)
	}
}
