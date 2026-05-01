package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

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
  -port     HTTP port (default 8080)
  -plugins  plugin directory (default <exe-dir>/plugins)
  -data     data directory (default <exe-dir>/data)
`)
}

func runServe() {
	cfg := parseFlags()
	log.SetFlags(log.Ltime)

	// Print the welcome + setup banner first so the user sees a clean
	// intro before any timestamped operational log lines start.
	printStartupBanner(cfg)
	printRLSetupNotice()

	store, err := NewDataStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] %v", err)
	}

	bus := NewEventBus()
	pm := NewPluginManager(cfg.PluginDir)
	client := NewRLClient(cfg.RLAddr, bus)
	lifecycle := NewLifecycleTracker(bus)
	client.AttachLifecycle(lifecycle)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &Server{bus: bus, store: store, plugins: pm, client: client, lifecycle: lifecycle, config: cfg}
	go client.Run(ctx)
	go lifecycle.Run(ctx)

	hs := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: srv.routes()}
	go func() {
		if err := hs.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("[server] %v", err)
		}
	}()

	awaitSignal()
	log.Println("[server] Shutting down …")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), httpShutdown)
	defer shutCancel()
	if err := hs.Shutdown(shutCtx); err != nil {
		log.Printf("[server] shutdown: %v", err)
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
	httpPort := flag.Int("port", 8080, "HTTP server port")
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

func awaitSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}

// ANSI escape codes. Empty when the terminal can't render them, so
// the same code path produces clean plain-text in pipes / dumb terms.
var (
	cReset  = ansi("\x1b[0m")
	cDim    = ansi("\x1b[2m")
	cBold   = ansi("\x1b[1m")
	cCyan   = ansi("\x1b[36m")
	cAmber  = ansi("\x1b[33m")
	cAccent = ansi("\x1b[1;36m") // bold cyan — for the wordmark
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
