package overrides

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func ptrStr(s string) *string  { return &s }
func ptrInt(n int) *int        { return &n }
func ptrFlt(f float64) *float64 { return &f }
func ptrBool(b bool) *bool     { return &b }

func TestStore_MergeAndPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := s.MergeOne("p1", Override{Anchor: ptrStr("top-left"), OffsetX: ptrInt(8)})
	if err != nil {
		t.Fatalf("MergeOne: %v", err)
	}
	if *merged.Anchor != "top-left" || *merged.OffsetX != 8 {
		t.Fatalf("merged: %+v", merged)
	}
	// Round-trip via a fresh Store to confirm persistence
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.GetAll()
	if got["p1"].Anchor == nil || *got["p1"].Anchor != "top-left" {
		t.Fatalf("after reload: %+v", got["p1"])
	}
}

func TestStore_PartialMergePreservesPreviousFields(t *testing.T) {
	s, _ := New(t.TempDir())
	if _, err := s.MergeOne("p1", Override{Anchor: ptrStr("top-right"), OffsetX: ptrInt(16)}); err != nil {
		t.Fatal(err)
	}
	merged, err := s.MergeOne("p1", Override{Opacity: ptrFlt(0.5)})
	if err != nil {
		t.Fatal(err)
	}
	if *merged.Anchor != "top-right" || *merged.OffsetX != 16 || *merged.Opacity != 0.5 {
		t.Fatalf("partial merge dropped earlier fields: %+v", merged)
	}
}

func TestOverride_ValidateRejectsBadValues(t *testing.T) {
	cases := []Override{
		{Anchor: ptrStr("middle")},
		{OffsetX: ptrInt(-1)},
		{Width: ptrInt(99999)},
		{Opacity: ptrFlt(2.0)},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected validation error for %+v", i, c)
		}
	}
}

func TestStore_QuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlay-overrides.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New on corrupt: %v", err)
	}
	if len(s.GetAll()) != 0 {
		t.Errorf("expected empty store, got %v", s.GetAll())
	}
	if _, err := os.Stat(path + ".broken"); err != nil {
		t.Errorf("corrupt file not quarantined: %v", err)
	}
}

func TestStore_DeleteIsIdempotent(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.Delete("absent"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	if _, err := s.MergeOne("p1", Override{Enabled: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("p1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetAll()["p1"]; ok {
		t.Error("entry still present after Delete")
	}
}

func TestStore_NotifyFires(t *testing.T) {
	s, _ := New(t.TempDir())
	called := 0
	s.Notify = func() { called++ }
	if _, err := s.MergeOne("p1", Override{Anchor: ptrStr("top-left")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("p1"); err != nil {
		t.Fatal(err)
	}
	if called != 2 {
		t.Errorf("Notify fired %d times, want 2", called)
	}
}

// Sanity: JSON tag spelling matches the production wire shape.
func TestOverride_JSONShape(t *testing.T) {
	o := Override{Anchor: ptrStr("top-left"), Enabled: ptrBool(false)}
	out, _ := json.Marshal(o)
	if string(out) != `{"anchor":"top-left","enabled":false}` {
		t.Fatalf("unexpected JSON: %s", out)
	}
}
