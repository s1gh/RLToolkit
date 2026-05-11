package tracker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustNewCache(t *testing.T, ttl time.Duration, now func() time.Time, dir string) *cache {
	t.Helper()
	c, err := newCache(ttl, now, dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sampleResult(platform, id string) *Result {
	return &Result{
		Platform:  platform,
		PlayerID:  id,
		Playlists: map[string]Rating{"2v2": {MMR: 1000}},
	}
}

func TestCache_EmptyGetMisses(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := mustNewCache(t, 5*time.Minute, clk.Now, "")
	if _, _, ok := c.Get("steam", "1"); ok {
		t.Fatal("empty cache returned a hit")
	}
}

func TestCache_PutThenGetWithinTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := mustNewCache(t, 5*time.Minute, clk.Now, "")
	stored := sampleResult("steam", "1")
	c.Put("steam", "1", stored, clk.Now())

	got, fetched, ok := c.Get("steam", "1")
	if !ok {
		t.Fatal("expected hit")
	}
	if got.PlayerID != "1" || got.Playlists["2v2"].MMR != 1000 {
		t.Fatalf("payload corrupted: %+v", got)
	}
	if !fetched.Equal(clk.Now()) {
		t.Fatalf("fetchedAt wrong: got %v want %v", fetched, clk.Now())
	}
}

func TestCache_ExpiresAfterTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := mustNewCache(t, 5*time.Minute, clk.Now, "")
	c.Put("steam", "1", sampleResult("steam", "1"), clk.Now())

	clk.tick(4*time.Minute + 59*time.Second)
	if _, _, ok := c.Get("steam", "1"); !ok {
		t.Fatal("entry should still be fresh at 4m59s")
	}
	clk.tick(2 * time.Second)
	if _, _, ok := c.Get("steam", "1"); ok {
		t.Fatal("entry should be expired past 5m")
	}
}

func TestCache_PutOverwrites(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := mustNewCache(t, 5*time.Minute, clk.Now, "")

	first := sampleResult("steam", "1")
	first.Playlists["2v2"] = Rating{MMR: 500}
	c.Put("steam", "1", first, clk.Now())

	clk.tick(30 * time.Second)
	second := sampleResult("steam", "1")
	second.Playlists["2v2"] = Rating{MMR: 1200}
	c.Put("steam", "1", second, clk.Now())

	got, _, ok := c.Get("steam", "1")
	if !ok || got.Playlists["2v2"].MMR != 1200 {
		t.Fatalf("overwrite failed: %+v", got)
	}
}

func TestCache_DiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	c1 := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	c1.Put("steam", "1", sampleResult("steam", "1"), clk.Now())

	// Construct a second cache from the same dir: must recover the entry.
	c2 := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	got, fetched, ok := c2.Get("steam", "1")
	if !ok {
		t.Fatal("entry missing after reload")
	}
	if got.PlayerID != "1" || got.Playlists["2v2"].MMR != 1000 {
		t.Fatalf("payload mismatch after reload: %+v", got)
	}
	if !fetched.Equal(clk.Now()) {
		t.Fatalf("fetchedAt drifted: got %v want %v", fetched, clk.Now())
	}
}

func TestCache_StaleOnLoadIsDropped(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	c1 := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	c1.Put("steam", "1", sampleResult("steam", "1"), clk.Now())

	// Advance past TTL, then construct a fresh cache: the on-disk entry
	// must be dropped.
	clk.tick(6 * time.Minute)
	c2 := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	if _, _, ok := c2.Get("steam", "1"); ok {
		t.Fatal("stale on-disk entry should have been dropped at load")
	}
}

func TestCache_CorruptFileFallsBackToEmpty(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	if err := os.WriteFile(filepath.Join(dir, "tracker-mmr-cache.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	if _, _, ok := c.Get("steam", "1"); ok {
		t.Fatal("cache should be empty after corrupt file")
	}
}

func TestCache_NoDataDirSkipsDisk(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := mustNewCache(t, 5*time.Minute, clk.Now, "")
	c.Put("steam", "1", sampleResult("steam", "1"), clk.Now())
	if _, _, ok := c.Get("steam", "1"); !ok {
		t.Fatal("in-memory entry missing")
	}
}

func TestCache_PlatformKeyCaseInsensitive(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := mustNewCache(t, 5*time.Minute, clk.Now, "")
	c.Put("Steam", "1", sampleResult("steam", "1"), clk.Now())

	// Different-case platform must hit the same entry.
	if _, _, ok := c.Get("steam", "1"); !ok {
		t.Fatal("lowercase platform should hit a Steam-cased Put")
	}
	if _, _, ok := c.Get("STEAM", "1"); !ok {
		t.Fatal("uppercase platform should hit a Steam-cased Put")
	}
}

func TestCache_DiskSecondWriteSucceeds(t *testing.T) {
	// Regression guard: os.Rename does not overwrite on Windows, so the
	// second Put used to silently fail on that platform. flushLocked
	// removes the destination first; this test asserts the visible
	// outcome (subsequent reload sees the latest entry) on every OS.
	dir := t.TempDir()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	c1 := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	first := sampleResult("steam", "1")
	first.Playlists["2v2"] = Rating{MMR: 500}
	c1.Put("steam", "1", first, clk.Now())

	clk.tick(10 * time.Second)
	second := sampleResult("steam", "2") // different key + payload
	c1.Put("steam", "2", second, clk.Now())

	clk.tick(10 * time.Second)
	updated := sampleResult("steam", "1")
	updated.Playlists["2v2"] = Rating{MMR: 1200}
	c1.Put("steam", "1", updated, clk.Now())

	// Reload from disk: must see the updated value for "1" and the
	// second key "2", proving every write made it to disk.
	c2 := mustNewCache(t, 5*time.Minute, clk.Now, dir)
	r1, _, ok := c2.Get("steam", "1")
	if !ok || r1.Playlists["2v2"].MMR != 1200 {
		t.Fatalf("steam/1 after reload: %+v ok=%v", r1, ok)
	}
	if _, _, ok := c2.Get("steam", "2"); !ok {
		t.Fatal("steam/2 missing after reload")
	}
}
