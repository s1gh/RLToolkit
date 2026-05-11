// Package install handles unpacking .rltp archives into the plugins
// directory and removing installed plugin folders. Single code path
// for both the CLI (`rl-toolkit install`) and dashboard sideload.
package install

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// MaxArchiveBytes caps the .rltp download size. 50 MiB is generous
// for HTML/JS plugins and prevents a malformed catalog entry from
// causing a runaway download.
const MaxArchiveBytes = 50 << 20

// InstallFromURL downloads a .rltp from url, verifies SHA-256 against
// expectedSHA256 (lowercase hex, 64 chars), and unpacks it into
// pluginsDir/<name>/. Returns the unpacked plugin name on success.
//
// The download streams to a temp file so the whole archive never
// lives in memory. Hash mismatches abort before unpacking; the temp
// file is removed in either case.
func InstallFromURL(ctx context.Context, url, expectedSHA256, pluginsDir string) (string, error) {
	if len(expectedSHA256) != 64 {
		return "", fmt.Errorf("install: expected sha256 must be 64 hex chars")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("install: build request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("install: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("install: HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "rltp-*.zip")
	if err != nil {
		return "", fmt.Errorf("install: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	h := sha256.New()
	limited := io.LimitReader(resp.Body, MaxArchiveBytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	closeErr := tmp.Close()
	if err != nil {
		return "", fmt.Errorf("install: write temp: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("install: close temp: %w", closeErr)
	}
	if n > MaxArchiveBytes {
		return "", fmt.Errorf("install: archive exceeds %d bytes", MaxArchiveBytes)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expectedSHA256) {
		return "", fmt.Errorf("install: sha256 mismatch (want %s, got %s)", expectedSHA256, got)
	}
	return Install(tmpPath, pluginsDir)
}
