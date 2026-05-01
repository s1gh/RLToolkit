package main

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// metricsRingSize is the number of recent publish durations we retain
// for percentile estimates. Sized to ~5s of 120Hz traffic — enough to
// smooth out a single slow tick, small enough that recent regressions
// dominate the window.
const metricsRingSize = 600

// busMetrics is process-wide telemetry for the event bus. Counters use
// atomics so the publish path doesn't take a mutex; the duration ring
// uses a tiny lock that's only held while appending one int64.
type busMetrics struct {
	startedAt time.Time

	publishes  atomic.Uint64 // total Publish calls
	deliveries atomic.Uint64 // total per-subscriber writes that landed
	evictions  atomic.Uint64 // subscribers dropped for being slow
	skipped    atomic.Uint64 // per-subscriber writes skipped by event filter
	bytesOut   atomic.Uint64 // total bytes delivered downstream

	durMu  sync.Mutex
	durs   [metricsRingSize]int64 // publish wall durations, in ns
	durIdx int                    // next write slot
	durLen int                    // total samples seen, capped at len(durs)
}

func newBusMetrics() *busMetrics {
	return &busMetrics{startedAt: time.Now()}
}

func (m *busMetrics) recordPublish(durNS int64, deliveries, skipped, evicted, bytesOut int) {
	m.publishes.Add(1)
	m.deliveries.Add(uint64(deliveries))
	m.skipped.Add(uint64(skipped))
	m.evictions.Add(uint64(evicted))
	m.bytesOut.Add(uint64(bytesOut))

	m.durMu.Lock()
	m.durs[m.durIdx] = durNS
	m.durIdx = (m.durIdx + 1) % len(m.durs)
	if m.durLen < len(m.durs) {
		m.durLen++
	}
	m.durMu.Unlock()
}

// metricsSnapshot is the wire shape served at /api/metrics. Counters
// are cumulative since process start; durations are summarized over
// the recent window. Keep field names stable — dashboards and
// external tools may scrape this.
type metricsSnapshot struct {
	UptimeSec       float64 `json:"uptime_sec"`
	Subscribers     int     `json:"subscribers"`
	Publishes       uint64  `json:"publishes_total"`
	Deliveries      uint64  `json:"deliveries_total"`
	Skipped         uint64  `json:"skipped_total"`
	Evictions       uint64  `json:"evictions_total"`
	BytesOut        uint64  `json:"bytes_out_total"`
	PublishesPerSec float64 `json:"publishes_per_sec"`
	PublishUsP50    int64   `json:"publish_us_p50"`
	PublishUsP95    int64   `json:"publish_us_p95"`
	PublishUsP99    int64   `json:"publish_us_p99"`
	PublishUsMax    int64   `json:"publish_us_max"`
}

func (m *busMetrics) snapshot(subscribers int) metricsSnapshot {
	uptime := time.Since(m.startedAt).Seconds()
	publishes := m.publishes.Load()

	rate := 0.0
	if uptime > 0 {
		rate = float64(publishes) / uptime
	}

	p50, p95, p99, max := m.percentilesUS()

	return metricsSnapshot{
		UptimeSec:       uptime,
		Subscribers:     subscribers,
		Publishes:       publishes,
		Deliveries:      m.deliveries.Load(),
		Skipped:         m.skipped.Load(),
		Evictions:       m.evictions.Load(),
		BytesOut:        m.bytesOut.Load(),
		PublishesPerSec: rate,
		PublishUsP50:    p50,
		PublishUsP95:    p95,
		PublishUsP99:    p99,
		PublishUsMax:    max,
	}
}

// percentilesUS computes p50/p95/p99/max over the duration ring,
// converted to microseconds. Returns zeros if no samples yet.
//
// Sort-on-read is fine here: /api/metrics isn't on a hot path, and
// metricsRingSize is small enough that an O(N log N) sort is microseconds.
// Trades read CPU for write simplicity (no maintained order on insert).
func (m *busMetrics) percentilesUS() (p50, p95, p99, max int64) {
	m.durMu.Lock()
	if m.durLen == 0 {
		m.durMu.Unlock()
		return 0, 0, 0, 0
	}
	buf := make([]int64, m.durLen)
	copy(buf, m.durs[:m.durLen])
	m.durMu.Unlock()

	sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
	pick := func(p float64) int64 {
		idx := int(float64(len(buf)-1) * p)
		return buf[idx] / 1000
	}
	return pick(0.50), pick(0.95), pick(0.99), buf[len(buf)-1] / 1000
}
