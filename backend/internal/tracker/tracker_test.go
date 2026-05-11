package tracker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// fakeDoer is a controllable doer for unit tests. callCount lets cache
// tests assert that an upstream call did or did not happen.
type fakeDoer struct {
	status    int
	body      []byte
	err       error
	gotURL    string
	callCount int
}

func (f *fakeDoer) Do(_ context.Context, url string) (int, []byte, error) {
	f.callCount++
	f.gotURL = url
	if f.err != nil {
		return 0, nil, f.err
	}
	return f.status, f.body, nil
}

// newTestClient builds a Client whose doer is the given fake. It uses
// generous limiter values so unit tests don't trip the limiter. No
// disk cache: DataDir is empty.
func newTestClient(d doer) *Client {
	c, err := New(Options{
		HTTPTimeout: 5 * time.Second,
		Doer:        d,
	})
	if err != nil {
		panic(err) // Options without DataDir can't fail
	}
	return c
}

// newTestClientWithClock is like newTestClient but uses the given
// injectable clock so cache-TTL tests can advance time without sleeping.
func newTestClientWithClock(d doer, now func() time.Time) *Client {
	c, err := New(Options{
		HTTPTimeout: 5 * time.Second,
		Doer:        d,
		Now:         now,
	})
	if err != nil {
		panic(err)
	}
	return c
}

func mustReadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/profile_steam_ok.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLookup_HappyPath(t *testing.T) {
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c := newTestClient(d)

	res, err := c.Lookup(context.Background(), "steam", "76561197964340095")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Platform != "steam" || res.PlayerID != "76561197964340095" {
		t.Fatalf("identity wrong: %+v", res)
	}
	if res.Cached {
		t.Errorf("fresh lookup should have Cached=false, got true")
	}
	if res.Age != 0 {
		t.Errorf("fresh lookup should have Age=0, got %d", res.Age)
	}
	wantURL := "https://api.tracker.gg/api/v2/rocket-league/standard/profile/steam/76561197964340095"
	if d.gotURL != wantURL {
		t.Fatalf("URL mismatch:\n got %s\nwant %s", d.gotURL, wantURL)
	}
}

func TestLookup_PlaylistKeyNormalization(t *testing.T) {
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c := newTestClient(d)

	res, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		mmr      int
		tier     string
		division string
		matches  int
	}{
		"1v1":    {785, "Platinum III", "Division III", 0},
		"2v2":    {1005, "Diamond III", "Division II", 0},
		"3v3":    {1067, "Diamond III", "Division IV", 10},
		"casual": {1405, "Unranked", "Division I", 0},
	}
	if len(res.Playlists) != len(cases) {
		t.Fatalf("playlist count: got %d, want %d (extra modes + unknown should be dropped)", len(res.Playlists), len(cases))
	}
	for k, want := range cases {
		got, ok := res.Playlists[k]
		if !ok {
			t.Errorf("missing playlist %q", k)
			continue
		}
		if got.MMR != want.mmr || got.Tier != want.tier || got.Division != want.division || got.Matches != want.matches {
			t.Errorf("playlist %q: got %+v, want %+v", k, got, want)
		}
	}
	for _, k := range []string{"hoops", "rumble", "dropshot", "snowday"} {
		if _, present := res.Playlists[k]; present {
			t.Errorf("extra-mode playlist %q should have been dropped", k)
		}
	}
}

func TestLookup_BaseURLOverride(t *testing.T) {
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c, err := New(Options{BaseURL: "http://127.0.0.1:1234", Doer: d})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Lookup(context.Background(), "steam", "9")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:1234/api/v2/rocket-league/standard/profile/steam/9"
	if d.gotURL != want {
		t.Fatalf("URL mismatch:\n got %s\nwant %s", d.gotURL, want)
	}
}

func TestLookup_MalformedJSONReturnsErrUpstream(t *testing.T) {
	d := &fakeDoer{status: 200, body: []byte(`{not json`)}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
}

func TestLookup_MissingSegmentsReturnsErrUpstream(t *testing.T) {
	d := &fakeDoer{status: 200, body: []byte(`{"data":{}}`)}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
}

func TestLookup_403IsErrUpstreamBlocked(t *testing.T) {
	d := &fakeDoer{status: 403, body: []byte(`{"errors":[{"code":"X"}]}`)}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrUpstreamBlocked) {
		t.Fatalf("want ErrUpstreamBlocked, got %v", err)
	}
}

func TestLookup_429IsErrUpstreamBlocked(t *testing.T) {
	d := &fakeDoer{status: 429, body: nil}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrUpstreamBlocked) {
		t.Fatalf("want ErrUpstreamBlocked, got %v", err)
	}
}

func TestLookup_404IsErrPlayerNotFound(t *testing.T) {
	d := &fakeDoer{status: 404}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrPlayerNotFound) {
		t.Fatalf("want ErrPlayerNotFound, got %v", err)
	}
}

func TestLookup_500IsErrUpstream(t *testing.T) {
	d := &fakeDoer{status: 500}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
}

func TestLookup_NetworkErrorIsErrUpstream(t *testing.T) {
	d := &fakeDoer{err: errors.New("dial tcp: i/o timeout")}
	c := newTestClient(d)
	_, err := c.Lookup(context.Background(), "steam", "1")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
}

func TestLookup_BlockedIncrementsBreakerSuccessResets(t *testing.T) {
	// Always-403 doer: 3 calls trip the breaker.
	blocked := &fakeDoer{status: 403}
	c := newTestClient(blocked)
	for i := 0; i < 3; i++ {
		_, _ = c.Lookup(context.Background(), "steam", "1")
	}
	if !c.breakerOpenForTest() {
		t.Fatal("breaker should be open after 3 blocks")
	}

	// Fresh client: two blocks + success + two blocks should NOT open it.
	mixed := &fakeDoer{}
	body := mustReadFixture(t)
	c2 := newTestClient(mixed)
	for i := 0; i < 2; i++ {
		mixed.status = 403
		mixed.body = nil
		_, _ = c2.Lookup(context.Background(), "steam", "1")
	}
	mixed.status = 200
	mixed.body = body
	if _, err := c2.Lookup(context.Background(), "steam", "1"); err != nil {
		t.Fatalf("success call failed: %v", err)
	}
	for i := 0; i < 2; i++ {
		mixed.status = 403
		mixed.body = nil
		_, _ = c2.Lookup(context.Background(), "steam", "1")
	}
	if c2.breakerOpenForTest() {
		t.Fatal("breaker opened on 2 blocks after a success reset")
	}
}

func TestLookup_CacheHitSkipsUpstream(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c := newTestClientWithClock(d, clk.Now)

	// First call: cache miss, hits upstream.
	r1, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Cached || r1.Age != 0 {
		t.Fatalf("first call should be live: Cached=%v Age=%d", r1.Cached, r1.Age)
	}
	if d.callCount != 1 {
		t.Fatalf("upstream calls after first lookup: got %d want 1", d.callCount)
	}

	// Advance 90s; second call: cache hit, no upstream.
	clk.tick(90 * time.Second)
	r2, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Cached {
		t.Errorf("second call should be Cached=true")
	}
	if r2.Age != 90 {
		t.Errorf("Age: got %d want 90", r2.Age)
	}
	if d.callCount != 1 {
		t.Errorf("upstream calls after cache hit: got %d want 1 (unchanged)", d.callCount)
	}
	// Cached payload should match the live one.
	if r2.Playlists["3v3"].MMR != r1.Playlists["3v3"].MMR {
		t.Errorf("cached payload diverged: %+v vs %+v", r2.Playlists["3v3"], r1.Playlists["3v3"])
	}
}

func TestLookup_CacheExpiresAfterTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c := newTestClientWithClock(d, clk.Now)

	if _, err := c.Lookup(context.Background(), "steam", "1"); err != nil {
		t.Fatal(err)
	}
	if d.callCount != 1 {
		t.Fatalf("upstream calls: got %d want 1", d.callCount)
	}

	// Past TTL: second call should hit upstream again.
	clk.tick(5*time.Minute + 1*time.Second)
	r, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Cached {
		t.Errorf("post-TTL call should be live, got Cached=true")
	}
	if d.callCount != 2 {
		t.Errorf("upstream calls after TTL: got %d want 2", d.callCount)
	}
}

func TestLookup_ErrorsAreNotCached(t *testing.T) {
	d := &fakeDoer{status: 403}
	c := newTestClient(d)

	// First call: 403, not cached.
	if _, err := c.Lookup(context.Background(), "steam", "1"); !errors.Is(err, ErrUpstreamBlocked) {
		t.Fatalf("first call err: %v", err)
	}
	// Flip to 200; second call should reach upstream (no poisoned cache).
	d.status = 200
	d.body = mustReadFixture(t)
	r, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Cached {
		t.Errorf("error should not have populated the cache: got Cached=true")
	}
	if d.callCount != 2 {
		t.Errorf("upstream calls: got %d want 2 (one per Lookup since 403 wasn't cached)", d.callCount)
	}
}

func TestLookup_CacheHitBypassesOpenBreaker(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c := newTestClientWithClock(d, clk.Now)

	// Prime the cache.
	if _, err := c.Lookup(context.Background(), "steam", "1"); err != nil {
		t.Fatal(err)
	}

	// Trip the breaker manually on a DIFFERENT key so we don't reset
	// it with the success above.
	d.status = 403
	for i := 0; i < 3; i++ {
		_, _ = c.Lookup(context.Background(), "steam", "999")
	}
	if !c.breakerOpenForTest() {
		t.Fatal("breaker should be open")
	}

	// Cached entry for "1" should still be servable: breaker is checked
	// AFTER the cache lookup.
	r, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatalf("cached lookup with open breaker: %v", err)
	}
	if !r.Cached {
		t.Error("expected cached hit")
	}
}

func TestLookup_404DoesNotTripBreaker(t *testing.T) {
	d := &fakeDoer{status: 404}
	c := newTestClient(d)
	// Three calls is enough: the breaker threshold is 3. If 404
	// incremented the counter, the third call would open it. We stay
	// at three because the limiter burst is three; a fourth call would
	// pay a real-time second waiting for refill.
	for i := 0; i < 3; i++ {
		_, _ = c.Lookup(context.Background(), "steam", "1")
	}
	if c.breakerOpenForTest() {
		t.Fatal("404s should not increment the breaker")
	}
}

func TestLookup_500DoesNotTripBreaker(t *testing.T) {
	d := &fakeDoer{status: 500}
	c := newTestClient(d)
	for i := 0; i < 3; i++ {
		_, _ = c.Lookup(context.Background(), "steam", "1")
	}
	if c.breakerOpenForTest() {
		t.Fatal("5xx errors should not increment the breaker")
	}
}

func TestLookup_ReturnedPlaylistsMapIsCallerSafe(t *testing.T) {
	// Regression guard: Lookup must return a deep copy of the cached
	// Result so a caller mutating Playlists does not poison the cache.
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	d := &fakeDoer{status: 200, body: mustReadFixture(t)}
	c := newTestClientWithClock(d, clk.Now)

	r1, err := c.Lookup(context.Background(), "steam", "1")
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the returned map.
	r1.Playlists["3v3"] = Rating{MMR: -1, Tier: "POISONED"}

	clk.tick(30 * time.Second)
	r2, err := c.Lookup(context.Background(), "steam", "1") // cache hit
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Cached {
		t.Fatal("expected cache hit")
	}
	if r2.Playlists["3v3"].MMR == -1 || r2.Playlists["3v3"].Tier == "POISONED" {
		t.Fatalf("cache poisoned by caller mutation: %+v", r2.Playlists["3v3"])
	}
}
