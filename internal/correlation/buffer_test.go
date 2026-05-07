package correlation

import "testing"

func TestBuffer_RecordAndFindWithin(t *testing.T) {
	b := New(8)
	b.Record("Goal", 1)
	b.Record("Save", 2)
	b.Record("Goal", 3)

	got := b.FindWithin("Goal", 5, func(p interface{}) bool { return p.(int) == 1 })
	if got != 1 {
		t.Errorf("FindWithin: got %v, want 1", got)
	}
	if hit := b.FindWithin("Demo", 5, func(p interface{}) bool { return true }); hit != nil {
		t.Errorf("FindWithin missing name: got %v, want nil", hit)
	}
}

func TestBuffer_EvictsOldestAtCapacity(t *testing.T) {
	b := New(2)
	b.Record("A", 1)
	b.Record("A", 2)
	b.Record("A", 3)
	got := b.Recent("A", 5)
	if len(got) != 2 || got[0] != 3 || got[1] != 2 {
		t.Errorf("Recent after eviction: got %v, want [3 2]", got)
	}
}

func TestBuffer_RemoveByName(t *testing.T) {
	b := New(8)
	b.Record("X", 1)
	b.Record("X", 2)
	b.Record("X", 3)
	b.RemoveByName("X", func(p interface{}) bool { return p.(int) == 2 })
	got := b.Recent("X", 5)
	if len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Errorf("after RemoveByName: got %v, want [3 1]", got)
	}
}

func TestBuffer_FindWithinHonorsLookback(t *testing.T) {
	b := New(8)
	b.Record("X", 1)
	b.Record("Y", 2)
	b.Record("Y", 3)
	// Lookback of 1 only inspects the newest entry; "X" target shouldn't be reached.
	if hit := b.FindWithin("X", 1, func(p interface{}) bool { return true }); hit != nil {
		t.Errorf("lookback=1: got %v, want nil", hit)
	}
}
