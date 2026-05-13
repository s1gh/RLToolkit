package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"rl-toolkit/backend/internal/bootid"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/overrides"
	"rl-toolkit/backend/internal/plugins"
	"rl-toolkit/backend/internal/replaywatch"
	"rl-toolkit/backend/internal/source"
	"rl-toolkit/backend/internal/surface"
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

func TestSideload_InstallsRltp(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	// Build a tiny .rltp in memory and post it.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	mf, _ := zw.Create("manifest.json")
	mf.Write([]byte(`{"name":"alpha","version":"0.1.0","overlay":{"file":"overlay.html"}}`))
	hf, _ := zw.Create("overlay.html")
	hf.Write([]byte("<html></html>"))
	zw.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "alpha-0.1.0.rltp")
	io.Copy(fw, &zipBuf)
	mw.Close()

	req := httptest.NewRequest("POST", "/api/sideload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "alpha", "manifest.json")); err != nil {
		t.Errorf("plugin not installed: %v", err)
	}
}

// Reinstalling a plugin via sideload should reset sizing-ish fields
// but keep the user's position and enable/disable choice. The new
// manifest may have changed width/height/opacity in ways that make the
// old values nonsensical, but anchor + offsets + the on/off toggle are
// still meaningful and re-doing them on every update would be annoying.
func TestSideload_TrimsOverridesToPositionAndEnabled(t *testing.T) {
	pluginsDir := t.TempDir()
	dataDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	ovr, err := overrides.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Plugins: pm, Overrides: ovr, PluginDir: pluginsDir})

	// Seed a full override for "alpha": position + size + opacity +
	// explicit disabled.
	anchor := "top-left"
	offX, offY := 42, 99
	opacity := 0.5
	width := plugins.Dimension{Px: 320}
	disabled := false
	if _, err := ovr.MergeOne("alpha", overrides.Override{
		Anchor:  &anchor,
		OffsetX: &offX,
		OffsetY: &offY,
		Width:   &width,
		Opacity: &opacity,
		Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}

	// Sideload a plugin under the same name (simulating an update).
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	mf, _ := zw.Create("manifest.json")
	mf.Write([]byte(`{"name":"alpha","version":"0.2.0","overlay":{"file":"overlay.html"}}`))
	hf, _ := zw.Create("overlay.html")
	hf.Write([]byte("<html></html>"))
	zw.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "alpha-0.2.0.rltp")
	io.Copy(fw, &zipBuf)
	mw.Close()

	req := httptest.NewRequest("POST", "/api/sideload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	got, ok := ovr.GetAll()["alpha"]
	if !ok {
		t.Fatal("override entry was dropped entirely; expected position to remain")
	}
	if got.Anchor == nil || *got.Anchor != anchor {
		t.Errorf("anchor: got %v, want %q", got.Anchor, anchor)
	}
	if got.OffsetX == nil || *got.OffsetX != offX {
		t.Errorf("offset_x: got %v, want %d", got.OffsetX, offX)
	}
	if got.OffsetY == nil || *got.OffsetY != offY {
		t.Errorf("offset_y: got %v, want %d", got.OffsetY, offY)
	}
	if got.Enabled == nil || *got.Enabled != disabled {
		t.Errorf("enabled: got %v, want %v", got.Enabled, disabled)
	}
	if got.Width != nil {
		t.Errorf("width should have been cleared, got %+v", got.Width)
	}
	if got.Opacity != nil {
		t.Errorf("opacity should have been cleared, got %v", *got.Opacity)
	}
}

// When the only existing override fields are sizing-ish (e.g. user
// tweaked opacity but never moved the overlay or toggled enable), a
// reinstall should remove the entry entirely rather than leaving an
// all-nil shell on disk.
func TestSideload_DropsOverrideEntryWhenNothingToKeep(t *testing.T) {
	pluginsDir := t.TempDir()
	dataDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	ovr, err := overrides.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Plugins: pm, Overrides: ovr, PluginDir: pluginsDir})

	opacity := 0.7
	if _, err := ovr.MergeOne("alpha", overrides.Override{Opacity: &opacity}); err != nil {
		t.Fatal(err)
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	mf, _ := zw.Create("manifest.json")
	mf.Write([]byte(`{"name":"alpha","version":"0.2.0","overlay":{"file":"overlay.html"}}`))
	hf, _ := zw.Create("overlay.html")
	hf.Write([]byte("<html></html>"))
	zw.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "alpha-0.2.0.rltp")
	io.Copy(fw, &zipBuf)
	mw.Close()

	req := httptest.NewRequest("POST", "/api/sideload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	if _, ok := ovr.GetAll()["alpha"]; ok {
		t.Errorf("override entry should have been removed (no position or enabled state to keep)")
	}
}

// Sideload of a brand-new plugin (no prior overrides) must not error
// on the no-op Delete; the override store treats Delete of a missing
// entry as success and the install path should rely on that.
func TestSideload_NoExistingOverridesIsHarmless(t *testing.T) {
	pluginsDir := t.TempDir()
	dataDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	ovr, err := overrides.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Plugins: pm, Overrides: ovr, PluginDir: pluginsDir})

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	mf, _ := zw.Create("manifest.json")
	mf.Write([]byte(`{"name":"fresh","version":"0.1.0","overlay":{"file":"overlay.html"}}`))
	hf, _ := zw.Create("overlay.html")
	hf.Write([]byte("<html></html>"))
	zw.Close()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "fresh.rltp")
	io.Copy(fw, &zipBuf)
	mw.Close()

	req := httptest.NewRequest("POST", "/api/sideload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestSideload_RejectsWrongExtension(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "totally-not-a-plugin.zip")
	fw.Write([]byte("PK"))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/sideload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSideload_RejectsBadArchive(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "bad.rltp")
	fw.Write([]byte("not a zip"))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/sideload", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSurface_GetReturnsFallbackWhenEmpty(t *testing.T) {
	surf, err := surface.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Surface: surf})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/overlay/surface", nil)
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Configured *surface.Size `json:"configured"`
		Detected   *surface.Size `json:"detected"`
		Effective  surface.Size  `json:"effective"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Configured != nil || body.Detected != nil {
		t.Errorf("expected nils, got configured=%+v detected=%+v", body.Configured, body.Detected)
	}
	if body.Effective.W != 1920 || body.Effective.H != 1080 {
		t.Errorf("effective: %+v", body.Effective)
	}
}

func TestSurface_PutPersistsAndPrioritizesConfigured(t *testing.T) {
	surf, err := surface.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Surface: surf})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/overlay/surface", strings.NewReader(`{"width":2560,"height":1440}`))
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rr.Code, rr.Body.String())
	}
	got := surf.Get()
	if got == nil || got.W != 2560 || got.H != 1440 {
		t.Errorf("after PUT: %+v", got)
	}
}

func TestSurface_PutNullClears(t *testing.T) {
	surf, err := surface.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := surf.Set(&surface.Size{W: 2560, H: 1440}); err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Surface: surf})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/overlay/surface", strings.NewReader(`null`))
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT null status %d: %s", rr.Code, rr.Body.String())
	}
	if surf.Get() != nil {
		t.Errorf("expected cleared, got %+v", surf.Get())
	}
}

func TestSurface_PutRejectsInvalid(t *testing.T) {
	surf, err := surface.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Surface: surf})
	cases := []string{
		`{"width":0,"height":1080}`,
		`{"width":99999,"height":1080}`,
		`not json`,
		`{"width":-1,"height":-1}`,
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/overlay/surface", strings.NewReader(c))
		srv.Routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", c, rr.Code)
		}
	}
}

func TestSurface_DetectedPostStoresInMemory(t *testing.T) {
	surf, err := surface.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Surface: surf})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/overlay/surface/detected", strings.NewReader(`{"width":3840,"height":2160}`))
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	d := surf.Detected()
	if d == nil || d.W != 3840 || d.H != 2160 {
		t.Errorf("detected: %+v", d)
	}
}

func TestMarshalSurfaceChanged_Shape(t *testing.T) {
	cfg := &surface.Size{W: 1280, H: 720}
	det := &surface.Size{W: 2560, H: 1440}
	eff := surface.Size{W: 1280, H: 720}
	raw := MarshalSurfaceChanged(cfg, det, eff)
	if raw == nil {
		t.Fatal("MarshalSurfaceChanged returned nil")
	}
	want := `{"Event":"_SurfaceChanged","Data":{"configured":{"width":1280,"height":720},"detected":{"width":2560,"height":1440},"effective":{"width":1280,"height":720}}}`
	if string(raw) != want {
		t.Errorf("envelope shape mismatch:\n got: %s\nwant: %s", raw, want)
	}
}

func TestReplayWatcher_GET(t *testing.T) {
	store, err := replaywatch.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := replaywatch.NewWatcher(store, recordingBroadcaster{}, replaywatch.WatcherOptions{})
	srv := New(Deps{ReplayWatcher: w})

	req := httptest.NewRequest(http.MethodGet, "/api/replay-watcher", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["status"]; !ok {
		t.Errorf("expected 'status' field in response: %+v", got)
	}
}

func TestReplayWatcher_PUT(t *testing.T) {
	store, err := replaywatch.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notified := 0
	store.Notify = func() { notified++ }
	w := replaywatch.NewWatcher(store, recordingBroadcaster{}, replaywatch.WatcherOptions{})
	srv := New(Deps{ReplayWatcher: w})

	req := httptest.NewRequest(http.MethodPut, "/api/replay-watcher",
		strings.NewReader(`{"dir":"/some/path"}`))
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := store.Get(); got != "/some/path" {
		t.Errorf("store.Get = %q, want /some/path", got)
	}
	if notified != 1 {
		t.Errorf("Notify fired %d times, want 1", notified)
	}
}

func TestReplayWatcher_PUT_NullClears(t *testing.T) {
	store, err := replaywatch.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Set("/x")
	w := replaywatch.NewWatcher(store, recordingBroadcaster{}, replaywatch.WatcherOptions{})
	srv := New(Deps{ReplayWatcher: w})

	req := httptest.NewRequest(http.MethodPut, "/api/replay-watcher",
		strings.NewReader(`{"dir":null}`))
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := store.Get(); got != "" {
		t.Errorf("store.Get = %q, want empty", got)
	}
}

// recordingBroadcaster is a minimal fake for tests that don't need the
// full bus.
type recordingBroadcaster struct{}

func (recordingBroadcaster) Broadcast(bus.Event) {}

func TestReplayFile_HappyPath(t *testing.T) {
	demoDir := t.TempDir()
	want := []byte("REPLAY-BYTES")
	replayPath := filepath.Join(demoDir, "ABCD1234.replay")
	if err := os.WriteFile(replayPath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServerWithReplayDir(t, demoDir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path="+url.QueryEscape(replayPath), nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), want)
	}
}

func TestReplayFile_RejectsPathTraversal(t *testing.T) {
	demoDir := t.TempDir()
	srv := newTestServerWithReplayDir(t, demoDir)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path="+url.QueryEscape("/etc/passwd"), nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReplayFile_RejectsNonReplayExtension(t *testing.T) {
	demoDir := t.TempDir()
	notesPath := filepath.Join(demoDir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerWithReplayDir(t, demoDir)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path="+url.QueryEscape(notesPath), nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReplayFile_WatcherInactive(t *testing.T) {
	store, err := replaywatch.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := replaywatch.NewWatcher(store, recordingBroadcaster{}, replaywatch.WatcherOptions{})
	srv := New(Deps{ReplayWatcher: w})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path=/some/path.replay", nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestReplayFile_FileMissing(t *testing.T) {
	demoDir := t.TempDir()
	srv := newTestServerWithReplayDir(t, demoDir)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path="+url.QueryEscape(filepath.Join(demoDir, "GONE.replay")), nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestReplayFile_OversizeFile(t *testing.T) {
	demoDir := t.TempDir()
	bigPath := filepath.Join(demoDir, "BIG.replay")
	if err := os.WriteFile(bigPath, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := replayFileMaxBytes
	replayFileMaxBytes = 50
	defer func() { replayFileMaxBytes = prev }()

	srv := newTestServerWithReplayDir(t, demoDir)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path="+url.QueryEscape(bigPath), nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestReplayFile_RejectsSymlinkEscape(t *testing.T) {
	demoDir := t.TempDir()
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "secret.replay")
	if err := os.WriteFile(target, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(demoDir, "link.replay")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported on this filesystem: %v", err)
	}
	srv := newTestServerWithReplayDir(t, demoDir)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/replay-file?path="+url.QueryEscape(link), nil)
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// newTestServerWithReplayDir builds a Server with a watcher whose
// effective dir is set to demoDir. Used by /api/replay-file tests.
func newTestServerWithReplayDir(t *testing.T, demoDir string) *Server {
	t.Helper()
	store, err := replaywatch.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(demoDir); err != nil {
		t.Fatal(err)
	}
	w := replaywatch.NewWatcher(store, recordingBroadcaster{}, replaywatch.WatcherOptions{})
	// Run the watcher just long enough that State().Effective is set.
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	t.Cleanup(cancel)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if w.State().Effective == demoDir {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w.State().Effective != demoDir {
		t.Fatalf("watcher never resolved demoDir; state=%+v", w.State())
	}
	return New(Deps{ReplayWatcher: w})
}

func TestPluginFetch_AllowedOriginForwards(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "TEST-KEY" {
			t.Errorf("Authorization = %q, want TEST-KEY", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	prevClient := pluginFetchClient
	pluginFetchClient = upstream.Client()
	pluginFetchClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { pluginFetchClient = prevClient }()

	pluginsDir := t.TempDir()
	upstreamURL, _ := url.Parse(upstream.URL)
	writePluginWithPermissions(t, pluginsDir, "p", []string{"https://" + upstreamURL.Host})
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	target := "https://" + upstreamURL.Host + "/api/"
	req := httptest.NewRequest(http.MethodGet,
		"/api/plugin-fetch/p?url="+url.QueryEscape(target), nil)
	req.Header.Set("Authorization", "TEST-KEY")
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != `{"ok":true}` {
		t.Errorf("body = %q", got)
	}
}

func TestPluginFetch_DisallowedOriginRejected(t *testing.T) {
	pluginsDir := t.TempDir()
	writePluginWithPermissions(t, pluginsDir, "p", []string{"https://allowed.example.com"})
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest(http.MethodGet,
		"/api/plugin-fetch/p?url=https%3A%2F%2Fevil.example.com%2F", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestPluginFetch_NoPermissionsBlockRejected(t *testing.T) {
	pluginsDir := t.TempDir()
	writePluginForCSP(t, pluginsDir, "p", nil)
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest(http.MethodGet,
		"/api/plugin-fetch/p?url=https%3A%2F%2Fanything.example.com%2F", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestPluginFetch_UnknownPlugin404(t *testing.T) {
	pluginsDir := t.TempDir()
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest(http.MethodGet,
		"/api/plugin-fetch/ghost?url=https%3A%2F%2Fx.example.com%2F", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPluginFetch_NonHTTPSRejected(t *testing.T) {
	pluginsDir := t.TempDir()
	writePluginWithPermissions(t, pluginsDir, "p", []string{"https://allowed.example.com"})
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	req := httptest.NewRequest(http.MethodGet,
		"/api/plugin-fetch/p?url=http%3A%2F%2Fallowed.example.com%2F", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestPluginFetch_RedirectNotFollowed(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://elsewhere.example.com/")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	prevClient := pluginFetchClient
	pluginFetchClient = upstream.Client()
	pluginFetchClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { pluginFetchClient = prevClient }()

	pluginsDir := t.TempDir()
	upstreamURL, _ := url.Parse(upstream.URL)
	writePluginWithPermissions(t, pluginsDir, "p", []string{"https://" + upstreamURL.Host})
	pm := plugins.New(pluginsDir)
	srv := New(Deps{Plugins: pm, PluginDir: pluginsDir})

	target := "https://" + upstreamURL.Host + "/api/"
	req := httptest.NewRequest(http.MethodGet,
		"/api/plugin-fetch/p?url="+url.QueryEscape(target), nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want 302 (not followed)", rec.Code)
	}
}

// writePluginWithPermissions writes a plugin whose manifest declares
// the given connect allowlist. Used by plugin-fetch tests.
func writePluginWithPermissions(t *testing.T, dir, name string, connect []string) {
	t.Helper()
	pluginRoot := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "overlay.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    name,
		"version": "1.0",
		"overlay": map[string]any{"file": "overlay.html", "anchor": "top-left"},
	}
	if connect != nil {
		manifest["permissions"] = map[string]any{"connect": connect}
	}
	body, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginRoot, "manifest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePluginForCSP writes a plugin without a permissions block.
// Used by tests that exercise the "no allowlist" path.
func writePluginForCSP(t *testing.T, dir, name string, _ []string) {
	t.Helper()
	writePluginWithPermissions(t, dir, name, nil)
}
