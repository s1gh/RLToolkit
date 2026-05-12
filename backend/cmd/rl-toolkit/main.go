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
	"rl-toolkit/backend/internal/logging"
	"rl-toolkit/backend/internal/overrides"
	"rl-toolkit/backend/internal/paths"
	"rl-toolkit/backend/internal/pipeline"
	"rl-toolkit/backend/internal/plugincatalog"
	"rl-toolkit/backend/internal/plugins"
	"rl-toolkit/backend/internal/replaywatch"
	"rl-toolkit/backend/internal/roster"
	"rl-toolkit/backend/internal/scaffold"
	"rl-toolkit/backend/internal/server"
	"rl-toolkit/backend/internal/source"
	"rl-toolkit/backend/internal/state"
	"rl-toolkit/backend/internal/surface"
	"rl-toolkit/backend/internal/tick"
	"rl-toolkit/backend/internal/tracker"
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
	Dev       bool
}

// httpShutdown bounds how long http.Shutdown waits for in-flight
// handlers. SSE handlers wake from r.Context().Done() in milliseconds;
// this is the safety net for any non-SSE handler mid-write.
const httpShutdown = 2 * time.Second

func main() {
	// `serve` is the implicit default; anything else is a tool subcommand.
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
		case "dev":
			os.Exit(runDev(os.Args[2:]))
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
  rl-toolkit dev [path]        Hot-reload a plugin folder against the running overlay.
  rl-toolkit help              Show this message.

Server flags (serve):
  -rl-addr  host:port for RL Stats API (default 127.0.0.1:49123)
  -port     HTTP port (default 49200)
  -plugins  plugin directory (default <user-data>/RLToolkit/plugins)
  -data     data directory (default <user-data>/RLToolkit/data)
`)
}

// identityAdapter bridges *identity.Store to roster.IdentityLookup
// (Get → MyPrimaryID).
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
	closeLogs := installLogSinks(cfg.DataDir)
	defer func() { _ = closeLogs() }()

	printStartupBanner(cfg)
	printRLSetupNotice()
	printLogsSectionHeader()

	store, err := datastore.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}

	eventBus := bus.NewBus()
	pm := plugins.New(cfg.PluginDir)
	pm.AttachBroadcaster(eventBus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pluginCatalog := plugincatalog.New(PluginCatalogURL, Version, pm)
	// Refresh asynchronously so a slow GitHub doesn't delay startup;
	// the dashboard also re-fetches on first paint.
	go func() {
		rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
		defer rcancel()
		if err := pluginCatalog.Refresh(rctx); err != nil {
			log.Printf("[plugincatalog] initial refresh: %v", err)
		}
	}()

	// Dev API is opt-in: a localhost listener for `rl-toolkit dev`
	// to register and hot-reload plugin folders.
	if cfg.Dev {
		devSrv, err := devapi.Start(ctx, pm)
		if err != nil {
			log.Fatalf("[server] devapi start: %v", err)
		}
		defer devSrv.Stop()
		log.Printf("[devapi] listening on %s (port file in user config dir; set RLT_DEV_DISCOVERY_DIR to override)", devSrv.Addr())
	} else {
		log.Printf("[devapi] disabled (pass -dev to enable plugin hot-reload)")
	}

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
	tickDiff := emit.NewTickDiff(matchState, tickStore, corr, statfeed, rt)
	matchEnded := emit.NewMatchEnded(tickStore)

	// Pipeline wiring: events flow source → pipeline → bus. State
	// processors run before any emit processor; emit processors are
	// registered in dependency order (see pipeline.runEmit).
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
		if env := server.MarshalOverridesChanged(ovr.GetAll()); env != nil {
			eventBus.Broadcast(bus.Event{Name: "_OverridesChanged", Raw: env})
		}
	}

	surf, err := surface.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}
	surf.Notify = func() {
		if env := server.MarshalSurfaceChanged(surf.Get(), surf.Detected(), surf.Effective()); env != nil {
			eventBus.Broadcast(bus.Event{Name: "_SurfaceChanged", Raw: env})
		}
	}

	rwStore, err := replaywatch.NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}
	rwWatcher := replaywatch.NewWatcher(rwStore, eventBus, replaywatch.WatcherOptions{})
	rwStore.Notify = func() {
		rwWatcher.Reload()
		if env := server.MarshalReplayWatcherChanged(rwWatcher.State()); env != nil {
			eventBus.Broadcast(bus.Event{Name: "_ReplayWatcherChanged", Raw: env})
		}
	}

	trk, err := tracker.New(tracker.Options{DataDir: cfg.DataDir})
	if err != nil {
		log.Fatalf("[tracker] %v", err)
	}

	srv := server.New(server.Deps{
		Bus:           eventBus,
		Store:         store,
		Plugins:       pm,
		Source:        src,
		MatchState:    matchState,
		Roster:        rt,
		Demos:         demos,
		Overrides:     ovr,
		Surface:       surf,
		Discoveries:   disc,
		Identity:      idStore,
		Catalog:       pluginCatalog,
		ReplayWatcher: rwWatcher,
		Tracker:       trk,
		PluginDir:     cfg.PluginDir,
	})
	go src.Run(ctx)
	go pipe.Run(ctx, src, eventBus)
	go matchState.Run(ctx)
	go rwWatcher.Run(ctx)
	// Flush new Statfeed-name discoveries to disk every 5s. The store
	// is debounced (no-op when unchanged) so a tight tick is fine.
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
		// Tie request contexts to the app lifetime so long-lived
		// handlers (notably SSE) wake on cancel instead of waiting
		// out the shutdown grace period.
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
// the name in any position; Go's stdlib flag package stops at the
// first non-flag, hence the manual split.
func runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	pluginDir := fs.String("plugins", defaultPluginDir(), "Plugin directory path")
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

// parseFlags reads server CLI flags. Default directories resolve to
// the per-OS user application data dir (see backend/internal/paths),
// falling back to <exe-dir> when the OS path can't be resolved.
func parseFlags() Config {
	rlAddr := flag.String("rl-addr", "127.0.0.1:49123", "RL Stats API address (host:port)")
	httpPort := flag.Int("port", 49200, "HTTP server port")
	pluginDir := flag.String("plugins", defaultPluginDir(), "Plugin directory path")
	dataDir := flag.String("data", defaultDataDir(), "Data directory path")
	dev := flag.Bool("dev", false, "Enable the localhost dev API used by `rl-toolkit dev` for plugin hot-reload")
	flag.Parse()
	return Config{
		RLAddr:    *rlAddr,
		HTTPPort:  *httpPort,
		PluginDir: *pluginDir,
		DataDir:   *dataDir,
		Dev:       *dev,
	}
}

func executableDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// defaultPluginDir returns the per-OS plugins location, falling back to
// <exe-dir>/plugins when the OS dir can't be resolved.
func defaultPluginDir() string {
	if dir, err := paths.PluginsDir(); err == nil {
		return dir
	}
	return filepath.Join(executableDir(), "plugins")
}

// defaultDataDir returns the per-OS data location, falling back to
// <exe-dir>/data when the OS dir can't be resolved.
func defaultDataDir() string {
	if dir, err := paths.DataDir(); err == nil {
		return dir
	}
	return filepath.Join(executableDir(), "data")
}

// awaitSignal blocks until SIGINT/SIGTERM. A second signal in the
// grace window force-exits so Ctrl+C, Ctrl+C doesn't wait for the
// shutdown deadline.
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

// ANSI escape codes; empty when output can't render them so pipes /
// dumb terms get clean plain text.
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

// ansi returns the escape only when stderr is a real terminal that
// can render it. Honors NO_COLOR (https://no-color.org) and TERM=dumb.
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

// printStartupBanner shows the URLs the user needs (dashboard,
// overlay) and the RL listen address.
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

// printRLSetupNotice reminds the user RL's Stats API is OFF by default —
// without PacketSendRate>0, the toolkit sits at "disconnected".
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

// printLogsSectionHeader separates the static banner from the live logs.
func printLogsSectionHeader() {
	fmt.Fprintf(os.Stderr, "  %s%s%s %s──────────────────────────────────%s\n\n",
		cDim, "logs", cReset, cDim, cReset)
}

// installLogSinks routes stdlib log output to ANSI-styled stderr and
// a per-day file under <dataDir>/logs/. Returns a closer that flushes
// the file at shutdown. The file sees raw bytes without escapes so
// it reads cleanly in any text viewer.
func installLogSinks(dataDir string) func() error {
	styled := &styledLogWriter{w: os.Stderr}
	out, closeFn := logging.Init(dataDir, styled)
	log.SetOutput(out)
	return closeFn
}

// styledLogWriter colorizes stdlib log lines of the form
// "HH:MM:SS [facet] message" — dim timestamp, per-facet colored tag,
// plain message body.
type styledLogWriter struct {
	w io.Writer
}

// facetColor maps log facets to consistent colors. Unknown facets
// render dim.
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

// styleLine matches stdlib Ltime + "[facet]" prefix; non-matching
// lines pass through unchanged so stack traces stay intact.
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
