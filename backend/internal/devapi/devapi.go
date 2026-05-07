// Package devapi exposes a localhost-only HTTP listener that
// `rl-toolkit dev` calls to register/reload/unregister plugins it's
// watching. Bound strictly to 127.0.0.1 so a LAN client cannot push a
// dev folder path at the running overlay; the file with the bound
// port is written to the platform-canonical user config dir for the
// CLI to discover.
package devapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Plugins is the subset of the plugin manager that devapi calls into.
// Defined here so tests can substitute a stub.
type Plugins interface {
	RegisterDev(name, path string) error
	UnregisterDev(name string)
	// NotifyReload tells the system that plugin <name>'s assets have
	// changed and any open overlay window for that plugin should be
	// refreshed. Implementation may be a no-op for now (the overlay
	// re-fetches assets each time it loads, and the dashboard can
	// notice via SSE when added).
	NotifyReload(name string)
}

// Server is a running devapi listener.
type Server struct {
	pluginsAPI Plugins
	httpSrv    *http.Server
	listener   net.Listener
	portFile   string

	stopOnce sync.Once
}

// DiscoveryDir returns the directory where dev.port lives. The
// location is platform-canonical (os.UserConfigDir + "/rl-toolkit"):
//
//	Linux:   $XDG_CONFIG_HOME/rl-toolkit  (or ~/.config/rl-toolkit)
//	macOS:   ~/Library/Application Support/rl-toolkit
//	Windows: %APPDATA%\rl-toolkit
//
// Set RLT_DEV_DISCOVERY_DIR to override (used by tests and by advanced
// users running multiple instances on the same machine).
func DiscoveryDir() (string, error) {
	if env := os.Getenv("RLT_DEV_DISCOVERY_DIR"); env != "" {
		return env, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	return filepath.Join(base, "rl-toolkit"), nil
}

// Start binds a listener on 127.0.0.1:0 and serves the dev API. Blocks
// only long enough to bind; the HTTP server runs in a goroutine. The
// returned *Server is safe to Stop concurrently.
func Start(_ context.Context, p Plugins) (*Server, error) {
	dir, err := DiscoveryDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("ensure discovery dir: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind 127.0.0.1: %w", err)
	}

	portFile := filepath.Join(dir, "dev.port")
	port := ln.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(portFile, []byte(fmt.Sprintf("%d", port)), 0644); err != nil {
		ln.Close()
		return nil, fmt.Errorf("write dev.port: %w", err)
	}

	s := &Server{
		pluginsAPI: p,
		listener:   ln,
		portFile:   portFile,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/dev/register", s.handleRegister)
	mux.HandleFunc("/dev/reload", s.handleReload)
	mux.HandleFunc("/dev/unregister", s.handleUnregister)

	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[devapi] serve error: %v", err)
		}
	}()
	return s, nil
}

// Addr returns the listener's bound address (e.g. "127.0.0.1:54321").
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Stop shuts the server down and removes the port file.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(ctx)
		_ = os.Remove(s.portFile)
	})
}

type registerReq struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type nameOnlyReq struct {
	Name string `json:"name"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Path == "" {
		http.Error(w, "name and path are required", http.StatusBadRequest)
		return
	}
	if err := s.pluginsAPI.RegisterDev(req.Name, req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req nameOnlyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	s.pluginsAPI.NotifyReload(req.Name)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req nameOnlyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	s.pluginsAPI.UnregisterDev(req.Name)
	w.WriteHeader(http.StatusOK)
}
