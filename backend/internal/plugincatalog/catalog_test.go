package plugincatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestRefreshPreservesPreviousCatalogOnError(t *testing.T) {
	good, _ := os.ReadFile("testdata/plugins.json")
	var serve []byte = good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serve == nil {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(serve)
	}))
	defer srv.Close()

	m := New(srv.URL, "1.0.0", nil)
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if _, ok := m.Find("demos2"); !ok {
		t.Fatal("expected demos2 after first refresh")
	}

	// Make the server fail; previous catalog must survive.
	serve = nil
	if err := m.Refresh(context.Background()); err == nil {
		t.Fatal("expected error from 500 response")
	}
	if _, ok := m.Find("demos2"); !ok {
		t.Fatal("previous catalog should be preserved after Refresh error")
	}
	if m.LastError() == nil {
		t.Fatal("LastError should be non-nil after failed refresh")
	}
}

func TestRefreshRejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	m := New(srv.URL, "1.0.0", nil)
	if err := m.Refresh(context.Background()); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestRefreshRejectsOversizedBody(t *testing.T) {
	// 1 MiB + 1 byte of zero-padded JSON-ish content. The server's
	// response triggers the post-LimitReader size check rather than a
	// parse error.
	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()
	m := New(srv.URL, "1.0.0", nil)
	err := m.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected error for oversize body")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-cap error, got %v", err)
	}
}

func TestIsValidHTTPSURLRejectsJunk(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/x.rltp":              true,
		"https://example.com:8443/path/to/x.rltp": true,
		"http://example.com/x.rltp":               false, // not https
		"https:///x.rltp":                         false, // no host
		"https://user:pass@example.com/x.rltp":    false, // userinfo
		"https://example.com/x.rltp?token=secret": false, // query
		"https://example.com/x.rltp#frag":         false, // fragment
		"not-a-url":                               false,
	}
	for in, want := range cases {
		if got := isValidHTTPSURL(in); got != want {
			t.Errorf("isValidHTTPSURL(%q) = %v, want %v", in, got, want)
		}
	}
}
