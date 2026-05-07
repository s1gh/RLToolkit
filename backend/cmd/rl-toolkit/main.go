// Command rl-toolkit is the binary entry point. Wires the
// internal packages together (pipeline + bus + sources + emit
// processors + HTTP server) and runs the serve loop.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/correlation"
	"rl-toolkit/backend/internal/datastore"
	"rl-toolkit/backend/internal/devapi"
	"rl-toolkit/backend/internal/discoveries"
	"rl-toolkit/backend/internal/emit"
	"rl-toolkit/backend/internal/identity"
	"rl-toolkit/backend/internal/overrides"
	"rl-toolkit/backend/internal/pipeline"
	"rl-toolkit/backend/internal/plugins"
	"rl-toolkit/backend/internal/roster"
	"rl-toolkit/backend/internal/scaffold"
	"rl-toolkit/backend/internal/server"
	"rl-toolkit/backend/internal/source"
	"rl-toolkit/backend/internal/state"
	"rl-toolkit/backend/internal/tick"
	"strings"
	"syscall"
	"time"
)

// Config is the runtime configuration assembled from CLI flags.
type Config struct {
	RLAddr    string
	HTTPPort  int
	PluginDir string
	DataDir   string
}

// httpShutdown bounds how long http.Shutdown waits for in-flight
// handlers to honor ctx cancellation. SSE handlers wake from
// r.Context().Done() inside ~ms, so this is a generous safety net for
// any non-SSE handler that's mid-write.
const httpShutdown = 2 * time.Second

func main() {
	// Subcommand dispatch: anything that isn't `serve` (or empty) is a
	// tool. `serve` is also the implicit default so a bare
	// `rl-toolkit` keeps behaving like before.
	if len(os.Args) > 1 && !isFlag(os.Args[1]) {
		switch os.Args[1] {
		case "serve":
			os.Args = append(os.Args[:1], os.Args[2:]...)
		case "new":
			os.Exit(runNew(os.Args[2:]))
		case "pack":
			os.Exit(runPack(os.Args[2:]))
		case "install":
			os.Exit(runInstall(os.Args[2:]))
		case "uninstall":
			os.Exit(runUninstall(os.Args[2:]))
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
  rl-toolkit pack [path]       Zip a plugin folder into <name>-<version>.rltp.
  rl-toolkit install <file>    Install a .rltp into the plugins directory.
  rl-toolkit uninstall <name>  Remove an installed plugin.
  rl-toolkit help              Show this message.

Server flags (serve):
  -rl-addr  host:port for RL Stats API (default 127.0.0.1:49123)
  -port     HTTP port (default 49200)
  -plugins  plugin directory (default <exe-dir>/plugins)
  -data     data directory (default <exe-dir>/data)
`)
}

// identityAdapter bridges *identity.Store to roster.IdentityLookup —
// the consumer-side interface declares MyPrimaryID(), the store has
// Get() returning a richer *Identity. Adapter does the unwrap.
type identityAdapter struct{ s *identity.Store }

func (a identityAdapter) MyPrimaryID() string {
	if a.s == nil {
		return ""
	}
	id := a.s.Get()
	if id == nil {
		return ""
	}
	return id.PrimaryID
}

func runServe() {
	cfg := parseFlags()
	log.SetFlags(log.Ltime)
	log.SetOutput(&styledLogWriter{w: os.Stderr})

	printStartupBanner(cfg)
	printRLSetupNotice()
	printLogsSectionHeader()

	store, err := datastore.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}

	eventBus := bus.NewBus()
	pm := plugins.New(cfg.PluginDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	devSrv, err := devapi.Start(ctx, pm, cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] devapi start: %v", err)
	}
	defer devSrv.Stop()
	log.Printf("[devapi] listening on %s (port written to %s/dev.port)", devSrv.Addr(), cfg.DataDir)

	src := source.NewRL(cfg.RLAddr)
	rt := roster.New()
	matchState := state.New()
	matchState.AttachBroadcaster(eventBus)
	corr := correlation.New(32)
	tickStore := tick.New()
	disc := discoveries.New(cfg.DataDir)
	ownGoal := emit.NewOwnGoal(matchState, tickStore, corr)
	statfeed := emit.NewStatfeed(rt, corr, disc, ownGoal)
	demos := emit.NewDemos(tickStore)
	goalEmit := emit.NewGoal(rt, corr, tickStore, statfeed, ownGoal)
	tickDiff := emit.NewTickDiff(matchState, tickStore, corr, statfeed)
	matchEnded := emit.NewMatchEnded(tickStore)

	// Pipeline wiring: events flow source → pipeline → bus.
	//
	// State processors update shared snapshots (roster, matchState,
	// tickStore) before any emit processor runs. Emit processors are
	// registered in dependency order so producers fire before
	// consumers — see pipeline.runEmit for the strictly-forward
	// chaining rule.
	pipe := pipeline.New()
	pipe.AddState(rt)
	pipe.AddState(matchState)
	pipe.AddState(tickStore)

	// Roster + MatchState are also emit processors for the synthetic
	// events they own (_RosterChanged / _MatchState).
	pipe.AddEmit(rt)
	pipe.AddEmit(matchState)

	// Wire-spec republishers: enrich raw RL events with resolved
	// players and pre-decoded fields.
	pipe.AddEmit(emit.NewBallHit(rt, matchState, corr))
	pipe.AddEmit(emit.NewCrossbar(rt, tickStore))
	pipe.AddEmit(goalEmit)
	pipe.AddEmit(matchEnded)

	// State-derived events that depend on the producers above.
	pipe.AddEmit(ownGoal)
	pipe.AddEmit(statfeed)
	pipe.AddEmit(demos)
	pipe.AddEmit(tickDiff)

	// Per-match milestones consume upstream emissions (_BallHit /
	// _GoalScored).
	pipe.AddEmit(emit.NewFastestShot())
	pipe.AddEmit(emit.NewFirstBlood())

	idStore, err := identity.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}
	rt.AttachIdentity(identityAdapter{s: idStore})
	idStore.Notify = func(id *identity.Identity) {
		body, err := json.Marshal(id)
		if err != nil {
			return
		}
		eventBus.Broadcast(bus.Event{Name: "_IdentityChanged", Data: body})
	}

	ovr, err := overrides.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}

	ovr.Notify = func() {
		// Re-snapshot the full overrides map and publish to
		// subscribers. Cheap: the file is tiny (one entry per plugin)
		// and Notify only fires on user-driven editor changes, not
		// in any hot loop.
		if env := server.MarshalOverridesChanged(ovr.GetAll()); env != nil {
			eventBus.Broadcast(bus.Event{Name: "_OverridesChanged", Raw: env})
		}
	}

	srv := server.New(server.Deps{
		Bus:         eventBus,
		Store:       store,
		Plugins:     pm,
		Source:      src,
		MatchState:  matchState,
		Roster:      rt,
		Demos:       demos,
		Overrides:   ovr,
		Discoveries: disc,
		Identity:    idStore,
		PluginDir:   cfg.PluginDir,
	})
	go src.Run(ctx)
	go pipe.Run(ctx, src, eventBus)
	go matchState.Run(ctx)
	// Periodically flush new Statfeed-name discoveries to disk. The
	// store itself is debounced (no-op when nothing changed), so a
	// tight tick is fine — every 5 seconds is well under any
	// human-noticeable lag.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = disc.Flush()
				return
			case <-ticker.C:
				if err := disc.Flush(); err != nil {
					log.Printf("[discoveries] flush failed: %v", err)
				}
			}
		}
	}()

	hs := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: srv.Routes(),
		// BaseContext makes every request's r.Context() a child of
		// the app's lifetime ctx. On shutdown we cancel ctx first so
		// long-lived handlers (notably the SSE stream) wake up via
		// <-r.Context().Done() instead of waiting for the shutdown
		// grace period to elapse.
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}
	go func() {
		if err := hs.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("[server] %v", err)
		}
	}()

	awaitSignal()
	log.Println("[server] shutting down")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), httpShutdown)
	defer shutCancel()
	if err := hs.Shutdown(shutCtx); err != nil {
		log.Printf("[server] forced shutdown after %s: %v", httpShutdown, err)
	}
}

// runNew handles `rl-toolkit new <name> [-plugins <dir>]`. Accepts
// the name in any position (before or after flags) so common typing
// patterns all work — Go's stdlib flag package stops at the first
// non-flag.
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
	if err := scaffold.Plugin(*pluginDir, positional[0]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// splitFlagsAndPositional separates a flat argv into flag tokens and
// bare positional arguments, preserving order within each group.
// Handles both `-flag value` and `-flag=value` forms.
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
		if !strings.Contains(a, "=") && i+1 < len(args) && !isFlag(args[i+1]) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return
}

// parseFlags reads server CLI flags and resolves directories relative
// to the executable (not the cwd) so double-click-from-anywhere "just
// works".
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
// the same code path produces clean plain-text in pipes / dumb
// terms.
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
// terminal that can render it. Honors the de-facto NO_COLOR
// convention (https://no-color.org) and TERM=dumb so log redirects
// produce clean text without escape garbage.
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

// printStartupBanner is the first thing the user sees on launch: the
// URLs they actually need (dashboard, overlay) and the address the
// toolkit will listen for RL on.
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
	row("Overlay", fmt.Sprintf("http://localhost:%d/overlay", cfg.HTTPPort))
	row("Stats API", cfg.RLAddr)
	fmt.Fprintln(os.Stderr)
}

// printRLSetupNotice reminds the user that RL's Stats API is OFF by
// default. Without PacketSendRate > 0 in DefaultStatsAPI.ini, RL
// never opens the local socket and the toolkit will sit at
// "disconnected" forever.
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
// banner above and the live operational logs below.
func printLogsSectionHeader() {
	fmt.Fprintf(os.Stderr, "  %s%s%s %s──────────────────────────────────%s\n\n",
		cDim, "logs", cReset, cDim, cReset)
}

// styledLogWriter wraps stderr to colorize log lines emitted by the
// stdlib log package. Each line arrives as "HH:MM:SS [facet] message".
// We dim the timestamp, color the facet tag (one color per facet so
// they're scannable at a glance), and leave the message body plain.
type styledLogWriter struct {
	w io.Writer
}

// facetColor maps log facets to consistent colors so users can scan
// a busy log by hue. Add new facets here as they're introduced;
// unknown facets render dim, which reads as "uncategorized" without
// breaking layout.
var facetColor = map[string]string{
	"server":  cGreen,
	"rl-api":  cCyan,
	"plugins": cMagenta,
	"bus":     cAmber,
	"http":    cDim,
	"data":    cDim,
}

func (s *styledLogWriter) Write(p []byte) (int, error) {
	if cReset == "" {
		return s.w.Write(p)
	}

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
	if len(line) < 12 || line[2] != ':' || line[5] != ':' || line[8] != ' ' || line[9] != '[' {
		out.Write(line)
		return
	}
	close := bytes.IndexByte(line[9:], ']')
	if close < 0 {
		out.Write(line)
		return
	}
	tsEnd := 8
	facet := string(line[10 : 9+close])
	color := facetColor[facet]
	if color == "" {
		color = cDim
	}
	rest := line[9+close+1:]

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
