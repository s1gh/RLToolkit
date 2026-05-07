package backend

import (
	"rl-toolkit/internal/bus"
	"testing"
)

func TestRoster_StampsIsMeFromIdentity(t *testing.T) {
	store, err := NewIdentityStore(t.TempDir())
	if err != nil {
		t.Fatalf("identity store: %v", err)
	}
	if err := store.Set(Identity{PrimaryID: "Steam|42|0", Name: "Me"}); err != nil {
		t.Fatal(err)
	}

	roster := NewRosterTracker(bus.NewBus())
	roster.AttachIdentity(store)
	// Inject a roster snapshot directly so the resolver has someone
	// to find.
	roster.lastRoster = []rosterPlayer{
		{ID: "Steam|1|0", Name: "Other", Team: 0},
		{ID: "Steam|42|0", Name: "Me", Team: 1},
	}

	got := roster.ResolveByShortcut(ShortcutRef{Name: "Me", TeamNum: 1})
	if got == nil || !got.IsMe {
		t.Fatalf("expected IsMe=true on identity match, got %+v", got)
	}
	other := roster.ResolveByShortcut(ShortcutRef{Name: "Other", TeamNum: 0})
	if other == nil || other.IsMe {
		t.Fatalf("expected IsMe=false on non-match, got %+v", other)
	}
}

func TestRoster_StampsIsMeFromPrimaryID(t *testing.T) {
	store, err := NewIdentityStore(t.TempDir())
	if err != nil {
		t.Fatalf("identity store: %v", err)
	}
	_ = store.Set(Identity{PrimaryID: "Steam|42|0", Name: "Me"})

	roster := NewRosterTracker(bus.NewBus())
	roster.AttachIdentity(store)
	roster.lastRoster = []rosterPlayer{{ID: "Steam|42|0", Name: "Me", Team: 1}}

	got := roster.ResolveByPrimaryId("Steam|42|0")
	if got == nil || !got.IsMe {
		t.Fatalf("expected IsMe=true on PrimaryID match, got %+v", got)
	}
}

func TestRoster_NoIdentityStoreLeavesIsMeFalse(t *testing.T) {
	roster := NewRosterTracker(bus.NewBus())
	roster.lastRoster = []rosterPlayer{{ID: "Steam|42|0", Name: "Me", Team: 1}}
	got := roster.ResolveByShortcut(ShortcutRef{Name: "Me", TeamNum: 1})
	if got == nil || got.IsMe {
		t.Fatalf("no identity attached should leave IsMe=false, got %+v", got)
	}
}
