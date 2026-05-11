//go:build probe

package tracker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestProbeEpic hits real tracker.gg with the surf doer to test
// whether Epic Games lookups work. Run with:
//
//	EPIC_NAME=SomeDisplayName go test ./backend/internal/tracker/ -tags probe -run TestProbeEpic -v
//
// Diagnostic-only; not part of CI.
func TestProbeEpic(t *testing.T) {
	name := os.Getenv("EPIC_NAME")
	if name == "" {
		t.Skip("set EPIC_NAME=<display name> to run this probe")
	}

	d := newSurfDoer(20 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://api.tracker.gg/api/v2/rocket-league/standard/profile/epic/%s", name)
	status, body, err := d.Do(ctx, url)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	t.Logf("status=%d", status)
	if len(body) < 800 {
		t.Logf("body: %s", string(body))
	} else {
		t.Logf("body[:800]: %s", string(body[:800]))
	}
}
