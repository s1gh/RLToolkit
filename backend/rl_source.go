package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/wire"
	"strings"
	"sync"
	"time"
)

// RLStatus is the connection state surfaced to dashboard / SSE clients.
type RLStatus string

const (
	StatusDisconnected RLStatus = "disconnected"
	StatusConnecting   RLStatus = "connecting"
	StatusConnected    RLStatus = "connected"
)

// connectionStatusEvent is the SSE event payload broadcast on every
// connection-state transition. Mirrored on the client side; if you change
// field names update the SDK and dashboard too.
type connectionStatusEvent struct {
	Event  string   `json:"Event"`
	Status RLStatus `json:"Status"`
}

func newStatusEvent(s RLStatus) []byte {
	b, _ := json.Marshal(connectionStatusEvent{Event: "_ConnectionStatus", Status: s})
	return b
}

// RLSource connects to Rocket League's Stats API over TCP, decodes the
// stream of JSON packets, and surfaces them as a channel of Events for
// Pipeline.Run to consume.
//
// The TCP read loop never blocks on consumers: it pushes onto out and
// returns to reading. If out fills, the read loop drops the packet —
// keeping the kernel receive buffer drained matters more than delivering
// every frame, because a stalled read is what causes RL itself to freeze
// (its exporter blocks on send() into our full receive buffer).
type RLSource struct {
	addr string

	// out carries decoded events to whoever called Events(). Buffered so
	// the TCP read loop can stay non-blocking; on full, packets are
	// dropped (logged via dropLog).
	out chan bus.Event

	mu     sync.RWMutex
	status RLStatus

	dropLog rateLimitedLogger

	// idleLog rate-limits the "silent for 30s — reconnecting" line and
	// its successor "connected to ..." so a long menu/idle stretch
	// (which cycles the connection every 30s by design) doesn't fill
	// the terminal. Reset when real packets arrive or a dial fails.
	idleLog rateLimitedLogger

	// lastDialMsg is the most recently logged dial-failure message.
	// We log a new failure only when the explanation changes (e.g.
	// "connection refused" → "no such host") instead of repeating the
	// same line on every retry. A successful connect resets it so the
	// next failure logs again.
	lastDialMsg string

	// selfReconnect is set when the previous readLoop returned because
	// of our own idle timeout, NOT because the peer disconnected or
	// errored. The reconnect path uses it to suppress the redundant
	// "disconnected; reconnecting in 500ms" line (the "silent for 30s"
	// line already explains the cycle) and to rate-limit the follow-up
	// "connected to ..." confirmation.
	selfReconnect bool
}

func NewRLSource(addr string) *RLSource {
	return &RLSource{
		addr:    addr,
		out:     make(chan bus.Event, outboxBufSize),
		status:  StatusDisconnected,
		dropLog: rateLimitedLogger{interval: dropLogInterval},
		idleLog: rateLimitedLogger{interval: idleLogInterval},
	}
}

// Events returns the event channel. Run must be invoked separately for
// events to start flowing. The channel stays open across reconnect
// cycles — closing it would force every downstream pipeline to handle
// reopen, but the source keeps trying as long as Run's ctx is alive.
func (s *RLSource) Events(ctx context.Context) <-chan bus.Event { return s.out }

func (s *RLSource) Status() RLStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// enqueue is the only path from RL packets into the event channel.
// Non-blocking by design — drops on full rather than backpressuring the
// read loop.
//
// Canonicalizes bot ids on UpdateState envelopes here so every consumer
// (raw bus subscribers and pipeline state processors alike) sees the
// rewritten payload. The wire ships every bot under the "Unknown|0|0"
// sentinel; without rewriting, downstream code that reads raw
// UpdateState would collapse multiple bots into one player.
func (s *RLSource) enqueue(msg []byte) {
	var env struct {
		EventPascal string          `json:"Event"`
		EventLower  string          `json:"event"`
		Data        json.RawMessage `json:"Data,omitempty"`
		DataLower   json.RawMessage `json:"data,omitempty"`
	}
	_ = json.Unmarshal(msg, &env)
	rawName := env.EventPascal
	if rawName == "" {
		rawName = env.EventLower
	}
	name := wire.Canonical([]byte(rawName))
	if name == "UpdateState" {
		msg = rewriteUpdateStateBotIds(msg)
		// rewriteUpdateStateBotIds may rebuild the envelope; re-pull
		// Data so the channel sees the post-rewrite bytes.
		_ = json.Unmarshal(msg, &env)
	}
	data := env.Data
	if len(data) == 0 {
		data = env.DataLower
	}
	select {
	case s.out <- bus.Event{Name: name, Data: data, Raw: msg}:
	default:
		s.dropLog.log("[rl-api] event channel full — dropping packets (downstream is too slow)")
	}
}

func (s *RLSource) setStatus(st RLStatus) {
	s.mu.Lock()
	prev := s.status
	s.status = st
	s.mu.Unlock()
	if prev == st {
		return
	}
	// Surface status changes as a synthetic _ConnectionStatus event on
	// the same channel as RL traffic. Same shape the legacy bus used,
	// so existing SSE consumers don't notice the difference.
	body := newStatusEvent(st)
	select {
	case s.out <- bus.Event{Name: "_ConnectionStatus", Raw: body}:
	case <-time.After(statusPushTimeout):
	}
}

func (s *RLSource) Run(ctx context.Context) {
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.setStatus(StatusConnecting)
		// Only announce "connecting to ..." on the first attempt. On
		// retry loops the user already saw the failure message; another
		// "connecting" line would just add noise.
		if first {
			log.Printf("[rl-api] connecting to %s", s.addr)
			first = false
		}

		dialer := net.Dialer{Timeout: dialTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", s.addr)
		if err != nil {
			s.setStatus(StatusDisconnected)
			// User-actionable message for the common case (RL not
			// running, or PacketSendRate=0 in DefaultStatsAPI.ini)
			// instead of leaking the raw syscall error. Log only when
			// the explanation changes — repeating the same "RL isn't
			// running" line every 5s just clutters the terminal until
			// the user starts the game.
			msg := reasonForDialFailure(err, s.addr)
			if msg != s.lastDialMsg {
				log.Println(msg)
				s.lastDialMsg = msg
			}
			// A dial failure is a real change of state — break out of
			// the silent-idle-cycle pattern so the next successful
			// connect (and the next idle reconnect) log normally.
			s.idleLog.reset()
			s.selfReconnect = false
			if !sleepCtx(ctx, dialTimeout) {
				return
			}
			continue
		}

		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(tcpReadBuffer)
			_ = tcp.SetNoDelay(true)
		}

		s.setStatus(StatusConnected)
		// On a self-induced reconnect (idle-timeout cycle while RL is
		// in menus/lobby) the connection comes back instantly and the
		// "connected" line is redundant with the "silent for 30s"
		// line just above it. Suppress entirely during a self-reconnect
		// cycle — when real traffic resumes, the cycle ends and the
		// next genuine reconnect logs normally.
		if !s.selfReconnect {
			log.Printf("[rl-api] connected to %s", s.addr)
		}
		// Forget the prior failure message so a future failure logs
		// again — useful when RL goes down mid-session and we want
		// users to see the new dial error rather than have it
		// silenced as a "duplicate."
		s.lastDialMsg = ""
		// Reset the drop-log timer so a subsequent disconnect logs
		// immediately instead of being silenced by the prior failure.
		s.dropLog.reset()
		s.readLoop(ctx, conn)
		_ = conn.Close()
		s.setStatus(StatusDisconnected)
		// Don't log on shutdown — the "[server] shutting down" line
		// already explains why we disconnected; an extra "disconnected,
		// reconnecting" right after would be noise.
		//
		// Also skip when readLoop ended because of our own idle
		// timeout: the "silent for 30s — reconnecting" line that
		// readLoop printed already explains the cycle. Without this
		// check, every idle cycle prints two lines for the same
		// event.
		if ctx.Err() == nil && !s.selfReconnect {
			log.Printf("[rl-api] disconnected; reconnecting in %s", reconnectDelay)
		}

		if !sleepCtx(ctx, reconnectDelay) {
			return
		}
	}
}

// readLoop decodes packets from a single connection until the context is
// canceled, the peer hangs up, or rlIdleTimeout elapses without traffic.
//
// The idle timeout exists because RL's exporter sometimes silently stops
// emitting on an existing connection (especially after a match ends + long
// menu idle) while leaving the TCP socket alive — but it always emits to
// a fresh client. So treat prolonged silence as "stale, reconnect" rather
// than waiting forever for data that won't come.
func (s *RLSource) readLoop(ctx context.Context, conn net.Conn) {
	dec := json.NewDecoder(conn)
	gotTraffic := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(rlIdleTimeout))

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				// Peer-side disconnect — different signal from our own
				// idle timeout. Treat it as a real cycle break.
				s.selfReconnect = false
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// Self-induced reconnect. Rate-limit the line so a long
				// menu stretch logs once, not every 30 seconds. The
				// successor "connected to ..." line is gated by the
				// same limiter in Run().
				s.idleLog.log("[rl-api] silent for " + rlIdleTimeout.String() + " — reconnecting")
				s.selfReconnect = true
				return
			}
			// Anything else (network error, malformed JSON, ...) is a
			// real failure mode — break the quiet cycle so the dial /
			// connect path logs normally on retry.
			log.Printf("[rl-api] Read error: %v", err)
			s.idleLog.reset()
			s.selfReconnect = false
			return
		}
		// First real packet on this connection — RL is back to actively
		// streaming, so the idle cycle (if any) is over. Reset the
		// limiter so the next idle stretch logs fresh.
		if !gotTraffic {
			gotTraffic = true
			s.idleLog.reset()
			s.selfReconnect = false
		}
		s.enqueue(raw)
	}
}

// sleepCtx waits for d or until ctx is canceled. Returns false if canceled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// reasonForDialFailure turns a raw net dial error into a one-line
// human-readable explanation. The vast majority of localhost dial
// failures here mean exactly one thing — RL isn't running with the
// Stats API enabled — so we say that plainly instead of pasting the
// Go stdlib error string the user can't do anything with.
func reasonForDialFailure(err error, addr string) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "[rl-api] " + addr + " is not accepting connections — is RL running with PacketSendRate>0?"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return "[rl-api] timed out reaching " + addr + " — check the address (is RL on a different host?)"
	case strings.Contains(msg, "no such host"):
		return "[rl-api] cannot resolve " + addr + " — check the host part of -rl-addr"
	default:
		return "[rl-api] connect failed: " + msg
	}
}

// rateLimitedLogger emits at most one log line per `interval`. Used for
// drop logging on the 60Hz hot path so we know dropping is happening
// without spamming at the packet rate.
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
// immediately. Useful after a state change (reconnect succeeded,
// match started) where we don't want a stale "still failing" timer
// to swallow the first new failure.
func (r *rateLimitedLogger) reset() {
	r.mu.Lock()
	r.last = time.Time{}
	r.mu.Unlock()
}
