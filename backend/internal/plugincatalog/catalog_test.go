package plugincatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestParseFixtureFiltersInvalidAndMinVersion(t *testing.T) {
	raw, err := os.ReadFile("testdata/plugins.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseCatalog(raw, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "demos2" {
		t.Fatalf("got %+v, want only demos2", entries)
	}
}

func TestParseRejectsWrongSchema(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"schema": 2, "plugins": []any{}})
	if _, err := parseCatalog(raw, "1.0.0"); err == nil {
		t.Fatal("expected error for schema != 1")
	}
}

func TestRefreshHTTP(t *testing.T) {
	body, _ := os.ReadFile("testdata/plugins.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	m := New(srv.URL, "1.0.0", nil)
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Find("demos2"); !ok {
		t.Fatal("expected demos2 in catalog")
	}
	if _, ok := m.Find("needs-newer"); ok {
		t.Fatal("entry above launcher version should be filtered")
	}
}
