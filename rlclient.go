package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
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

// RLClient connects to Rocket League's Stats API over TCP, decodes the
// stream of JSON packets, and feeds them into the EventBus via a small
// outbox channel.
//
// The TCP read loop never blocks on the bus: it pushes onto outbox and
// returns to reading. A separate dispatcher drains outbox into the bus.
// If outbox fills, the read loop drops the packet — keeping the kernel
// receive buffer drained matters more than delivering every frame, because
// a stalled read is what causes RL itself to freeze (its exporter blocks
// on send() into our full receive buffer).
type RLClient struct {
	addr string
	bus  *EventBus

	// lifecycle, if set, is fed every packet before bus.Publish so it can
	// drive the gameplay-state machine without re-subscribing.
	lifecycle *LifecycleTracker

	mu     sync.RWMutex
	status RLStatus

	outbox  chan []byte
	dropLog rateLimitedLogger

	// lastDialMsg is the most recently logged dial-failure message.
	// We log a new failure only when the explanation changes (e.g.
	// "connection refused" → "no such host") instead of repeating the
	// same line on every retry. A successful connect resets it so the
	// next failure logs again.
	lastDialMsg string
}

func NewRLClient(addr string, bus *EventBus) *RLClient {
	return &RLClient{
		addr:    addr,
		bus:     bus,
		status:  StatusDisconnected,
		outbox:  make(chan []byte, outboxBufSize),
		dropLog: rateLimitedLogger{interval: dropLogInterval},
	}
}

// AttachLifecycle wires a LifecycleTracker so it observes every packet
// the dispatcher publishes. Call before Run.
func (c *RLClient) AttachLifecycle(t *LifecycleTracker) { c.lifecycle = t }

func (c *RLClient) Status() RLStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// dispatcher drains outbox into the bus serially, so the publish path is
// naturally serialized and the read loop never has to wait on bus internals.
func (c *RLClient) dispatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.outbox:
			if !ok {
				return
			}
			if c.lifecycle != nil {
				c.lifecycle.Feed(msg)
			}
			c.bus.Publish(msg)
		}
	}
}

// enqueue is the only path from RL packets into the bus. Non-blocking by
// design — drops on full rather than backpressuring the read loop.
func (c *RLClient) enqueue(msg []byte) {
	select {
	case c.outbox <- msg:
	default:
		c.dropLog.log("[rl-api] outbox full — dropping packets (downstream is too slow)")
	}
}

func (c *RLClient) setStatus(s RLStatus) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
	// Status changes are rare and important — block briefly if the bus
	// is congested but never longer than statusPushTimeout, so a wedged
	// dispatcher can't pin the reconnect path either.
	select {
	case c.outbox <- newStatusEvent(s):
	case <-time.After(statusPushTimeout):
	}
}

func (c *RLClient) Run(ctx context.Context) {
	go c.dispatcher(ctx)

	first := true
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.setStatus(StatusConnecting)
		// Only announce "connecting to ..." on the first attempt. On
		// retry loops the user already saw the failure message; another
		// "connecting" line would just add noise.
		if first {
			log.Printf("[rl-api] connecting to %s", c.addr)
			first = false
		}

		dialer := net.Dialer{Timeout: dialTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			c.setStatus(StatusDisconnected)
			// User-actionable message for the common case (RL not
			// running, or PacketSendRate=0 in DefaultStatsAPI.ini)
			// instead of leaking the raw syscall error. Log only when
			// the explanation changes — repeating the same "RL isn't
			// running" line every 5s just clutters the terminal until
			// the user starts the game.
			msg := reasonForDialFailure(err, c.addr)
			if msg != c.lastDialMsg {
				log.Println(msg)
				c.lastDialMsg = msg
			}
			if !sleepCtx(ctx, dialTimeout) {
				return
			}
			continue
		}

		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(tcpReadBuffer)
			_ = tcp.SetNoDelay(true)
		}

		c.setStatus(StatusConnected)
		log.Printf("[rl-api] connected to %s", c.addr)
		// Forget the prior failure message so a future failure logs
		// again — useful when RL goes down mid-session and we want
		// users to see the new dial error rather than have it
		// silenced as a "duplicate."
		c.lastDialMsg = ""
		// Reset the drop-log timer so a subsequent disconnect logs
		// immediately instead of being silenced by the prior failure.
		c.dropLog.reset()
		c.readLoop(ctx, conn)
		_ = conn.Close()
		c.setStatus(StatusDisconnected)
		// Don't log on shutdown — the "[server] shutting down" line
		// already explains why we disconnected; an extra "disconnected,
		// reconnecting" right after would be noise.
		if ctx.Err() == nil {
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
func (c *RLClient) readLoop(ctx context.Context, conn net.Conn) {
	dec := json.NewDecoder(conn)

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
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				log.Printf("[rl-api] silent for %s — reconnecting", rlIdleTimeout)
				return
			}
			log.Printf("[rl-api] Read error: %v", err)
			return
		}
		c.enqueue(raw)
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
