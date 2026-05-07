package devapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubPlugins implements the Plugins interface used by devapi.
type stubPlugins struct {
	mu           sync.Mutex
	registered   map[string]string
	unregistered []string
	reloaded     []string
	registerErr  error
}

func newStubPlugins() *stubPlugins {
	return &stubPlugins{registered: map[string]string{}}
}

func (s *stubPlugins) RegisterDev(name, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registerErr != nil {
		return s.registerErr
	}
	s.registered[name] = path
	return nil
}

func (s *stubPlugins) UnregisterDev(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregistered = append(s.unregistered, name)
	delete(s.registered, name)
}

func (s *stubPlugins) NotifyReload(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloaded = append(s.reloaded, name)
}

func startTestServer(t *testing.T, pluginsAPI Plugins) (*Server, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("RLT_DEV_DISCOVERY_DIR", dataDir)
	srv, err := Start(context.Background(), pluginsAPI)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	// Wait briefly for the port file to appear (Start writes it
	// synchronously, so this is just a paranoia loop).
	deadline := time.Now().Add(time.Second)
	var portBody []byte
	for time.Now().Before(deadline) {
		portBody, err = os.ReadFile(filepath.Join(dataDir, "dev.port"))
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(portBody) == 0 {
		t.Fatalf("dev.port not written: %v", err)
	}
	port := strings.TrimSpace(string(portBody))
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("dev.port body not numeric: %q", port)
	}
	base := "http://127.0.0.1:" + port
	return srv, base, dataDir
}

func TestDevAPI_RegisterReloadUnregister(t *testing.T) {
	stub := newStubPlugins()
	_, base, _ := startTestServer(t, stub)

	// register
	pluginPath := t.TempDir()
	body, _ := json.Marshal(map[string]string{"name": "alpha", "path": pluginPath})
	resp, err := http.Post(base+"/dev/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("register status = %d, want 200", resp.StatusCode)
	}
	stub.mu.Lock()
	if got := stub.registered["alpha"]; got != pluginPath {
		t.Errorf("registered path = %q, want %q", got, pluginPath)
	}
	stub.mu.Unlock()

	// reload
	body, _ = json.Marshal(map[string]string{"name": "alpha"})
	resp, err = http.Post(base+"/dev/reload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("reload status = %d, want 200", resp.StatusCode)
	}
	stub.mu.Lock()
	if len(stub.reloaded) != 1 || stub.reloaded[0] != "alpha" {
		t.Errorf("reloaded = %v, want [alpha]", stub.reloaded)
	}
	stub.mu.Unlock()

	// unregister
	body, _ = json.Marshal(map[string]string{"name": "alpha"})
	resp, err = http.Post(base+"/dev/unregister", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("unregister status = %d, want 200", resp.StatusCode)
	}
	stub.mu.Lock()
	if len(stub.unregistered) != 1 || stub.unregistered[0] != "alpha" {
		t.Errorf("unregistered = %v, want [alpha]", stub.unregistered)
	}
	stub.mu.Unlock()
}

func TestDevAPI_BindsToLocalhostOnly(t *testing.T) {
	stub := newStubPlugins()
	srv, _, _ := startTestServer(t, stub)
	if !strings.HasPrefix(srv.Addr(), "127.0.0.1:") {
		t.Errorf("Addr() = %q, want 127.0.0.1:<port>", srv.Addr())
	}
}

func TestDevAPI_RemovesPortFileOnStop(t *testing.T) {
	stub := newStubPlugins()
	srv, _, dataDir := startTestServer(t, stub)
	srv.Stop()

	// Allow brief shutdown time.
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dataDir, "dev.port")); !os.IsNotExist(err) {
		t.Errorf("dev.port still exists after Stop (err=%v)", err)
	}
}

func TestDevAPI_RegisterRejectsBadJSON(t *testing.T) {
	stub := newStubPlugins()
	_, base, _ := startTestServer(t, stub)

	resp, err := http.Post(base+"/dev/register", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDevAPI_RegisterPropagatesError(t *testing.T) {
	stub := newStubPlugins()
	stub.registerErr = errSentinel
	_, base, _ := startTestServer(t, stub)

	body, _ := json.Marshal(map[string]string{"name": "alpha", "path": t.TempDir()})
	resp, err := http.Post(base+"/dev/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDevAPI_RegisterRejectsEmptyFields(t *testing.T) {
	stub := newStubPlugins()
	_, base, _ := startTestServer(t, stub)

	cases := []map[string]string{
		{"name": "", "path": "/tmp"},
		{"name": "alpha", "path": ""},
		{},
	}
	for _, c := range cases {
		body, _ := json.Marshal(c)
		resp, err := http.Post(base+"/dev/register", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 400 {
			t.Errorf("payload %v: status = %d, want 400", c, resp.StatusCode)
		}
	}
}

var errSentinel = &sentinelErr{}

type sentinelErr struct{}

func (*sentinelErr) Error() string { return "stub error" }

func TestDevAPI_HonorsDiscoveryEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RLT_DEV_DISCOVERY_DIR", dir)
	got, err := DiscoveryDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("DiscoveryDir() = %q, want %q", got, dir)
	}
}

func TestDevAPI_DefaultDiscoveryDirUnderUserConfig(t *testing.T) {
	t.Setenv("RLT_DEV_DISCOVERY_DIR", "")
	got, err := DiscoveryDir()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := os.UserConfigDir()
	want := filepath.Join(base, "rl-toolkit")
	if got != want {
		t.Errorf("DiscoveryDir() default = %q, want %q", got, want)
	}
}
