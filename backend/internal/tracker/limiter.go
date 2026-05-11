package tracker

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrRateLimited is returned when Acquire's wait would exceed the budget.
var ErrRateLimited = errors.New("tracker: rate limited")

// limiter is a token bucket with a per-call wait budget. The clock is
// injectable so tests don't sleep.
type limiter struct {
	mu       sync.Mutex
	rate     float64       // tokens per second
	capacity float64       // burst capacity
	budget   time.Duration // max wait per Acquire
	now      func() time.Time
	tokens   float64
	last     time.Time
}

func newLimiter(rate float64, burst int, budget time.Duration, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{
		rate:     rate,
		capacity: float64(burst),
		budget:   budget,
		now:      now,
		tokens:   float64(burst),
		last:     now(),
	}
}

// Acquire blocks until one token is available, the budget elapses, or
// ctx is done. It returns ErrRateLimited when the budget would be
// exceeded; ctx.Err() when ctx fires first.
func (l *limiter) Acquire(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		elapsed := now.Sub(l.last).Seconds()
		if elapsed > 0 {
			l.tokens += elapsed * l.rate
			if l.tokens > l.capacity {
				l.tokens = l.capacity
			}
			l.last = now
		}
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		need := 1 - l.tokens
		wait := time.Duration(need / l.rate * float64(time.Second))
		l.mu.Unlock()

		if wait > l.budget {
			return ErrRateLimited
		}

		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// breaker is a consecutive-failure circuit breaker. Calls to
// RecordBlocked count toward the threshold; RecordSuccess resets the
// counter and closes the breaker. When open, IsOpen returns true until
// `cooldown` elapses, at which point it half-opens (IsOpen returns
// false but the counter is unchanged — the next success closes it).
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	count     int
	openedAt  time.Time // zero when closed
}

func newBreaker(threshold int, cooldown time.Duration, now func() time.Time) *breaker {
	if now == nil {
		now = time.Now
	}
	return &breaker{threshold: threshold, cooldown: cooldown, now: now}
}

// IsOpen reports whether the breaker should short-circuit calls. The
// first call after the cooldown has elapsed has a side effect: it
// clears the open marker so the breaker transitions to half-open.
// The fault counter is intentionally not reset; the next RecordBlocked
// re-opens the breaker immediately.
func (b *breaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return false
	}
	if b.now().Sub(b.openedAt) >= b.cooldown {
		// Half-open: stop short-circuiting, but leave the counter alone
		// so a fresh block re-opens immediately.
		b.openedAt = time.Time{}
		return false
	}
	return true
}

// RecordBlocked increments the failure counter. If we hit the
// threshold, the breaker opens for `cooldown`.
func (b *breaker) RecordBlocked() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count++
	if b.count >= b.threshold {
		b.openedAt = b.now()
	}
}

// RecordSuccess resets the counter and closes the breaker.
func (b *breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count = 0
	b.openedAt = time.Time{}
}

// RemainingOpen returns the duration until the breaker re-opens, or 0
// if it's already closed.
func (b *breaker) RemainingOpen() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openedAt.IsZero() {
		return 0
	}
	left := b.cooldown - b.now().Sub(b.openedAt)
	if left < 0 {
		return 0
	}
	return left
}
