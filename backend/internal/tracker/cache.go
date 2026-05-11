package tracker

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cacheFileName is the on-disk JSON file under DataDir. Single flat
// object keyed by "<platform>/<id>".
const cacheFileName = "tracker-mmr-cache.json"

// Result is the public response shape returned by tracker.Client.Lookup.
// It lives here because the cache is the first consumer; tracker.go
// (Task 7) wires it into Lookup.
type Result struct {
	Platform  string            `json:"platform"`
	PlayerID  string            `json:"playerId"`
	Playlists map[string]Rating `json:"playlists"`
	FetchedAt time.Time         `json:"fetchedAt"` // when the upstream call returned 200
	Cached    bool              `json:"cached"`    // true when served from cache, false on miss
	Age       int               `json:"age"`       // seconds since FetchedAt at response time
}

// Rating is the per-playlist data inside a Result.
type Rating struct {
	MMR      int    `json:"mmr"`
	Tier     string `json:"tier"`
	Division string `json:"division"`
	Matches  int    `json:"matches"`
}

// cacheEntry is what we persist per key. The full Result is stored
// (not just the playlists) so reloads return the same shape.
type cacheEntry struct {
	Result    *Result   `json:"result"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// cache holds successful Lookup results for a short TTL. Writes are
// write-through to disk when path is non-empty. The single mutex covers
// reads, writes, and file I/O; volume is tiny.
type cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	path    string // empty disables disk persistence
	entries map[string]cacheEntry
}

// newCache builds a cache. When dataDir is non-empty, the cache loads
// any existing tracker-mmr-cache.json under it and writes back on every
// Put. Stale-on-load entries are dropped during construction. A
// corrupt file is logged and replaced with an empty cache.
func newCache(ttl time.Duration, now func() time.Time, dataDir string) (*cache, error) {
	if now == nil {
		now = time.Now
	}
	c := &cache{
		ttl:     ttl,
		now:     now,
		entries: map[string]cacheEntry{},
	}
	if dataDir == "" {
		return c, nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	c.path = filepath.Join(dataDir, cacheFileName)

	body, err := os.ReadFile(c.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	if len(body) == 0 {
		return c, nil
	}
	var raw map[string]cacheEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		log.Printf("[tracker] cache file %s is corrupt, starting empty: %v", c.path, err)
		return c, nil
	}
	cutoff := now().Add(-ttl)
	for k, e := range raw {
		if e.Result == nil || e.FetchedAt.Before(cutoff) {
			continue
		}
		c.entries[k] = e
	}
	return c, nil
}

func cacheKey(platform, id string) string { return platform + "/" + id }

// Get returns (result, fetchedAt, true) when an entry exists and is
// still fresh. Expired entries are evicted lazily.
func (c *cache) Get(platform, id string) (*Result, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[cacheKey(platform, id)]
	if !ok {
		return nil, time.Time{}, false
	}
	if c.now().Sub(e.FetchedAt) >= c.ttl {
		delete(c.entries, cacheKey(platform, id))
		return nil, time.Time{}, false
	}
	return e.Result, e.FetchedAt, true
}

// Put stores or overwrites the entry for (platform, id). When path is
// set, the full cache (minus expired entries) is written to disk
// synchronously. Disk errors are logged but not surfaced; the
// in-memory cache is authoritative.
func (c *cache) Put(platform, id string, r *Result, fetchedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cacheKey(platform, id)] = cacheEntry{Result: r, FetchedAt: fetchedAt}
	if c.path == "" {
		return
	}
	c.flushLocked()
}

// flushLocked writes the current entries to disk atomically. The caller
// holds c.mu.
func (c *cache) flushLocked() {
	cutoff := c.now().Add(-c.ttl)
	out := make(map[string]cacheEntry, len(c.entries))
	for k, e := range c.entries {
		if e.FetchedAt.Before(cutoff) {
			continue
		}
		out[k] = e
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		log.Printf("[tracker] cache marshal failed: %v", err)
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		log.Printf("[tracker] cache write %s failed: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		log.Printf("[tracker] cache rename %s failed: %v", c.path, err)
	}
}
