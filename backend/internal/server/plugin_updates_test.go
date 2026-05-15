package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rl-toolkit/backend/internal/install"
	"rl-toolkit/backend/internal/plugincatalog"
	"rl-toolkit/backend/internal/plugins"
)

func TestUpdatesEndpointEmptyWhenCatalogNotRefreshed(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	cat := plugincatalog.New("http://127.0.0.1:0", "1.0.0", pm)
	s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/plugins/updates")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Updates []any   `json:"updates"`
		LastErr *string `json:"last_error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Updates) != 0 {
		t.Fatalf("expected empty updates, got %d", len(body.Updates))
	}
}

func TestRefreshCatalogEndpointRoundtrip(t *testing.T) {
	catalogBody := `{"schema":1,"plugins":[{"name":"demos2","version":"1.1.0","url":"https://example.com/x.rltp","sha256":"0000000000000000000000000000000000000000000000000000000000000001"}]}`
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, catalogBody)
	}))
	defer catSrv.Close()

	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	cat := plugincatalog.New(catSrv.URL, "1.0.0", pm)
	s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/plugins/refresh-catalog", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d", resp.StatusCode)
	}
	if _, ok := cat.Find("demos2"); !ok {
		t.Fatal("expected demos2 in catalog after refresh")
	}
}

func TestInstallUpdateUnknownPlugin(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	cat := plugincatalog.New("http://127.0.0.1:0", "1.0.0", pm)
	s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/plugins/install-update",
		"application/json", strings.NewReader(`{"name":"nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestInstallUpdateFreshInstall verifies the install-update handler
// works for a plugin that was not previously installed. Browse-tab
// installs reuse this handler, so a regression here would break that
// flow. Uses test hooks on plugincatalog.Manager and the install
// package because the catalog parser rejects non-HTTPS URLs, which
// would otherwise prevent a local httptest server from being usable
// as an archive host.
func TestInstallUpdateFreshInstall(t *testing.T) {
	// Build a minimal .rltp in memory: a zip with manifest.json and
	// the overlay file it points at, so the freshly-installed plugin
	// validates and surfaces via Plugins.Get.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mf, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mf.Write([]byte(`{"name":"freshplug","version":"1.0.0","overlay":{"file":"overlay.html"}}`)); err != nil {
		t.Fatal(err)
	}
	of, err := zw.Create("overlay.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := of.Write([]byte("<!doctype html><title>fresh</title>")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	archiveSHA := hex.EncodeToString(sum[:])

	// HTTPS archive server. httptest.NewTLSServer gives us a self-
	// signed cert; its Client() trusts that cert, so we swap the
	// install package's downloadClient for the duration of the test.
	archiveSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(archive)
	}))
	defer archiveSrv.Close()
	restoreClient := install.SetHTTPClientForTesting(archiveSrv.Client())
	defer restoreClient()

	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	cat := plugincatalog.New("https://unused.invalid/plugins.json", "1.0.0", pm)
	cat.SetEntriesForTesting([]plugincatalog.Entry{{
		Name:    "freshplug",
		Version: "1.0.0",
		URL:     archiveSrv.URL + "/freshplug.rltp",
		SHA256:  archiveSHA,
	}})

	s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})
	ts := httptest.NewServer(s.Routes())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/plugins/install-update",
		"application/json", strings.NewReader(`{"name":"freshplug"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("install status = %d, body = %s", resp.StatusCode, body)
	}

	var body struct {
		Name             string `json:"name"`
		InstalledVersion string `json:"installed_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "freshplug" {
		t.Errorf("name = %q, want freshplug", body.Name)
	}
	if body.InstalledVersion != "1.0.0" {
		t.Errorf("installed_version = %q, want 1.0.0", body.InstalledVersion)
	}

	if m := pm.Get("freshplug"); m == nil {
		t.Fatal("Plugins.Get(freshplug) returned nil after install")
	} else if m.Version != "1.0.0" {
		t.Fatalf("manifest version = %q, want 1.0.0", m.Version)
	}
}
