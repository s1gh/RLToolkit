package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlugin_CreatesManifestAndOverlay(t *testing.T) {
	dir := t.TempDir()
	if err := Plugin(dir, "demo"); err != nil {
		t.Fatalf("Plugin: %v", err)
	}
	for _, fn := range []string{"manifest.json", "overlay.html"} {
		body, err := os.ReadFile(filepath.Join(dir, "demo", fn))
		if err != nil {
			t.Fatalf("read %s: %v", fn, err)
		}
		if !strings.Contains(string(body), "demo") {
			t.Errorf("%s missing rendered name; got %s", fn, body)
		}
		if strings.Contains(string(body), "__NAME__") {
			t.Errorf("%s still contains placeholder __NAME__", fn)
		}
	}
}

func TestPlugin_RejectsBadName(t *testing.T) {
	if err := Plugin(t.TempDir(), "bad name!"); err == nil {
		t.Fatal("expected error for invalid name")
	}
}

func TestPlugin_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := Plugin(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := Plugin(dir, "demo"); err == nil {
		t.Fatal("expected error on second Plugin call")
	}
}
