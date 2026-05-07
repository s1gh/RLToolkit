package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rl-toolkit/backend/internal/bootid"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/plugins"
	"rl-toolkit/backend/internal/source"
	"strings"
	"testing"
	"time"
)

func newBootIDTestServer(t *testing.T) *Server {
	t.Helper()
	return New(Deps{
		Bus:    bus.NewBus(),
		Source: source.NewRL("127.0.0.1:0"),
	})
}

func TestSSEFirstFrameIncludesBootID(t *testing.T) {
	srv := newBootIDTestServer(t)
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	rd := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		wantSnippet := `"Event":"_BootId"`
		wantID := `"bootId":"` + bootid.Get() + `"`
		if !strings.Contains(body, wantSnippet) || !strings.Contains(body, wantID) {
			t.Fatalf("first data frame did not contain _BootId; got: %s", body)
		}
		return
	}
	t.Fatalf("timed out waiting for first data frame")
}

func TestHandleBootIDReturnsJSON(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/boot-id", nil)
	s.handleBootID(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d; want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q; want %q", got, want)
	}
	want := `{"bootId":"` + bootid.Get() + `"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q; want %q", got, want)
	}
}

func writePlugin(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    name,
		"version": "0.1.0",
		"overlay": map[string]any{"file": "overlay.html", "anchor": "top-left"},
	}
	mb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "overlay.html"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestServePluginAsset_ServesInstalled(t *testing.T) {
	pluginsDir := t.TempDir()
	writePlugin(t, pluginsDir, "alpha", "<html>installed</html>")

	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest("GET", "/plugins/alpha/overlay.html", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<html>installed</html>" {
		t.Errorf("body = %q, want installed asset", got)
	}
}

func TestServePluginAsset_DevOverridesInstalled(t *testing.T) {
	pluginsDir := t.TempDir()
	writePlugin(t, pluginsDir, "alpha", "<html>installed</html>")

	devRoot := t.TempDir()
	writePlugin(t, devRoot, "alpha", "<html>dev</html>")

	pm := plugins.New(pluginsDir)
	if err := pm.RegisterDev("alpha", filepath.Join(devRoot, "alpha")); err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest("GET", "/plugins/alpha/overlay.html", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<html>dev</html>" {
		t.Errorf("body = %q, want dev asset", got)
	}
}

func TestServePluginAsset_RejectsTraversal(t *testing.T) {
	pluginsDir := t.TempDir()
	writePlugin(t, pluginsDir, "alpha", "<html>installed</html>")
	// Drop a sensitive file outside the plugin dir to ensure we don't reach it.
	secretPath := filepath.Join(pluginsDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}

	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest("GET", "/plugins/alpha/../secret.txt", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	// net/http.ServeMux calls cleanPath, which collapses
	// /plugins/alpha/../secret.txt → /plugins/secret.txt and issues a
	// 301 redirect. Either a redirect or a 404 is acceptable — both mean
	// the raw secret.txt bytes were never returned.
	if rec.Code == http.StatusOK {
		t.Errorf("traversal succeeded; status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestServePluginAsset_NotFoundForUnknownPlugin(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest("GET", "/plugins/ghost/overlay.html", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
