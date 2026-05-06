package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// synthAdapter bridges the legacy Synthesizer.Feed into the pipeline.
// Acts as the synth's Broadcaster too — emissions land in `pending`
// instead of going straight to the bus, so they flow back through the
// pipeline where downstream emit processors can consume them.
//
// The synth only handles raw RL events, so we skip everything that
// starts with "_" (the synthetic events emitted by other processors).
// Without that guard, synth.Feed would fire its name-extract +
// dispatch on every chained emission only to fall through. Cheap but
// pointless.
//
// Pipeline.Run is single-threaded, so a single shared buffer here is
// safe — Process is never re-entered concurrently.
type synthAdapter struct {
	s       *Synthesizer
	pending []Event
}

func (a *synthAdapter) Broadcast(evt Event) { a.pending = append(a.pending, evt) }

func (a *synthAdapter) Process(evt Event) []Event {
	if len(evt.Name) > 0 && evt.Name[0] == '_' {
		return nil
	}
	a.pending = a.pending[:0]
	a.s.Feed(evt.Raw)
	if len(a.pending) == 0 {
		return nil
	}
	out := make([]Event, len(a.pending))
	copy(out, a.pending)
	return out
}

func main() {
	// Subcommand dispatch: anything that isn't `serve` (or empty) is a tool.
	// `serve` is also the implicit default so a bare `rl-toolkit` keeps
	// behaving like before.
	if len(os.Args) > 1 && !isFlag(os.Args[1]) {
		switch os.Args[1] {
		case "serve":
			os.Args = append(os.Args[:1], os.Args[2:]...) // strip subcommand
		case "new":
			os.Exit(runNew(os.Args[2:]))
		case "help", "-h", "--help":
			printUsage(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			printUsage(os.Stderr)
			os.Exit(2)
		}
	}
	runServe()
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func printUsage(w *os.File) {
	fmt.Fprint(w, `rl-toolkit — Rocket League stats SDK + overlay host

Usage:
  rl-toolkit [serve] [flags]   Run the server (default).
  rl-toolkit new <name>        Scaffold a new plugin in plugins/<name>.
  rl-toolkit help              Show this message.

Server flags (serve):
  -rl-addr  host:port for RL Stats API (default 127.0.0.1:49123)
  -port     HTTP port (default 49200)
  -plugins  plugin directory (default <exe-dir>/plugins)
  -data     data directory (default <exe-dir>/data)
`)
}

func runServe() {
	cfg := parseFlags()
	log.SetFlags(log.Ltime)
	log.SetOutput(&styledLogWriter{w: os.Stderr})

	// Print the welcome + setup banner first so the user sees a clean
	// intro before any timestamped operational log lines start.
	printStartupBanner(cfg)
	printRLSetupNotice()
	printLogsSectionHeader()

	store, err := NewDataStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}

	bus := NewBus()
	pm := NewPluginManager(cfg.PluginDir)
	source := NewRLSource(cfg.RLAddr)
	roster := NewRosterTracker(bus)
	matchState := NewMatchState()
	matchState.AttachBroadcaster(bus)
	correlation := NewCorrelationBuffer(32)
	tickStore := NewTickStore()
	synthBridge := &synthAdapter{}
	synth := NewSynthesizer(synthBridge, roster, correlation, tickStore)
	synthBridge.s = synth
	synth.AttachMatchState(matchState)
	discoveries := NewStatfeedDiscoveryStore(cfg.DataDir)
	synth.AttachDiscoveryStore(discoveries)

	// Stage 5 wiring: events flow RLSource → Pipeline → Bus, and the
	// legacy Synthesizer is still in the loop as one big EmitProcessor
	// (synthAdapter) that captures every emission instead of publishing
	// directly. Downstream emitters registered after synthAdapter can
	// therefore consume its synthetic events as inputs — this is what
	// lets emit_fastest_shot.go see the _BallHit / _GoalScored stream.
	pipe := NewPipeline()
	pipe.AddState(roster)
	pipe.AddState(matchState)
	pipe.AddState(tickStore)
	pipe.AddEmit(matchState)
	pipe.AddEmit(NewBallHitEmitter(roster, matchState, correlation))
	pipe.AddEmit(NewCrossbarEmitter(roster, tickStore))
	pipe.AddEmit(NewOwnGoalEmitter(matchState, tickStore, correlation))
	pipe.AddEmit(synthBridge)
	pipe.AddEmit(NewFastestShotEmitter())
	pipe.AddEmit(NewFirstBloodEmitter())

	overrides, err := NewOverridesStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}

	overrides.Notify = func() {
		// Re-snapshot the full overrides map and publish to subscribers.
		// Cheap: the file is tiny (one entry per plugin) and Notify only
		// fires on user-driven editor changes, not in any hot loop.
		// Note: GetAll re-acquires the store's RLock, so a concurrent
		// writer could land between the persist that triggered us and
		// this snapshot — meaning what we publish reflects the latest
		// state, not necessarily the write that fired us. Latest-wins
		// is the right semantics for live reflow.
		if env := marshalOverridesChanged(overrides.GetAll()); env != nil {
			bus.Broadcast(Event{Name: "_OverridesChanged", Raw: env})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &Server{bus: bus, store: store, plugins: pm, source: source, matchState: matchState, roster: roster, synth: synth, overrides: overrides, discoveries: discoveries, config: cfg}
	go source.Run(ctx)
	go pipe.Run(ctx, source, bus)
	go matchState.Run(ctx)
	// Periodically flush new Statfeed-name discoveries to disk. The store
	// itself is debounced (no-op when nothing changed), so a tight tick
	// is fine — every 5 seconds is well under any human-noticeable lag.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = discoveries.Flush()
				return
			case <-ticker.C:
				if err := discoveries.Flush(); err != nil {
					log.Printf("[discoveries] flush failed: %v", err)
				}
			}
		}
	}()

	hs := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: srv.routes(),
		// BaseContext makes every request's r.Context() a child of the
		// app's lifetime ctx. On shutdown we cancel ctx first so
		// long-lived handlers (notably the SSE stream) wake up via
		// <-r.Context().Done() instead of waiting for the shutdown
		// grace period to elapse — which used to manifest as
		// "shutdown: context deadline exceeded" on Ctrl+C.
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}
	go func() {
		if err := hs.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("[server] %v", err)
		}
	}()

	awaitSignal()
	log.Println("[server] shutting down")
	// Cancel the app ctx first. With BaseContext above, this cancels
	// every in-flight r.Context() — wakes up the SSE handlers so they
	// return promptly instead of waiting on the next event/heartbeat.
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), httpShutdown)
	defer shutCancel()
	if err := hs.Shutdown(shutCtx); err != nil {
		// "context deadline exceeded" here means an in-flight handler
		// didn't honor ctx cancellation in time. Log it as a warning
		// rather than a generic [server] line so users notice the
		// difference between a clean shutdown and a forced one.
		log.Printf("[server] forced shutdown after %s: %v", httpShutdown, err)
	}
}

// runNew handles `rl-toolkit new <name> [-plugins <dir>]`. Accepts the
// name in any position (before or after flags) so common typing patterns
// all work — Go's stdlib flag package stops at the first non-flag.
func runNew(args []string) int {
	exeDir := executableDir()
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	pluginDir := fs.String("plugins", filepath.Join(exeDir, "plugins"), "Plugin directory path")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rl-toolkit new <name> [-plugins <dir>]\n\n")
		fs.PrintDefaults()
	}

	flagArgs, positional := splitFlagsAndPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positional) < 1 {
		fs.Usage()
		return 2
	}
	if err := scaffoldPlugin(*pluginDir, positional[0]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// splitFlagsAndPositional separates a flat argv into flag tokens and bare
// positional arguments, preserving order within each group. Handles both
// `-flag value` and `-flag=value` forms.
func splitFlagsAndPositional(args []string) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			return
		}
		if !isFlag(a) {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		// `-flag=value` is self-contained; `-flag value` consumes the next token.
		if !strings.Contains(a, "=") && i+1 < len(args) && !isFlag(args[i+1]) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return
}

// parseFlags reads server CLI flags and resolves directories relative to
// the executable (not the cwd) so double-click-from-anywhere "just works".
func parseFlags() Config {
	exeDir := executableDir()
	rlAddr := flag.String("rl-addr", "127.0.0.1:49123", "RL Stats API address (host:port)")
	httpPort := flag.Int("port", 49200, "HTTP server port")
	pluginDir := flag.String("plugins", filepath.Join(exeDir, "plugins"), "Plugin directory path")
	dataDir := flag.String("data", filepath.Join(exeDir, "data"), "Data directory path")
	flag.Parse()
	return Config{
		RLAddr:    *rlAddr,
		HTTPPort:  *httpPort,
		PluginDir: *pluginDir,
		DataDir:   *dataDir,
	}
}

func executableDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// awaitSignal blocks until SIGINT/SIGTERM. After the first signal it
// returns so the main goroutine can run shutdown; a SECOND signal in
// the grace window force-exits, so an impatient Ctrl+C, Ctrl+C
// doesn't wait for the HTTP shutdown deadline.
func awaitSignal() {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "  forced exit")
		os.Exit(130) // 128 + SIGINT
	}()
}

// ANSI escape codes. Empty when the terminal can't render them, so
// the same code path produces clean plain-text in pipes / dumb terms.
var (
	cReset   = ansi("\x1b[0m")
	cDim     = ansi("\x1b[2m")
	cBold    = ansi("\x1b[1m")
	cCyan    = ansi("\x1b[36m")
	cAmber   = ansi("\x1b[33m")
	cMagenta = ansi("\x1b[35m")
	cGreen   = ansi("\x1b[32m")
	cAccent  = ansi("\x1b[1;36m") // bold cyan — for the wordmark
)

// ansi returns the given escape only when output is going to a real
// terminal that can render it. Honors the de-facto NO_COLOR convention
// (https://no-color.org) and TERM=dumb so log redirects produce clean
// text without escape garbage.
func ansi(seq string) string {
	if os.Getenv("NO_COLOR") != "" {
		return ""
	}
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return ""
	}
	if fi, err := os.Stderr.Stat(); err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return ""
	}
	return seq
}

// printStartupBanner is the first thing the user sees on launch:
// the URLs they actually need (dashboard, overlay) and the address
// the toolkit will listen for RL on. Printed to stderr without the
// log prefix so the URLs stand out cleanly above the timestamped
// operational logs that follow.
func printStartupBanner(cfg Config) {
	fmt.Fprintf(os.Stderr, "\n  %srl-toolkit%s %s· stats overlay host%s\n",
		cAccent, cReset, cDim, cReset)
	fmt.Fprintf(os.Stderr, "  %s───────────────────────────────────────%s\n\n",
		cDim, cReset)
	row := func(label, value string) {
		fmt.Fprintf(os.Stderr, "    %s%-11s%s  %s%s%s\n",
			cDim, label, cReset, cCyan, value, cReset)
	}
	row("Dashboard", fmt.Sprintf("http://localhost:%d", cfg.HTTPPort))
	row("Overlay",   fmt.Sprintf("http://localhost:%d/overlay", cfg.HTTPPort))
	row("Stats API", cfg.RLAddr)
	fmt.Fprintln(os.Stderr)
}

// printRLSetupNotice reminds the user that RL's Stats API is OFF by
// default. Without PacketSendRate > 0 in DefaultStatsAPI.ini, RL never
// opens the local socket and the toolkit will sit at "disconnected"
// forever. This is the single most common first-run failure, so we
// surface the requirement up-front instead of letting users debug it
// from "Connection refused" logs.
//
// Visual style: a left-edge accent bar (┃) instead of a heavy box;
// the bar carries the eye through the block without making it feel
// like a modal dialog.
func printRLSetupNotice() {
	bar := cAmber + "┃" + cReset
	line := func(s string) { fmt.Fprintf(os.Stderr, "  %s  %s\n", bar, s) }

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s  %sFirst-run setup%s %s· Rocket League Stats API%s\n",
		bar, cBold, cReset, cDim, cReset)
	line("")
	line(fmt.Sprintf("RL's Stats API is %sOFF by default%s. Enable it before starting the game:", cBold, cReset))
	line("")
	line(fmt.Sprintf("    %sedit%s  %s<RL Install>/TAGame/Config/DefaultStatsAPI.ini%s",
		cDim, cReset, cCyan, cReset))
	line("")
	line(fmt.Sprintf("    %sPacketSendRate%s = %s10%s     %s10 Hz; up to 120 supported%s",
		cBold, cReset, cCyan, cReset, cDim, cReset))
	line(fmt.Sprintf("    %sPort%s           = %s49123%s  %stoolkit's default%s",
		cBold, cReset, cCyan, cReset, cDim, cReset))
	line("")
	line(fmt.Sprintf("%sChanges only take effect on a fresh RL launch.%s", cDim, cReset))
	fmt.Fprintln(os.Stderr)
}

// printLogsSectionHeader is the visual hand-off between the static
// banner above and the live operational logs below. A small label
// over a dim hairline rule makes it unambiguous that what follows
// is runtime output, not more banner content.
func printLogsSectionHeader() {
	fmt.Fprintf(os.Stderr, "  %s%s%s %s──────────────────────────────────%s\n\n",
		cDim, "logs", cReset, cDim, cReset)
}

// styledLogWriter wraps stderr to colorize log lines emitted by the
// stdlib log package. Each line arrives as "HH:MM:SS [facet] message".
// We dim the timestamp, color the facet tag (one color per facet so
// they're scannable at a glance), and leave the message body plain.
//
// Falls back to passthrough when ansi() returned "" — same code path
// in pipes / NO_COLOR / dumb terminals, no escape garbage.
type styledLogWriter struct {
	w io.Writer
}

// facetColor maps log facets ("[bus]", "[plugins]", "[server]", …)
// to consistent colors so users can scan a busy log by hue. Add new
// facets here as they're introduced; unknown facets render dim, which
// reads as "uncategorized" without breaking layout.
var facetColor = map[string]string{
	"server":  cGreen,
	"rl-api":  cCyan,
	"plugins": cMagenta,
	"bus":     cAmber,
	"http":    cDim,
	"data":    cDim,
}

func (s *styledLogWriter) Write(p []byte) (int, error) {
	// Fast path: no color → straight passthrough. Keeps this writer
	// from re-allocating on every log line in non-TTY environments.
	if cReset == "" {
		return s.w.Write(p)
	}

	// Log entries can theoretically batch (rare for stdlib log, but
	// safe to handle) — split on '\n' and style each non-empty line.
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(p, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		s.styleLine(&out, line)
	}
	_, err := s.w.Write(out.Bytes())
	return len(p), err
}

// styleLine recognizes the stdlib log format with Ltime + a "[facet]"
// prefix. Anything that doesn't match is written through unchanged so
// non-conforming log calls (e.g. multi-line stack traces) don't get
// chopped up.
func (s *styledLogWriter) styleLine(out *bytes.Buffer, line []byte) {
	// Expect: "HH:MM:SS [facet] message\n" — bail to passthrough on
	// any deviation. Length floor: 8 (time) + 1 (space) + 3 ([x]).
	if len(line) < 12 || line[2] != ':' || line[5] != ':' || line[8] != ' ' || line[9] != '[' {
		out.Write(line)
		return
	}
	close := bytes.IndexByte(line[9:], ']')
	if close < 0 {
		out.Write(line)
		return
	}
	tsEnd := 8                    // index just past "HH:MM:SS"
	facet := string(line[10 : 9+close]) // contents between '[' and ']'
	color := facetColor[facet]
	if color == "" {
		color = cDim
	}
	rest := line[9+close+1:] // " message\n"

	// Dim timestamp, colored [facet], plain message body.
	out.WriteString(cDim)
	out.Write(line[:tsEnd])
	out.WriteString(cReset)
	out.WriteByte(' ')
	out.WriteString(color)
	out.WriteByte('[')
	out.WriteString(facet)
	out.WriteByte(']')
	out.WriteString(cReset)
	out.Write(rest)
}
