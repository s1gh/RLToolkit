package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

type devClient struct {
	dataDir string
	http    *http.Client
}

func newDevClient(dataDir string) *devClient {
	return &devClient{
		dataDir: dataDir,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *devClient) baseURL() (string, error) {
	body, err := os.ReadFile(filepath.Join(c.dataDir, "dev.port"))
	if err != nil {
		return "", fmt.Errorf("read dev.port (is the overlay running?): %w", err)
	}
	port := strings.TrimSpace(string(body))
	if port == "" {
		return "", errors.New("dev.port is empty")
	}
	return "http://127.0.0.1:" + port, nil
}

func (c *devClient) post(path string, payload any) error {
	base, err := c.baseURL()
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := c.http.Post(base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (c *devClient) register(name, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return c.post("/dev/register", map[string]string{"name": name, "path": abs})
}

func (c *devClient) reload(name string) error {
	return c.post("/dev/reload", map[string]string{"name": name})
}

func (c *devClient) unregister(name string) error {
	return c.post("/dev/unregister", map[string]string{"name": name})
}

func runDev(args []string) int {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	dataDir := fs.String("data", filepath.Join(exeDirOrDot(), "data"), "Data directory (where dev.port lives)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rl-toolkit dev [path] [-data <dir>]\n\n")
		fs.PrintDefaults()
	}
	flagArgs, positional := splitFlagsAndPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	src := "."
	if len(positional) >= 1 {
		src = positional[0]
	}

	manifestPath := filepath.Join(src, "manifest.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dev: cannot read %s: %v\n", manifestPath, err)
		return 1
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		fmt.Fprintf(os.Stderr, "dev: parse manifest: %v\n", err)
		return 1
	}
	if m.Name == "" {
		fmt.Fprintln(os.Stderr, "dev: manifest has no name")
		return 1
	}

	client := newDevClient(*dataDir)
	if err := client.register(m.Name, src); err != nil {
		fmt.Fprintf(os.Stderr, "dev: register: %v\n", err)
		return 1
	}
	fmt.Printf("dev: registered %s from %s\n", m.Name, src)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dev: watcher: %v\n", err)
		_ = client.unregister(m.Name)
		return 1
	}
	defer watcher.Close()

	if err := watcher.Add(src); err != nil {
		fmt.Fprintf(os.Stderr, "dev: watch %s: %v\n", src, err)
		_ = client.unregister(m.Name)
		return 1
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		<-debounce.C
	}
	pendingReload := false

	for {
		select {
		case ev := <-watcher.Events:
			// Skip chmod-only events (most editors emit them on save).
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			pendingReload = true
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(150 * time.Millisecond)
		case <-debounce.C:
			if !pendingReload {
				continue
			}
			pendingReload = false
			if err := client.reload(m.Name); err != nil {
				fmt.Fprintf(os.Stderr, "dev: reload: %v\n", err)
			} else {
				fmt.Printf("dev: reloaded %s\n", m.Name)
			}
		case err := <-watcher.Errors:
			fmt.Fprintf(os.Stderr, "dev: watcher error: %v\n", err)
		case <-sigCh:
			fmt.Println("\ndev: shutting down")
			if err := client.unregister(m.Name); err != nil {
				fmt.Fprintf(os.Stderr, "dev: unregister: %v\n", err)
			}
			return 0
		}
	}
}
