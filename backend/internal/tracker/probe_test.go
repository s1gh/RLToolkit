//go:build probe

package tracker

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestProbeLiveLookup hits real tracker.gg with the real surf doer.
// Run with: go test ./backend/internal/tracker/ -tags probe -run TestProbeLiveLookup -v
// Diagnostic-only; not part of CI.
func TestProbeLiveLookup(t *testing.T) {
	c, err := New(Options{HTTPTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := c.Lookup(ctx, "steam", "76561197964340095")
	if err != nil {
		t.Fatalf("Lookup error (type %T): %v", err, err)
	}
	t.Logf("platform=%s playerId=%s cached=%v age=%d", res.Platform, res.PlayerID, res.Cached, res.Age)
	for k, v := range res.Playlists {
		t.Log(fmt.Sprintf("  %-8s mmr=%d %s %s", k, v.MMR, v.Tier, v.Division))
	}
}
