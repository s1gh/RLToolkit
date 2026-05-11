package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Errors. Use errors.Is at call sites.
var (
	ErrUpstream        = errors.New("tracker: upstream error")
	ErrUpstreamBlocked = errors.New("tracker: upstream blocked")
	ErrPlayerNotFound  = errors.New("tracker: player not found")
	ErrCircuitOpen     = errors.New("tracker: circuit open")
)

// defaultBaseURL is the production tracker.gg API origin.
const defaultBaseURL = "https://api.tracker.gg"

// Options configures Client. Zero values use sane defaults.
type Options struct {
	HTTPTimeout time.Duration    // default 15s
	Now         func() time.Time // default time.Now (injected for tests)
	BaseURL     string           // default defaultBaseURL; tests point this at httptest
	Doer        doer             // optional override; default newSurfDoer(HTTPTimeout)
	DataDir     string           // empty disables on-disk cache persistence
	CacheTTL    time.Duration    // default 5 * time.Minute
}

// Client is the public tracker.gg lookup client. Safe for concurrent use.
type Client struct {
	d       doer
	limiter *limiter
	breaker *breaker
	cache   *cache
	now     func() time.Time
	baseURL string
	timeout time.Duration
}

// New constructs a Client. When opts.Doer is nil, a surf-backed
// Chrome-impersonating doer is used (production path). When
// opts.DataDir is non-empty, the cache loads from and writes to
// <DataDir>/tracker-mmr-cache.json. Returns an error when the data
// directory cannot be prepared.
func New(opts Options) (*Client, error) {
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = 15 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.CacheTTL == 0 {
		opts.CacheTTL = 5 * time.Minute
	}
	if opts.Doer == nil {
		opts.Doer = newSurfDoer(opts.HTTPTimeout)
	}
	cch, err := newCache(opts.CacheTTL, opts.Now, opts.DataDir)
	if err != nil {
		return nil, err
	}
	return &Client{
		d: opts.Doer,
		// Limiter uses real time even when opts.Now is injected. Its
		// internal time.NewTimer sleeps in real time; pairing that with
		// a stopped fake clock would deadlock (tokens never refill).
		// Tests that need to exercise rate limiting do so on the
		// limiter directly.
		limiter: newLimiter(1.0, 3, 5*time.Second, time.Now),
		breaker: newBreaker(3, 5*time.Minute, opts.Now),
		cache:   cch,
		now:     opts.Now,
		baseURL: opts.BaseURL,
		timeout: opts.HTTPTimeout,
	}, nil
}

// breakerOpenForTest exposes the breaker state to the test file.
func (c *Client) breakerOpenForTest() bool { return c.breaker.IsOpen() }

// BreakerRetryAfter returns the time until the breaker re-closes, or 0
// if it is closed. Used by the HTTP layer to set Retry-After.
func (c *Client) BreakerRetryAfter() time.Duration { return c.breaker.RemainingOpen() }

// Lookup returns the MMR Result for (platform, id). A fresh cache
// entry short-circuits the upstream call entirely (no limiter, no
// breaker check). On miss, the call goes through the breaker and
// rate-limiter before hitting tracker.gg. Successful 200s are written
// through to the cache. Errors are never cached.
func (c *Client) Lookup(ctx context.Context, platform, id string) (*Result, error) {
	// 1. Cache hit short-circuits everything else.
	if cached, fetchedAt, ok := c.cache.Get(platform, id); ok {
		out := *cached // copy so we don't mutate the cached entry
		out.Cached = true
		out.Age = int(c.now().Sub(fetchedAt).Seconds())
		return &out, nil
	}

	// 2. Cache miss: enforce breaker, then rate limiter.
	if c.breaker.IsOpen() {
		return nil, ErrCircuitOpen
	}
	if err := c.limiter.Acquire(ctx); err != nil {
		return nil, err
	}

	// 3. Upstream call.
	url := fmt.Sprintf("%s/api/v2/rocket-league/standard/profile/%s/%s", c.baseURL, platform, id)
	status, body, err := c.d.Do(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}

	switch {
	case status == 200:
		now := c.now()
		res, perr := parseProfile(body, platform, id, now)
		if perr != nil {
			return nil, fmt.Errorf("%w: %v", ErrUpstream, perr)
		}
		c.breaker.RecordSuccess()
		// Write-through to cache, then mark this response as live.
		c.cache.Put(platform, id, res, now)
		out := *res
		out.Cached = false
		out.Age = 0
		return &out, nil
	case status == 403 || status == 429:
		c.breaker.RecordBlocked()
		return nil, fmt.Errorf("%w: status %d", ErrUpstreamBlocked, status)
	case status == 404:
		return nil, ErrPlayerNotFound
	default:
		return nil, fmt.Errorf("%w: status %d", ErrUpstream, status)
	}
}

// playlistNameToKey is the stable mapping documented in the spec. Any
// upstream playlist name not in this table is dropped silently. We
// only expose the three core ranked playlists plus Casual; the extra
// modes (Hoops, Rumble, Dropshot, Snowday) are intentionally dropped.
var playlistNameToKey = map[string]string{
	"Ranked Duel 1v1":     "1v1",
	"Ranked Doubles 2v2":  "2v2",
	"Ranked Standard 3v3": "3v3",
	"Casual":              "casual",
}

// upstreamProfile mirrors the subset of the tracker.gg response we care
// about. Anything outside `data.segments` is ignored.
type upstreamProfile struct {
	Data struct {
		Segments []upstreamSegment `json:"segments"`
	} `json:"data"`
}
type upstreamSegment struct {
	Type     string `json:"type"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Stats struct {
		Rating struct {
			Value int `json:"value"`
		} `json:"rating"`
		Tier struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"tier"`
		Division struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"division"`
		MatchesPlayed struct {
			Value int `json:"value"`
		} `json:"matchesPlayed"`
	} `json:"stats"`
}

func parseProfile(body []byte, platform, id string, now time.Time) (*Result, error) {
	var up upstreamProfile
	if err := json.Unmarshal(body, &up); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if up.Data.Segments == nil {
		return nil, errors.New("missing data.segments")
	}
	out := &Result{
		Platform:  platform,
		PlayerID:  id,
		Playlists: map[string]Rating{},
		FetchedAt: now,
	}
	for _, seg := range up.Data.Segments {
		if seg.Type != "playlist" {
			continue
		}
		key, ok := playlistNameToKey[seg.Metadata.Name]
		if !ok {
			continue
		}
		out.Playlists[key] = Rating{
			MMR:      seg.Stats.Rating.Value,
			Tier:     seg.Stats.Tier.Metadata.Name,
			Division: seg.Stats.Division.Metadata.Name,
			Matches:  seg.Stats.MatchesPlayed.Value,
		}
	}
	return out, nil
}
