// Package install handles unpacking .rltp archives into the plugins
// directory and removing installed plugin folders. Single code path
// for both the CLI (`rl-toolkit install`) and dashboard sideload.
package install

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Install unzips rltpPath into pluginsDir/<name>/, where <name> is
// the manifest.json name. Replaces the target folder if it exists.
// Each entry path is validated against zip-slip.
func Install(rltpPath, pluginsDir string) (string, error) {
	r, err := zip.OpenReader(rltpPath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer r.Close()

	var m manifest
	for _, f := range r.File {
		if f.Name != "manifest.json" {
			continue
		}
		body, err := readZipEntry(f)
		if err != nil {
			return "", fmt.Errorf("read manifest: %w", err)
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return "", fmt.Errorf("parse manifest: %w", err)
		}
		break
	}
	if m.Name == "" {
		return "", errors.New("archive has no manifest.json with a non-empty name")
	}
	if err := safeName(m.Name); err != nil {
		return "", err
	}

	target := filepath.Join(pluginsDir, m.Name)
	if err := os.RemoveAll(target); err != nil {
		return "", fmt.Errorf("clear target: %w", err)
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return "", fmt.Errorf("create target: %w", err)
	}

	for _, f := range r.File {
		if err := extractEntry(f, target); err != nil {
			// Best-effort cleanup; surface the original error.
			_ = os.RemoveAll(target)
			return "", err
		}
	}
	return m.Name, nil
}

// Uninstall removes pluginsDir/<name>/. Refuses names containing path
// separators or dot-segments to avoid accidental traversal.
func Uninstall(name, pluginsDir string) error {
	if err := safeName(name); err != nil {
		return err
	}
	target := filepath.Join(pluginsDir, name)
	return os.RemoveAll(target)
}

func safeName(name string) error {
	if name == "" {
		return errors.New("plugin name is empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("plugin name %q contains path separator", name)
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return fmt.Errorf("plugin name %q is reserved or hidden", name)
	}
	return nil
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func extractEntry(f *zip.File, dest string) error {
	cleaned := filepath.Clean(f.Name)
	if cleaned == "." || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive entry %q escapes destination", f.Name)
	}
	outPath := filepath.Join(dest, cleaned)
	relCheck, err := filepath.Rel(dest, outPath)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		return fmt.Errorf("archive entry %q escapes destination", f.Name)
	}

	if f.FileInfo().IsDir() {
		return os.MkdirAll(outPath, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return err
	}
	return writeZipEntry(f, outPath)
}

// writeZipEntry copies one archive entry to disk. Surfaces output
// close errors so a silent failure on partial write doesn't leave a
// corrupt plugin behind.
func writeZipEntry(f *zip.File, outPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
