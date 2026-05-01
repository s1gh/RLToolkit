package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
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

	mu     sync.RWMutex
	status RLStatus

	outbox  chan []byte
	dropLog rateLimitedLogger
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

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.setStatus(StatusConnecting)
		log.Printf("[rl-api] Connecting to %s …", c.addr)

		dialer := net.Dialer{Timeout: dialTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			c.setStatus(StatusDisconnected)
			log.Printf("[rl-api] Failed: %v  (retry in %s)", err, dialTimeout)
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
		log.Printf("[rl-api] Connected!")
		c.readLoop(ctx, conn)
		_ = conn.Close()
		c.setStatus(StatusDisconnected)
		log.Printf("[rl-api] Disconnected — reconnecting in %s …", reconnectDelay)

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
