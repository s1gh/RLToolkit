package roster

import (
	"rl-toolkit/internal/types"
	"testing"
)

// stubIdentity is a tiny in-memory IdentityLookup used by resolver tests.
type stubIdentity struct{ pid string }

func (s stubIdentity) MyPrimaryID() string { return s.pid }

func TestResolver_StampsIsMeFromIdentity(t *testing.T) {
	tr := New()
	tr.AttachIdentity(stubIdentity{pid: "Steam|42|0"})
	tr.SetRoster([]Player{
		{ID: "Steam|1|0", Name: "Other", Team: 0},
		{ID: "Steam|42|0", Name: "Me", Team: 1},
	})

	got := tr.ResolveByShortcut(types.ShortcutRef{Name: "Me", TeamNum: 1})
	if got == nil || !got.IsMe {
		t.Fatalf("expected IsMe=true on identity match, got %+v", got)
	}
	other := tr.ResolveByShortcut(types.ShortcutRef{Name: "Other", TeamNum: 0})
	if other == nil || other.IsMe {
		t.Fatalf("expected IsMe=false on non-match, got %+v", other)
	}
}

func TestResolver_StampsIsMeFromPrimaryID(t *testing.T) {
	tr := New()
	tr.AttachIdentity(stubIdentity{pid: "Steam|42|0"})
	tr.SetRoster([]Player{{ID: "Steam|42|0", Name: "Me", Team: 1}})

	got := tr.ResolveByPrimaryId("Steam|42|0")
	if got == nil || !got.IsMe {
		t.Fatalf("expected IsMe=true on PrimaryID match, got %+v", got)
	}
}

func TestResolver_NoIdentityStoreLeavesIsMeFalse(t *testing.T) {
	tr := New()
	tr.SetRoster([]Player{{ID: "Steam|42|0", Name: "Me", Team: 1}})
	got := tr.ResolveByShortcut(types.ShortcutRef{Name: "Me", TeamNum: 1})
	if got == nil || got.IsMe {
		t.Fatalf("no identity attached should leave IsMe=false, got %+v", got)
	}
}
