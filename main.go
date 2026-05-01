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
		log.Printf("[server] Dashboard → http://localhost:%d", cfg.HTTPPort)
		log.Printf("[server] Overlay   → http://localhost:%d/overlay", cfg.HTTPPort)
		log.Printf("[server] Waiting for Rocket League Stats API on %s …", cfg.RLAddr)
		printRLSetupNotice()
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

// printRLSetupNotice reminds the user that RL's Stats API is OFF by
// default. Without PacketSendRate > 0 in DefaultStatsAPI.ini, RL never
// opens the local socket and the toolkit will sit at "disconnected"
// forever. This is the single most common first-run failure, so we
// surface the requirement up-front instead of letting users debug it
// from "Connection refused" logs.
func printRLSetupNotice() {
	log.Println("[server] ────────────────────────────────────────────────────")
	log.Println("[server] First-run check: RL's Stats API is OFF by default.")
	log.Println("[server] Edit  <RL Install>/TAGame/Config/DefaultStatsAPI.ini")
	log.Println("[server] before launching Rocket League and set:")
	log.Println("[server]     PacketSendRate=60   (or up to 120)")
	log.Println("[server]     Port=49123          (toolkit's default)")
	log.Println("[server] Changes only take effect on a fresh RL launch.")
	log.Println("[server] ────────────────────────────────────────────────────")
}
