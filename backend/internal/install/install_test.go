package install

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRltp(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstall_UnzipsIntoPluginsDir(t *testing.T) {
	pluginsDir := t.TempDir()
	rltp := filepath.Join(t.TempDir(), "alpha.rltp")
	writeRltp(t, rltp, map[string]string{
		"manifest.json": `{"name":"alpha","version":"0.1.0","title":"Alpha","overlay":{"file":"overlay.html"}}`,
		"overlay.html":  "<html></html>",
		"app.js":        "console.log(1)",
	})

	name, err := Install(rltp, pluginsDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if name != "alpha" {
		t.Errorf("returned name = %q, want %q", name, "alpha")
	}
	for _, want := range []string{"manifest.json", "overlay.html", "app.js"} {
		got, err := os.ReadFile(filepath.Join(pluginsDir, "alpha", want))
		if err != nil {
			t.Errorf("expected file %s: %v", want, err)
			continue
		}
		if len(got) == 0 {
			t.Errorf("file %s is empty", want)
		}
	}
}

func TestInstall_RejectsMissingManifest(t *testing.T) {
	pluginsDir := t.TempDir()
	rltp := filepath.Join(t.TempDir(), "bad.rltp")
	writeRltp(t, rltp, map[string]string{"overlay.html": "<html></html>"})

	if _, err := Install(rltp, pluginsDir); err == nil {
		t.Fatal("Install succeeded without manifest; want error")
	}
}

func TestInstall_RejectsZipSlip(t *testing.T) {
	pluginsDir := t.TempDir()
	rltp := filepath.Join(t.TempDir(), "evil.rltp")
	// Entry name with .. tries to escape the destination.
	writeRltp(t, rltp, map[string]string{
		"manifest.json":  `{"name":"alpha","version":"0.1.0","overlay":{"file":"overlay.html"}}`,
		"../escaped.txt": "boo",
	})

	if _, err := Install(rltp, pluginsDir); err == nil {
		t.Fatal("Install accepted a zip-slip entry; want error")
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "..", "escaped.txt")); err == nil {
		t.Fatal("zip-slip wrote a file outside pluginsDir")
	}
}

func TestInstall_OverwritesExisting(t *testing.T) {
	pluginsDir := t.TempDir()
	target := filepath.Join(pluginsDir, "alpha")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	rltp := filepath.Join(t.TempDir(), "alpha.rltp")
	writeRltp(t, rltp, map[string]string{
		"manifest.json": `{"name":"alpha","version":"0.2.0","overlay":{"file":"overlay.html"}}`,
		"overlay.html":  "<html></html>",
	})

	if _, err := Install(rltp, pluginsDir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "stale.txt")); !os.IsNotExist(err) {
		t.Errorf("stale file from previous version still present (err=%v); install should replace folder", err)
	}
	body, err := os.ReadFile(filepath.Join(target, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"version":"0.2.0"`) {
		t.Errorf("manifest not updated: %s", body)
	}
}

func TestUninstall_RemovesFolder(t *testing.T) {
	pluginsDir := t.TempDir()
	target := filepath.Join(pluginsDir, "alpha")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte(`{"name":"alpha","version":"0.1.0"}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall("alpha", pluginsDir); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("plugin folder still exists after uninstall (err=%v)", err)
	}
}

func TestUninstall_RejectsPathTraversal(t *testing.T) {
	pluginsDir := t.TempDir()
	if err := Uninstall("../etc", pluginsDir); err == nil {
		t.Fatal("Uninstall accepted path-traversal name; want error")
	}
}

func TestInstallFromURL_HappyPath(t *testing.T) {
	pluginsDir := t.TempDir()
	rltp := filepath.Join(t.TempDir(), "foo.rltp")
	writeRltp(t, rltp, map[string]string{
		"manifest.json": `{"name":"foo","version":"1.0.0","overlay":{"file":"o.html"}}`,
		"o.html":        "<html></html>",
	})
	archiveBytes, err := os.ReadFile(rltp)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archiveBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer srv.Close()

	name, err := InstallFromURL(context.Background(), srv.URL, hex.EncodeToString(sum[:]), pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	if name != "foo" {
		t.Fatalf("name = %q, want foo", name)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "foo", "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestInstallFromURL_HashMismatch(t *testing.T) {
	pluginsDir := t.TempDir()
	rltp := filepath.Join(t.TempDir(), "foo.rltp")
	writeRltp(t, rltp, map[string]string{
		"manifest.json": `{"name":"foo","version":"1.0.0","overlay":{"file":"o.html"}}`,
		"o.html":        "<html></html>",
	})
	archiveBytes, _ := os.ReadFile(rltp)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer srv.Close()

	zero := strings.Repeat("0", 64)
	if _, err := InstallFromURL(context.Background(), srv.URL, zero, pluginsDir); err == nil {
		t.Fatal("expected hash mismatch error")
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "foo")); !os.IsNotExist(err) {
		t.Fatalf("plugin folder should not exist after hash mismatch: %v", err)
	}
}

func TestInstallFromURL_InvalidExpectedSHA(t *testing.T) {
	_, err := InstallFromURL(context.Background(), "https://example.com/x.rltp", "tooshort", t.TempDir())
	if err == nil {
		t.Fatal("expected error for short sha256")
	}
}

func TestInstallFromURL_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	zero := strings.Repeat("0", 64)
	if _, err := InstallFromURL(context.Background(), srv.URL, zero, t.TempDir()); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestInstallFromURL_NonHexExpectedSHA(t *testing.T) {
	notHex := strings.Repeat("Z", 64)
	_, err := InstallFromURL(context.Background(), "https://example.com/x.rltp", notHex, t.TempDir())
	if err == nil {
		t.Fatal("expected error for non-hex sha256")
	}
	if !strings.Contains(err.Error(), "hex") {
		t.Fatalf("expected hex error, got %v", err)
	}
}

func TestInstallFromURL_BodyTooLarge(t *testing.T) {
	// Serve MaxArchiveBytes+1 bytes so the size cap fires before the
	// hash comparison.
	big := bytes.Repeat([]byte("a"), MaxArchiveBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()
	zero := strings.Repeat("0", 64)
	_, err := InstallFromURL(context.Background(), srv.URL, zero, t.TempDir())
	if err == nil {
		t.Fatal("expected over-limit error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected 'exceeds' error, got %v", err)
	}
}
