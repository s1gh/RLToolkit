package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"rl-toolkit/backend/internal/bus"
	"sync"
	"testing"
	"time"
)

// writeManifest writes a plugin folder with the given manifest plus
// stub files for every view path the manifest references (overlay,
// dashboard, settings). validateManifest requires referenced files to
// exist on disk, so tests that exercise the happy path must materialize
// them — an empty file is enough to pass the existence check.
func writeManifest(t *testing.T, dir, name string, m Manifest) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	touchView := func(rel string) {
		if rel == "" {
			return
		}
		full := filepath.Join(pluginDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	touchView(m.Overlay.File)
	if m.Dashboard != nil {
		touchView(m.Dashboard.File)
	}
	if m.Settings != nil {
		touchView(m.Settings.File)
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
	writeManifest(t, dir, "alpha", Manifest{Name: "alpha", Version: "1.0", Overlay: OverlayConfig{File: "overlay.html"}})
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
	writeManifest(t, dir, "p", Manifest{Name: "p", Title: "Old", Version: "1.0", Overlay: OverlayConfig{File: "overlay.html"}})
	pm := New(dir)
	if got := pm.List(); got[0].Title != "Old" {
		t.Fatalf("first read title=%q", got[0].Title)
	}
	// Change manifest with a fresh mtime to trigger re-parse.
	time.Sleep(10 * time.Millisecond)
	writeManifest(t, dir, "p", Manifest{Name: "p", Title: "New", Version: "1.0", Overlay: OverlayConfig{File: "overlay.html"}})
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
	writeManifest(t, dir, "good", Manifest{Name: "good", Version: "1.0", Overlay: OverlayConfig{File: "overlay.html"}})

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
	if err := os.WriteFile(filepath.Join(devPluginRoot, "overlay.html"), nil, 0644); err != nil {
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
	if err := os.WriteFile(filepath.Join(devRoot, "overlay.html"), nil, 0644); err != nil {
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
	if err := os.WriteFile(filepath.Join(devRoot, "overlay.html"), nil, 0644); err != nil {
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
	if err := os.WriteFile(filepath.Join(devRoot, "overlay.html"), nil, 0644); err != nil {
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

// stubBroadcaster captures broadcast events for assertions.
type stubBroadcaster struct {
	mu     sync.Mutex
	events []bus.Event
}

func (s *stubBroadcaster) Broadcast(evt bus.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
}

func TestManager_NotifyReloadBroadcastsDevPluginReload(t *testing.T) {
	pm := New(t.TempDir())
	stub := &stubBroadcaster{}
	pm.AttachBroadcaster(stub)

	pm.NotifyReload("alpha")

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.events) != 1 {
		t.Fatalf("got %d events, want 1", len(stub.events))
	}
	if stub.events[0].Name != "_DevPluginReload" {
		t.Errorf("event name = %q, want _DevPluginReload", stub.events[0].Name)
	}
	var payload map[string]string
	if err := json.Unmarshal(stub.events[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["name"] != "alpha" {
		t.Errorf("payload name = %q, want alpha", payload["name"])
	}
}

func TestManager_NotifyReloadWithoutBroadcasterDoesNotPanic(t *testing.T) {
	pm := New(t.TempDir())
	// No AttachBroadcaster.
	pm.NotifyReload("alpha") // should not panic, should just log
}

// ─── Validation tests ────────────────────────────────────────────
//
// Each subtest writes a manifest that's syntactically valid JSON but
// fails one specific structural rule. The plugin should be skipped
// (logged but not surfaced via List).

func TestManager_RejectsManifestMissingName(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "noname", Manifest{Version: "1.0", Overlay: OverlayConfig{File: "overlay.html"}})
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest without name to be rejected; got %+v", got)
	}
}

func TestManager_RejectsManifestMissingVersion(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "noversion", Manifest{Name: "noversion", Overlay: OverlayConfig{File: "overlay.html"}})
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest without version to be rejected; got %+v", got)
	}
}

func TestManager_RejectsManifestMissingOverlayFile(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "nooverlay", Manifest{Name: "nooverlay", Version: "1.0"})
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest without overlay.file to be rejected; got %+v", got)
	}
}

func TestManager_RejectsManifestWithMissingOverlayHTML(t *testing.T) {
	// Manifest references overlay.html but the file isn't on disk —
	// validation must catch that, not wait until the user opens the
	// overlay.
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "ghost")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Manifest{
		Name: "ghost", Version: "1.0",
		Overlay: OverlayConfig{File: "overlay.html"},
	})
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	// Note: no overlay.html written.
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest with missing overlay file to be rejected; got %+v", got)
	}
}

func TestManager_RejectsManifestWithEscapingViewPath(t *testing.T) {
	// `..` traversal would let a malformed plugin reference files
	// outside its sandbox. validateManifest must refuse the path
	// regardless of whether the target exists.
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "evil")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Manifest{
		Name: "evil", Version: "1.0",
		Overlay: OverlayConfig{File: "../escape.html"},
	})
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest with escaping view path to be rejected; got %+v", got)
	}
}

func TestManager_RejectsManifestWithAbsoluteViewPath(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "abs")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Manifest{
		Name: "abs", Version: "1.0",
		Overlay: OverlayConfig{File: "/etc/passwd"},
	})
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest with absolute view path to be rejected; got %+v", got)
	}
}

func TestManager_RejectsManifestWithMissingDashboardFile(t *testing.T) {
	// Optional view blocks (dashboard, settings) get the same checks
	// when they're present — a manifest declaring dashboard but not
	// providing the file is broken.
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "halfbaked")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "overlay.html"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(Manifest{
		Name: "halfbaked", Version: "1.0",
		Overlay:   OverlayConfig{File: "overlay.html"},
		Dashboard: &ViewConfig{File: "missing-dashboard.html"},
	})
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).List(); len(got) != 0 {
		t.Errorf("expected manifest with missing dashboard file to be rejected; got %+v", got)
	}
}

func TestManager_DefaultsEmptyTitleToName(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "p", Manifest{Name: "p", Version: "1.0", Overlay: OverlayConfig{File: "overlay.html"}})
	got := New(dir).List()
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Title != "p" {
		t.Errorf("Title = %q, want %q (empty title falls back to name)", got[0].Title, "p")
	}
}

func TestValidateManifest_PermissionsAcceptsHTTPS(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "ok", Manifest{
		Name: "ok", Version: "1.0",
		Overlay:     OverlayConfig{File: "overlay.html", Anchor: "top-left"},
		Permissions: &Permissions{Connect: []string{"https://ballchasing.com"}},
	})
	pm := New(dir)
	got := pm.Get("ok")
	if got == nil {
		t.Fatal("Get(ok) returned nil")
	}
	if got.Permissions == nil || len(got.Permissions.Connect) != 1 || got.Permissions.Connect[0] != "https://ballchasing.com" {
		t.Errorf("Permissions = %+v, want one https://ballchasing.com entry", got.Permissions)
	}
}

func TestValidateManifest_PermissionsDropsInvalidEntries(t *testing.T) {
	cases := []string{
		"http://x.com",       // non-https
		"https://x.com/path", // has path
		"https://x.com:8080", // non-443 port
		"https://x.com?q=1",  // query
		"https://x.com#frag", // fragment
		"https://u:p@x.com",  // userinfo
		"://malformed",       // unparseable
		"",                   // empty
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "p", Manifest{
				Name: "p", Version: "1.0",
				Overlay:     OverlayConfig{File: "overlay.html", Anchor: "top-left"},
				Permissions: &Permissions{Connect: []string{bad, "https://good.com"}},
			})
			pm := New(dir)
			got := pm.Get("p")
			if got == nil {
				t.Fatal("Get(p) returned nil; manifest should still load with bad entries dropped")
			}
			if got.Permissions == nil || len(got.Permissions.Connect) != 1 || got.Permissions.Connect[0] != "https://good.com" {
				t.Errorf("Permissions.Connect = %+v, want only [https://good.com]", got.Permissions)
			}
		})
	}
}

func TestValidateManifest_PermissionsAbsentMeansNil(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "p", Manifest{
		Name: "p", Version: "1.0",
		Overlay: OverlayConfig{File: "overlay.html", Anchor: "top-left"},
	})
	pm := New(dir)
	got := pm.Get("p")
	if got == nil {
		t.Fatal("Get(p) returned nil")
	}
	if got.Permissions != nil {
		t.Errorf("Permissions = %+v, want nil when no permissions block in manifest", got.Permissions)
	}
}

func TestValidateManifest_PermissionsEmptyConnectIsAllowed(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "p", Manifest{
		Name: "p", Version: "1.0",
		Overlay:     OverlayConfig{File: "overlay.html", Anchor: "top-left"},
		Permissions: &Permissions{},
	})
	pm := New(dir)
	got := pm.Get("p")
	if got == nil || got.Permissions == nil {
		t.Fatalf("expected non-nil Permissions, got %+v", got)
	}
	if len(got.Permissions.Connect) != 0 {
		t.Errorf("Connect = %+v, want empty", got.Permissions.Connect)
	}
}
