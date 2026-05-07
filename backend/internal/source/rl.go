package source

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/roster"
	"rl-toolkit/backend/internal/wire"
	"strings"
	"sync"
	"time"
)

// Status is the connection state surfaced to dashboard / SSE clients.
type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
)

// connectionStatusEvent is the SSE event payload broadcast on every
// connection-state transition. Mirrored on the client side.
type connectionStatusEvent struct {
	Event  string `json:"Event"`
	Status Status `json:"Status"`
}

// StatusEventBytes builds the JSON envelope the source pushes onto
// the bus on every connection-state transition. Exported so the SSE
// handler can use the same shape for the initial-frame push it sends
// to a freshly-connected subscriber.
func StatusEventBytes(s Status) []byte {
	b, _ := json.Marshal(connectionStatusEvent{Event: "_ConnectionStatus", Status: s})
	return b
}

// RL connects to Rocket League's Stats API over TCP, decodes the stream
// of JSON packets, and surfaces them as a channel of Events for
// pipeline.Run to consume.
//
// The TCP read loop never blocks on consumers: it pushes onto out and
// returns to reading. If out fills, the read loop drops the packet —
// keeping the kernel receive buffer drained matters more than
// delivering every frame, because a stalled read is what causes RL
// itself to freeze (its exporter blocks on send() into our full
// receive buffer).
type RL struct {
	addr string

	out chan bus.Event

	mu     sync.RWMutex
	status Status

	dropLog rateLimitedLogger
	idleLog rateLimitedLogger

	lastDialMsg   string
	selfReconnect bool
}

// NewRL creates an RL source pointing at addr (host:port).
func NewRL(addr string) *RL {
	return &RL{
		addr:    addr,
		out:     make(chan bus.Event, outboxBufSize),
		status:  StatusDisconnected,
		dropLog: rateLimitedLogger{interval: dropLogInterval},
		idleLog: rateLimitedLogger{interval: idleLogInterval},
	}
}

// Events returns the event channel. Run must be invoked separately for
// events to start flowing. The channel stays open across reconnect
// cycles.
func (s *RL) Events(ctx context.Context) <-chan bus.Event { return s.out }

// Status returns the current connection state.
func (s *RL) Status() Status {
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
func (s *RL) enqueue(msg []byte) {
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
		msg = roster.RewriteUpdateStateBotIds(msg)
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

func (s *RL) setStatus(st Status) {
	s.mu.Lock()
	prev := s.status
	s.status = st
	s.mu.Unlock()
	if prev == st {
		return
	}
	body := StatusEventBytes(st)
	select {
	case s.out <- bus.Event{Name: "_ConnectionStatus", Raw: body}:
	case <-time.After(statusPushTimeout):
	}
}

// Run dials RL and enters a reconnect loop. Returns when ctx is done.
func (s *RL) Run(ctx context.Context) {
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.setStatus(StatusConnecting)
		if first {
			log.Printf("[rl-api] connecting to %s", s.addr)
			first = false
		}

		dialer := net.Dialer{Timeout: dialTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", s.addr)
		if err != nil {
			s.setStatus(StatusDisconnected)
			msg := reasonForDialFailure(err, s.addr)
			if msg != s.lastDialMsg {
				log.Println(msg)
				s.lastDialMsg = msg
			}
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
		if !s.selfReconnect {
			log.Printf("[rl-api] connected to %s", s.addr)
		}
		s.lastDialMsg = ""
		s.dropLog.reset()
		s.readLoop(ctx, conn)
		_ = conn.Close()
		s.setStatus(StatusDisconnected)
		if ctx.Err() == nil && !s.selfReconnect {
			log.Printf("[rl-api] disconnected; reconnecting in %s", reconnectDelay)
		}

		if !sleepCtx(ctx, reconnectDelay) {
			return
		}
	}
}

// readLoop decodes packets from a single connection until the context
// is canceled, the peer hangs up, or rlIdleTimeout elapses without
// traffic.
//
// The idle timeout exists because RL's exporter sometimes silently
// stops emitting on an existing connection (especially after a match
// ends + long menu idle) while leaving the TCP socket alive — but it
// always emits to a fresh client. So treat prolonged silence as
// "stale, reconnect" rather than waiting forever for data that won't
// come.
func (s *RL) readLoop(ctx context.Context, conn net.Conn) {
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
				s.selfReconnect = false
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				s.idleLog.log("[rl-api] silent for " + rlIdleTimeout.String() + " — reconnecting")
				s.selfReconnect = true
				return
			}
			log.Printf("[rl-api] Read error: %v", err)
			s.idleLog.reset()
			s.selfReconnect = false
			return
		}
		if !gotTraffic {
			gotTraffic = true
			s.idleLog.reset()
			s.selfReconnect = false
		}
		s.enqueue(raw)
	}
}

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
