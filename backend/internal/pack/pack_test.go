package pack

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPack_ProducesArchiveWithRootEntries(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	writeFile(t, filepath.Join(src, "manifest.json"), `{"name":"alpha","version":"0.1.0","title":"Alpha","overlay":{"file":"overlay.html"}}`)
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")
	writeFile(t, filepath.Join(src, "app.js"), "console.log('hi')")

	got, err := Pack(src, out)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	wantName := "alpha-0.1.0.rltp"
	if filepath.Base(got) != wantName {
		t.Errorf("filename = %q, want %q", filepath.Base(got), wantName)
	}

	r, err := zip.OpenReader(got)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	names := map[string]bool{}
	for _, f := range r.File {
		names[f.Name] = true
	}

	for _, want := range []string{"manifest.json", "overlay.html", "app.js"} {
		if !names[want] {
			t.Errorf("archive missing %q; got entries: %v", want, names)
		}
	}
	for n := range names {
		if strings.HasPrefix(n, "alpha/") {
			t.Errorf("entry %q is nested under a top-level folder; archive root should be flat", n)
		}
	}
}

func TestPack_RejectsMissingManifest(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")

	if _, err := Pack(src, out); err == nil {
		t.Fatal("Pack succeeded with no manifest.json; want error")
	}
}

func TestPack_RejectsBadManifestJSON(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFile(t, filepath.Join(src, "manifest.json"), `{not json`)
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")

	if _, err := Pack(src, out); err == nil {
		t.Fatal("Pack succeeded with malformed manifest.json; want error")
	}
}

func TestPack_RoundTripManifestMatches(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFile(t, filepath.Join(src, "manifest.json"), `{"name":"alpha","version":"0.1.0","title":"Alpha","overlay":{"file":"overlay.html"}}`)
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")

	got, err := Pack(src, out)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	r, err := zip.OpenReader(got)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		var m struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.NewDecoder(rc).Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m.Name != "alpha" || m.Version != "0.1.0" {
			t.Errorf("manifest mismatch: %+v", m)
		}
		return
	}
	t.Fatal("manifest.json not found in archive")
}
