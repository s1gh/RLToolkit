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

func TestManager_DevPluginShadowsInstalled(t *testing.T) {
	installed := t.TempDir()
	dev := t.TempDir()

	// Installed: name=alpha, version=1.0.0
	writeManifest(t, installed, "alpha", Manifest{
		Name: "alpha", Title: "Alpha (installed)", Version: "1.0.0",
		Overlay: OverlayConfig{File: "overlay.html", Width: 100, Height: 100, Anchor: "top-left"},
	})

	// Dev: same name, different version, different folder.
	devPluginRoot := filepath.Join(dev, "alpha-dev")
	if err := os.MkdirAll(devPluginRoot, 0755); err != nil {
		t.Fatal(err)
	}
	devManifest := Manifest{
		Name: "alpha", Title: "Alpha (dev)", Version: "9.9.9",
		Overlay: OverlayConfig{File: "overlay.html", Width: 100, Height: 100, Anchor: "top-left"},
	}
	body, _ := json.Marshal(devManifest)
	if err := os.WriteFile(filepath.Join(devPluginRoot, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}

	pm := New(installed)
	if err := pm.RegisterDev("alpha", devPluginRoot); err != nil {
		t.Fatalf("RegisterDev: %v", err)
	}

	got := pm.List()
	if len(got) != 1 {
		t.Fatalf("got %d manifests, want 1; have %+v", len(got), got)
	}
	if got[0].Version != "9.9.9" {
		t.Errorf("dev override not applied; got version %q, want 9.9.9", got[0].Version)
	}
	if got[0].Title != "Alpha (dev)" {
		t.Errorf("dev override title not applied; got %q", got[0].Title)
	}
}

func TestManager_DevPluginUnregisterRevealsInstalled(t *testing.T) {
	installed := t.TempDir()
	dev := t.TempDir()

	writeManifest(t, installed, "alpha", Manifest{Name: "alpha", Title: "Alpha", Version: "1.0.0", Overlay: OverlayConfig{File: "overlay.html", Anchor: "top-left"}})

	devRoot := filepath.Join(dev, "alpha")
	if err := os.MkdirAll(devRoot, 0755); err != nil {
		t.Fatal(err)
	}
	dm := Manifest{Name: "alpha", Title: "Dev", Version: "9.9.9", Overlay: OverlayConfig{File: "overlay.html", Anchor: "top-left"}}
	body, _ := json.Marshal(dm)
	if err := os.WriteFile(filepath.Join(devRoot, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}

	pm := New(installed)
	if err := pm.RegisterDev("alpha", devRoot); err != nil {
		t.Fatal(err)
	}
	pm.UnregisterDev("alpha")

	got := pm.List()
	if len(got) != 1 || got[0].Version != "1.0.0" {
		t.Errorf("after UnregisterDev, want installed alpha v1.0.0; got %+v", got)
	}
}

func TestManager_RegisterDevValidatesPath(t *testing.T) {
	pm := New(t.TempDir())
	if err := pm.RegisterDev("alpha", t.TempDir()); err == nil {
		t.Error("RegisterDev should fail when path has no manifest.json")
	}
}

func TestManager_DevPluginAppearsWhenNoInstalledCounterpart(t *testing.T) {
	installed := t.TempDir() // empty
	dev := t.TempDir()

	devRoot := filepath.Join(dev, "ghost")
	if err := os.MkdirAll(devRoot, 0755); err != nil {
		t.Fatal(err)
	}
	dm := Manifest{Name: "ghost", Title: "Ghost", Version: "0.1.0", Overlay: OverlayConfig{File: "overlay.html", Anchor: "top-left"}}
	body, _ := json.Marshal(dm)
	if err := os.WriteFile(filepath.Join(devRoot, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}

	pm := New(installed)
	if err := pm.RegisterDev("ghost", devRoot); err != nil {
		t.Fatal(err)
	}
	got := pm.List()
	if len(got) != 1 || got[0].Name != "ghost" {
		t.Errorf("dev-only plugin not surfaced; got %+v", got)
	}
}

func TestManager_DevPathReturnsAbsoluteRegisteredPath(t *testing.T) {
	dev := t.TempDir()
	devRoot := filepath.Join(dev, "alpha")
	if err := os.MkdirAll(devRoot, 0755); err != nil {
		t.Fatal(err)
	}
	dm := Manifest{Name: "alpha", Version: "0.1.0", Overlay: OverlayConfig{File: "overlay.html", Anchor: "top-left"}}
	body, _ := json.Marshal(dm)
	if err := os.WriteFile(filepath.Join(devRoot, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}

	pm := New(t.TempDir())
	if got := pm.DevPath("alpha"); got != "" {
		t.Errorf("DevPath before register = %q, want empty", got)
	}
	if err := pm.RegisterDev("alpha", devRoot); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(devRoot)
	if got := pm.DevPath("alpha"); got != abs {
		t.Errorf("DevPath after register = %q, want %q", got, abs)
	}
}
