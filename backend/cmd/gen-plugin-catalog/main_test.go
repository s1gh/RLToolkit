package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func makeRltp(t *testing.T, dir, name, version string) string {
	t.Helper()
	p := filepath.Join(dir, name+"-"+version+".rltp")
	out, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	mf, _ := zw.Create("manifest.json")
	_, _ = mf.Write([]byte(`{"name":"` + name + `","version":"` + version +
		`","title":"` + name + `","author":"test","description":"t","overlay":{"file":"o.html","width":10,"height":10}}`))
	oh, _ := zw.Create("o.html")
	_, _ = oh.Write([]byte("<html></html>"))
	_ = zw.Close()
	_ = out.Close()
	return p
}

func TestBuildCatalog(t *testing.T) {
	dir := t.TempDir()
	p := makeRltp(t, dir, "demos2", "1.1.0")

	cat, err := buildCatalog(dir, "https://example.com/dl/")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plugins) != 1 {
		t.Fatalf("plugins len = %d", len(cat.Plugins))
	}
	if cat.Plugins[0].Name != "demos2" || cat.Plugins[0].Version != "1.1.0" {
		t.Fatalf("bad row: %+v", cat.Plugins[0])
	}
	raw, _ := os.ReadFile(p)
	want := sha256.Sum256(raw)
	if cat.Plugins[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("sha256 mismatch")
	}
	if cat.Plugins[0].URL != "https://example.com/dl/demos2-1.1.0.rltp" {
		t.Fatalf("url = %q", cat.Plugins[0].URL)
	}
	if cat.Schema != 1 {
		t.Fatal("schema != 1")
	}
	// generated_at parsable
	var doc map[string]any
	b, _ := json.Marshal(cat)
	_ = json.Unmarshal(b, &doc)
	if _, ok := doc["generated_at"].(string); !ok {
		t.Fatal("generated_at missing")
	}
}

func TestBuildCatalogSortsByName(t *testing.T) {
	dir := t.TempDir()
	_ = makeRltp(t, dir, "zoo", "1.0.0")
	_ = makeRltp(t, dir, "alpha", "1.0.0")

	cat, err := buildCatalog(dir, "https://example.com/dl/")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plugins) != 2 {
		t.Fatalf("len = %d", len(cat.Plugins))
	}
	if cat.Plugins[0].Name != "alpha" || cat.Plugins[1].Name != "zoo" {
		t.Fatalf("not sorted: %+v", cat.Plugins)
	}
}
