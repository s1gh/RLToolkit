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
