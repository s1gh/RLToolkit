package identity

import (
	"sync"
	"testing"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := s.Get(); got != nil {
		t.Errorf("expected nil at init, got %+v", got)
	}
	if err := s.Set(Identity{PrimaryID: "Steam|1|0", Name: "Tester"}); err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if got == nil || got.PrimaryID != "Steam|1|0" {
		t.Fatalf("got %+v, want PrimaryID=Steam|1|0", got)
	}

	// New store on the same dir reads from disk.
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got = s2.Get()
	if got == nil || got.Name != "Tester" {
		t.Fatalf("persistence: got %+v", got)
	}

	if err := s2.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get(); got != nil {
		t.Errorf("after clear: got %+v", got)
	}
}

func TestStore_RejectsEmptyPrimaryID(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Set(Identity{Name: "no id"}); err == nil {
		t.Fatal("expected error on empty primaryId")
	}
}

func TestStore_NotifyFires(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	var (
		mu       sync.Mutex
		received []*Identity
	)
	s.Notify = func(id *Identity) {
		mu.Lock()
		defer mu.Unlock()
		if id == nil {
			received = append(received, nil)
			return
		}
		cp := *id
		received = append(received, &cp)
	}

	if err := s.Set(Identity{PrimaryID: "Steam|7|0", Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 notify calls (Set + Clear), got %d", len(received))
	}
	if received[0] == nil || received[0].PrimaryID != "Steam|7|0" {
		t.Fatalf("first notify wrong: %+v", received[0])
	}
	if received[1] != nil {
		t.Fatalf("second notify expected nil, got %+v", received[1])
	}
}
