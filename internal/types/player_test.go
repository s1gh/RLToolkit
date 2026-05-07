package types

import (
	"encoding/json"
	"testing"
)

// TestEnrichedPlayer_WireShape locks the JSON tags. Renaming or
// removing any field breaks every plugin that's already in the wild.
func TestEnrichedPlayer_WireShape(t *testing.T) {
	p := EnrichedPlayer{
		ID:       "Steam|1|0",
		Name:     "Ada",
		Team:     0,
		Platform: "Steam",
		IsBot:    false,
		IsMe:     true,
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"Steam|1|0","name":"Ada","team":0,"platform":"Steam","isBot":false,"isMe":true}`
	if string(out) != want {
		t.Fatalf("wire shape drift\n got: %s\nwant: %s", out, want)
	}
}

// TestEnrichedPlayer_OmitsIsMeWhenFalse confirms the omitempty tag —
// `isMe` is a per-subscriber stamp; sending false on every player
// would bloat traffic.
func TestEnrichedPlayer_OmitsIsMeWhenFalse(t *testing.T) {
	out, _ := json.Marshal(EnrichedPlayer{ID: "x", Name: "y", IsMe: false})
	if got := string(out); got != `{"id":"x","name":"y","team":0,"isBot":false}` {
		t.Errorf("isMe leaked when false: %s", got)
	}
}

// TestShortcutRef_DecodesNumericShortcut guards against the regression
// that decoding Shortcut as a string used to silently break every
// downstream synthetic event.
func TestShortcutRef_DecodesNumericShortcut(t *testing.T) {
	var ref ShortcutRef
	if err := json.Unmarshal([]byte(`{"Name":"Ada","Shortcut":3,"TeamNum":0}`), &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Name != "Ada" || ref.Shortcut != 3 || ref.TeamNum != 0 {
		t.Fatalf("decoded ref wrong: %+v", ref)
	}
}
