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

func TestBreaker_OpensAfterThreeBlocks(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := newBreaker(3, 5*time.Minute, clk.Now)

	b.RecordBlocked()
	b.RecordBlocked()
	if b.IsOpen() {
		t.Fatal("breaker opened after 2 blocks")
	}

	b.RecordBlocked()
	if !b.IsOpen() {
		t.Fatal("breaker did not open after 3 blocks")
	}
}

func TestBreaker_SuccessResetsCounter(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := newBreaker(3, 5*time.Minute, clk.Now)

	b.RecordBlocked()
	b.RecordBlocked()
	b.RecordSuccess()
	b.RecordBlocked()
	b.RecordBlocked()
	if b.IsOpen() {
		t.Fatal("two blocks after a success should not open the breaker")
	}
}

func TestBreaker_HalfOpenAfterCooldown(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := newBreaker(3, 5*time.Minute, clk.Now)

	b.RecordBlocked()
	b.RecordBlocked()
	b.RecordBlocked()
	if !b.IsOpen() {
		t.Fatal("breaker should be open")
	}

	clk.tick(4*time.Minute + 59*time.Second)
	if !b.IsOpen() {
		t.Fatal("breaker should still be open at 4m59s")
	}

	clk.tick(2 * time.Second)
	if b.IsOpen() {
		t.Fatal("breaker should be half-open after 5 min")
	}
	b.RecordSuccess()
	b.RecordBlocked()
	b.RecordBlocked()
	if b.IsOpen() {
		t.Fatal("breaker tripped on 2 blocks after success close")
	}
}

func TestBreaker_HalfOpenReopensOnNextBlock(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := newBreaker(3, 5*time.Minute, clk.Now)

	b.RecordBlocked()
	b.RecordBlocked()
	b.RecordBlocked()

	// Past cooldown: half-open (the fault counter is intentionally
	// preserved — a flapping upstream should not get three fresh
	// strikes).
	clk.tick(5*time.Minute + time.Second)
	if b.IsOpen() {
		t.Fatal("expected half-open after cooldown")
	}

	// One more block without an intervening success must re-open
	// immediately because count was preserved.
	b.RecordBlocked()
	if !b.IsOpen() {
		t.Fatal("half-open + 1 block should re-open the breaker")
	}
}

func TestBreaker_RemainingOpen(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := newBreaker(3, 5*time.Minute, clk.Now)

	if got := b.RemainingOpen(); got != 0 {
		t.Fatalf("closed breaker RemainingOpen: got %s want 0", got)
	}
	b.RecordBlocked()
	b.RecordBlocked()
	b.RecordBlocked()
	clk.tick(1 * time.Minute)
	got := b.RemainingOpen()
	want := 4 * time.Minute
	if got != want {
		t.Fatalf("RemainingOpen: got %s want %s", got, want)
	}
}
