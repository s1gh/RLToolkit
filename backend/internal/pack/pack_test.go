package pack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestPack_PreservesNestedDirectoriesWithForwardSlashes(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()

	writeFile(t, filepath.Join(src, "manifest.json"), `{"name":"alpha","version":"0.1.0","overlay":{"file":"overlay.html"}}`)
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")
	writeFile(t, filepath.Join(src, "assets", "img", "logo.png"), "fakepng")

	got, err := Pack(src, out)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	r, err := zip.OpenReader(got)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	want := "assets/img/logo.png"
	found := false
	for _, f := range r.File {
		if f.Name == want {
			found = true
		}
		if strings.Contains(f.Name, `\`) {
			t.Errorf("entry %q contains backslash; want forward slashes only", f.Name)
		}
	}
	if !found {
		t.Errorf("nested entry %q missing from archive", want)
	}
}

func TestPack_OmitsDirectoryEntries(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFile(t, filepath.Join(src, "manifest.json"), `{"name":"alpha","version":"0.1.0","overlay":{"file":"overlay.html"}}`)
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")
	writeFile(t, filepath.Join(src, "assets", "logo.png"), "fakepng")

	got, err := Pack(src, out)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	r, err := zip.OpenReader(got)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			t.Errorf("archive contains directory entry %q; should be files only", f.Name)
		}
		if strings.HasSuffix(f.Name, "/") {
			t.Errorf("entry %q ends with slash (directory); should be files only", f.Name)
		}
	}
}

func TestPack_RejectsEmptyNameOrVersion(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"empty name", `{"name":"","version":"0.1.0","overlay":{"file":"overlay.html"}}`},
		{"empty version", `{"name":"alpha","version":"","overlay":{"file":"overlay.html"}}`},
		{"missing name", `{"version":"0.1.0","overlay":{"file":"overlay.html"}}`},
		{"missing version", `{"name":"alpha","overlay":{"file":"overlay.html"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := t.TempDir()
			out := t.TempDir()
			writeFile(t, filepath.Join(src, "manifest.json"), c.json)
			writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")
			if _, err := Pack(src, out); err == nil {
				t.Fatal("Pack accepted manifest without name/version; want error")
			}
		})
	}
}

func TestPack_IsReproducible(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	writeFile(t, filepath.Join(src, "manifest.json"), `{"name":"alpha","version":"0.1.0","overlay":{"file":"overlay.html"}}`)
	writeFile(t, filepath.Join(src, "overlay.html"), "<html></html>")
	writeFile(t, filepath.Join(src, "app.js"), "console.log('hi')")

	first, err := Pack(src, out)
	if err != nil {
		t.Fatalf("Pack #1: %v", err)
	}
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}

	// Force a second pack to a different dir; both must be byte-identical.
	out2 := t.TempDir()
	// Touch the source files to bump their mtimes — output should still match
	// because Pack writes stable mtimes into the archive.
	now := time.Now()
	_ = os.Chtimes(filepath.Join(src, "overlay.html"), now, now)
	_ = os.Chtimes(filepath.Join(src, "app.js"), now, now)

	second, err := Pack(src, out2)
	if err != nil {
		t.Fatalf("Pack #2: %v", err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("two packs of the same source produced different bytes (len %d vs %d); reproducibility broken", len(a), len(b))
	}
}
