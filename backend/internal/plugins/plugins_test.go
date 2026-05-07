package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeManifest(t *testing.T, dir, name string, m Manifest) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestManager_ListsPlugins(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "alpha", Manifest{Name: "alpha", Title: "Alpha", Version: "1.0", Overlay: OverlayConfig{File: "overlay.html", Width: 100, Height: 100, Anchor: "top-left"}})
	writeManifest(t, dir, "beta", Manifest{Name: "beta", Title: "Beta", Version: "0.1", Overlay: OverlayConfig{File: "overlay.html", Width: 50, Height: 50, Anchor: "bottom-right"}})

	pm := New(dir)
	got := pm.List()
	if len(got) != 2 {
		t.Fatalf("got %d manifests, want 2", len(got))
	}
	names := map[string]bool{}
	for _, m := range got {
		names[m.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("missing expected plugins; got %v", names)
	}
}

func TestManager_HasGatesUnknown(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "alpha", Manifest{Name: "alpha", Version: "1.0"})
	pm := New(dir)

	if !pm.Has("alpha") {
		t.Error("Has(alpha) = false")
	}
	if pm.Has("ghost") {
		t.Error("Has(ghost) = true; folder doesn't exist")
	}
}

func TestManager_DefaultsZeroOpacityToOne(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "p", Manifest{Name: "p", Version: "1.0", Overlay: OverlayConfig{File: "x.html", Anchor: "top-left"}})
	pm := New(dir)
	got := pm.List()
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Overlay.Opacity != 1.0 {
		t.Errorf("Opacity = %v, want 1.0 (zero default)", got[0].Overlay.Opacity)
	}
}

func TestManager_RescansOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "p", Manifest{Name: "p", Title: "Old", Version: "1.0"})
	pm := New(dir)
	if got := pm.List(); got[0].Title != "Old" {
		t.Fatalf("first read title=%q", got[0].Title)
	}
	// Change manifest with a fresh mtime to trigger re-parse.
	time.Sleep(10 * time.Millisecond)
	writeManifest(t, dir, "p", Manifest{Name: "p", Title: "New", Version: "1.0"})
	got := pm.List()
	if got[0].Title != "New" {
		t.Errorf("after rewrite: title=%q, want New", got[0].Title)
	}
}

func TestManager_SkipsBadManifest(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken")
	if err := os.MkdirAll(bad, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "manifest.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "good", Manifest{Name: "good", Version: "1.0"})

	pm := New(dir)
	got := pm.List()
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("got %v, want only 'good'", got)
	}
}
