package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"rl-toolkit/backend/internal/plugincatalog"
	"rl-toolkit/backend/internal/plugins"
)

func TestCatalogEndpointEmptyBeforeRefresh(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	cat := plugincatalog.New("http://127.0.0.1:0", "1.0.0", pm)
	s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/plugins/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Entries       []any   `json:"entries"`
		LastCheckedAt *string `json:"last_checked_at"`
		LastError     *string `json:"last_error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 0 {
		t.Fatalf("expected empty entries, got %d", len(body.Entries))
	}
	if body.LastCheckedAt != nil {
		t.Fatalf("expected nil last_checked_at, got %v", *body.LastCheckedAt)
	}
}

func TestCatalogEndpointStatesAfterRefresh(t *testing.T) {
	catalogBody := `{"schema":1,"plugins":[
		{"name":"alpha","version":"1.0.0","title":"Alpha","author":"a","description":"d","size_bytes":10,"url":"https://example.com/a.rltp","sha256":"0000000000000000000000000000000000000000000000000000000000000001"},
		{"name":"beta","version":"2.0.0","title":"Beta","author":"b","description":"d","size_bytes":20,"url":"https://example.com/b.rltp","sha256":"0000000000000000000000000000000000000000000000000000000000000002"},
		{"name":"gamma","version":"3.1.0","title":"Gamma","author":"c","description":"d","size_bytes":30,"url":"https://example.com/c.rltp","sha256":"0000000000000000000000000000000000000000000000000000000000000003"}
	]}`
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, catalogBody)
	}))
	defer catSrv.Close()

	pluginsDir := t.TempDir()
	writeManifest(t, pluginsDir, "beta", `{"name":"beta","version":"2.0.0","overlay":{"file":"overlay.html"}}`)
	writeManifest(t, pluginsDir, "gamma", `{"name":"gamma","version":"3.0.0","overlay":{"file":"overlay.html"}}`)

	pm := plugins.New(pluginsDir)
	cat := plugincatalog.New(catSrv.URL, "1.0.0", pm)
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/plugins/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var body struct {
		Entries []struct {
			Name             string `json:"name"`
			Title            string `json:"title"`
			Author           string `json:"author"`
			Description      string `json:"description"`
			Version          string `json:"version"`
			InstalledVersion string `json:"installed_version"`
			State            string `json:"state"`
			SizeBytes        int64  `json:"size_bytes"`
		} `json:"entries"`
		LastCheckedAt *string `json:"last_checked_at"`
		LastError     *string `json:"last_error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(body.Entries))
	}
	want := map[string]struct {
		state            string
		installedVersion string
	}{
		"alpha": {"not_installed", ""},
		"beta":  {"up_to_date", "2.0.0"},
		"gamma": {"update_available", "3.0.0"},
	}
	for _, e := range body.Entries {
		w, ok := want[e.Name]
		if !ok {
			t.Fatalf("unexpected entry %q", e.Name)
		}
		if e.State != w.state {
			t.Errorf("%s: state = %q, want %q", e.Name, e.State, w.state)
		}
		if e.InstalledVersion != w.installedVersion {
			t.Errorf("%s: installed_version = %q, want %q", e.Name, e.InstalledVersion, w.installedVersion)
		}
	}
	if body.LastCheckedAt == nil {
		t.Fatal("expected last_checked_at to be set after refresh")
	}
	if body.LastError != nil {
		t.Fatalf("expected last_error nil, got %q", *body.LastError)
	}
}

func writeManifest(t *testing.T, pluginsDir, name, body string) {
	t.Helper()
	dir := filepath.Join(pluginsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// validateManifest requires overlay.file to exist on disk; the
	// tests above declare overlay.html in their manifest bodies, so
	// drop an empty file by that name to satisfy the check.
	if err := os.WriteFile(filepath.Join(dir, "overlay.html"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
