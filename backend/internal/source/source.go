// Package source provides event sources for the pipeline: RL (a TCP
// client to the Stats API) and Fixture (a JSONL replayer used by tests
// and demos).
package source

import (
	"log"
	"sync"
	"time"
)

// Tunables for the RL TCP client. The numbers here are load-bearing: a
// stalled SSE write or a backed-up bus eventually fills RL's TCP send
// buffer, and on Linux/Proton that freezes RL's main game thread inside
// send(). The TCP read loop and outbox prefer "drop" over "block" for
// that reason.
const (
	// outboxBufSize is the ring between the TCP read loop and the bus
	// dispatcher. At RL's recommended 10 Hz that's ~100s of headroom;
	// at the 120 Hz max, ~8s. If it fills, packets drop (not block).
	outboxBufSize = 1024

	// tcpReadBuffer increases kernel headroom so bursts don't backpressure
	// RL's send().
	tcpReadBuffer = 1 << 20 // 1 MiB

	dialTimeout       = 5 * time.Second
	reconnectDelay    = 500 * time.Millisecond
	rlIdleTimeout     = 30 * time.Second // force reconnect after silence
	statusPushTimeout = 100 * time.Millisecond
	dropLogInterval   = 5 * time.Second

	// idleLogInterval rate-limits the "silent for 30s — reconnecting"
	// cycle so a long menu / between-matches stretch doesn't fill the
	// terminal with one cycle log every 30s.
	idleLogInterval = 5 * time.Minute
)

// rateLimitedLogger emits at most one log line per `interval`.
type rateLimitedLogger struct {
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func (r *rateLimitedLogger) log(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.last) < r.interval {
		return
	}
	r.last = time.Now()
	log.Println(msg)
}

// reset clears the rate limiter so the next log() call fires
// immediately, e.g. after a reconnect succeeds.
func (r *rateLimitedLogger) reset() {
	r.mu.Lock()
	r.last = time.Time{}
	r.mu.Unlock()
}
