package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureSource_ReadsJSONL(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "events.jsonl")
	body := `{"Event":"MatchCreated","Data":""}
{"Event":"CountdownBegin","Data":""}
{"Event":"RoundStarted","Data":""}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	src := &FixtureSource{Path: path}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := src.Events(ctx)

	var names []string
	for evt := range ch {
		names = append(names, evt.Name)
		if evt.Raw == nil {
			t.Errorf("event %q has nil Raw", evt.Name)
		}
	}
	want := []string{"MatchCreated", "CountdownBegin", "RoundStarted"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("event[%d] = %q, want %q", i, names[i], n)
		}
	}
}
