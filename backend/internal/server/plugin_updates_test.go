package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
