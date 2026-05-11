package tracker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock advances only when tick() is called.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) tick(d time.Duration)    { c.t = c.t.Add(d) }

func TestLimiter_BurstAllowsThreeImmediate(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(1.0, 3, 5*time.Second, clk.Now)

	for i := 0; i < 3; i++ {
		if err := l.Acquire(context.Background()); err != nil {
			t.Fatalf("burst[%d] failed: %v", i, err)
		}
	}
}

func TestLimiter_FourthBlocksUntilRefill(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(1.0, 3, 5*time.Second, clk.Now)

	for i := 0; i < 3; i++ {
		_ = l.Acquire(context.Background())
	}

	// Drained: with no clock advance the 4th call has no token and a
	// 1s wait ahead of it. A pre-cancelled context must surface as
	// ctx.Err(), not ErrRateLimited (the 1s wait is well within the
	// 5s budget).
	clk.tick(0)
	tightCtx, cancel := context.WithTimeout(context.Background(), 0)
	cancel()
	err := l.Acquire(tightCtx)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.{DeadlineExceeded,Canceled}, got %v", err)
	}

	// Advance one second so a token refills, then Acquire must succeed.
	clk.tick(1 * time.Second)
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatalf("after 1s refill: %v", err)
	}
}

func TestLimiter_BudgetExceededReturnsErrRateLimited(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(0.1, 1, 1*time.Second, clk.Now)

	if err := l.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.Acquire(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}
