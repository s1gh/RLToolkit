package bus

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// metricsRingSize is how many recent publish durations we retain for
// percentile estimates. The window is sample-count based, NOT
// time-based: in a busy match (~120 Hz of UpdateState fanouts) it
// covers ~5 seconds; in an idle session it can stretch to many
// minutes. Big enough to smooth out a single slow publish; small
// enough that a recent regression dominates the percentiles.
const metricsRingSize = 600

// busMetrics is process-wide telemetry for the event bus. Counters use
// atomics so the publish path doesn't take a mutex; the duration ring
// uses a tiny lock that's only held while appending one int64.
type busMetrics struct {
	startedAt time.Time

	publishes     atomic.Uint64 // total Broadcast calls (one per published event)
	deliveries    atomic.Uint64 // per-subscriber channel writes that landed
	evictions     atomic.Uint64 // subscribers detached for being slow (channel full)
	filterRejects atomic.Uint64 // fanout slots a subscriber filter declined
	bytesQueued   atomic.Uint64 // bytes handed to subscriber channels (not wire)

	durMu  sync.Mutex
	durs   [metricsRingSize]int64 // publish wall durations, in ns
	durIdx int                    // next write slot
	durLen int                    // total samples seen, capped at len(durs)
}

func newBusMetrics() *busMetrics {
	return &busMetrics{startedAt: time.Now()}
}

func (m *busMetrics) recordPublish(durNS int64, deliveries, filterRejects, evicted, bytesQueued int) {
	m.publishes.Add(1)
	m.deliveries.Add(uint64(deliveries))
	m.filterRejects.Add(uint64(filterRejects))
	m.evictions.Add(uint64(evicted))
	m.bytesQueued.Add(uint64(bytesQueued))

	m.durMu.Lock()
	m.durs[m.durIdx] = durNS
	m.durIdx = (m.durIdx + 1) % len(m.durs)
	if m.durLen < len(m.durs) {
		m.durLen++
	}
	m.durMu.Unlock()
}

// MetricsSnapshot is the wire shape served at /api/metrics. Counters
// are cumulative since process start; durations are summarized over a
// recent fixed-size sample window (see metricsRingSize). Keep field
// names stable — dashboards and external tools may scrape this.
//
// Field semantics — what each value really counts:
//
//   - uptime_sec: seconds since the bus was constructed (effectively
//     since the backend started).
//   - subscribers: current count of attached subscribers. In
//     production this equals active SSE clients (the only Subscribe
//     caller is the /api/events handler).
//   - publishes_total: number of Bus.Broadcast calls processed,
//     regardless of subscriber count.
//   - deliveries_total: subscriber-channel writes that succeeded
//     (channel had room). Excludes filter rejects and slow-path
//     evictions.
//   - filter_rejects_total: fanout slots a subscriber's event filter
//     declined (e.g. dashboard subscribed to {_StoreChanged} when a
//     _BallHit broadcasts). NOT a back-pressure signal — a growing
//     value just means subscribers are being selective, which is the
//     expected behavior. Watch evictions_total for genuine
//     slow-consumer problems.
//   - evictions_total: subscribers the bus detached because their
//     channel was full when a publish hit them. This is the
//     back-pressure / slow-consumer signal. Counts unique-detachments,
//     not occasions of fullness — a single evicted subscriber adds 1
//     and never appears again.
//   - bytes_queued_total: total bytes the bus handed to subscriber
//     channels (i.e. delivered × envelope size summed across
//     broadcasts). Does NOT measure bytes that left the SSE socket;
//     a subscriber that disconnects mid-flight still has its
//     in-channel bytes counted here. Useful as a "how chatty are my
//     plugins" proxy, not as a network-traffic gauge.
//   - publishes_per_sec: publishes_total / uptime_sec. A LIFETIME
//     average — not a rolling rate. To detect rate changes, sample
//     the snapshot over time and diff publishes_total yourself.
//   - publish_us_p50/p95/p99/max: publish wall-duration percentiles
//     over the most recent metricsRingSize publishes (NOT lifetime).
//     One Broadcast = one sample regardless of subscriber count.
//     "max" is the max within the window, so it decays as old slow
//     samples roll out. All values in microseconds.
type MetricsSnapshot struct {
	UptimeSec       float64 `json:"uptime_sec"`
	Subscribers     int     `json:"subscribers"`
	Publishes       uint64  `json:"publishes_total"`
	Deliveries      uint64  `json:"deliveries_total"`
	FilterRejects   uint64  `json:"filter_rejects_total"`
	Evictions       uint64  `json:"evictions_total"`
	BytesQueued     uint64  `json:"bytes_queued_total"`
	PublishesPerSec float64 `json:"publishes_per_sec"`
	PublishUsP50    int64   `json:"publish_us_p50"`
	PublishUsP95    int64   `json:"publish_us_p95"`
	PublishUsP99    int64   `json:"publish_us_p99"`
	PublishUsMax    int64   `json:"publish_us_max"`
}

func (m *busMetrics) snapshot(subscribers int) MetricsSnapshot {
	uptime := time.Since(m.startedAt).Seconds()
	publishes := m.publishes.Load()

	rate := 0.0
	if uptime > 0 {
		rate = float64(publishes) / uptime
	}

	p50, p95, p99, max := m.percentilesUS()

	return MetricsSnapshot{
		UptimeSec:       uptime,
		Subscribers:     subscribers,
		Publishes:       publishes,
		Deliveries:      m.deliveries.Load(),
		FilterRejects:   m.filterRejects.Load(),
		Evictions:       m.evictions.Load(),
		BytesQueued:     m.bytesQueued.Load(),
		PublishesPerSec: rate,
		PublishUsP50:    p50,
		PublishUsP95:    p95,
		PublishUsP99:    p99,
		PublishUsMax:    max,
	}
}

// percentilesUS computes p50/p95/p99/max over the recent-publish
// duration ring (metricsRingSize most recent samples), converted from
// nanoseconds to microseconds. Returns zeros if no samples yet.
//
// Sort-on-read is fine here: /api/metrics isn't on a hot path, and
// metricsRingSize is small enough that an O(N log N) sort is
// microseconds. Trades read CPU for write simplicity (no maintained
// order on insert).
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
