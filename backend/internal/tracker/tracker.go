package tracker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Errors. Use errors.Is at call sites; errors.As for UpstreamError when
// you want the recoverable HTTP status of an upstream failure.
var (
	ErrUpstream        = errors.New("tracker: upstream error")
	ErrUpstreamBlocked = errors.New("tracker: upstream blocked")
	ErrPlayerNotFound  = errors.New("tracker: player not found")
	ErrCircuitOpen     = errors.New("tracker: circuit open")
)

// UpstreamError wraps an upstream failure with the HTTP status code
// tracker.gg returned, so callers can use errors.As to inspect the
// code without parsing strings. It also satisfies errors.Is against
// either ErrUpstreamBlocked (for 403/429) or ErrUpstream (other
// non-200, non-404 statuses) via Unwrap.
type UpstreamError struct {
	Status int
	base   error // ErrUpstreamBlocked or ErrUpstream
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("%s: status %d", e.base.Error(), e.Status)
}

func (e *UpstreamError) Unwrap() error { return e.base }

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
		out := cloneResult(cached)
		out.Cached = true
		out.Age = int(c.now().Sub(fetchedAt).Seconds())
		return out, nil
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
		// Preserve context errors unwrapped so the HTTP layer can map
		// "client gave up" to 504 instead of a generic upstream error.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
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
		// Write-through to cache, then return a deep copy so the
		// caller can mutate without poisoning the cached entry.
		c.cache.Put(platform, id, res, now)
		out := cloneResult(res)
		out.Cached = false
		out.Age = 0
		return out, nil
	case status == 403 || status == 429:
		c.breaker.RecordBlocked()
		return nil, &UpstreamError{Status: status, base: ErrUpstreamBlocked}
	case status == 404:
		return nil, ErrPlayerNotFound
	default:
		return nil, &UpstreamError{Status: status, base: ErrUpstream}
	}
}

// cloneResult returns a deep copy of r safe to hand to callers. The
// shallow `*r` copy would share the Playlists map with the cached
// entry; any caller mutation would silently poison the cache.
func cloneResult(r *Result) *Result {
	out := *r
	out.Playlists = make(map[string]Rating, len(r.Playlists))
	for k, v := range r.Playlists {
		out.Playlists[k] = v
	}
	return &out
}
