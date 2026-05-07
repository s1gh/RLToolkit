// Package pack zips a plugin source folder into a .rltp archive.
// The archive root is flat (manifest.json sits at the root, not nested
// under a top-level folder) so install becomes "unzip into the plugin
// folder" with no path rewriting.
package pack

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Pack zips srcDir into outDir as <name>-<version>.rltp and returns
// the absolute path to the written file. The archive's entry names are
// relative to srcDir (no nested top-level folder).
//
// The function reads manifest.json from srcDir to derive the filename
// and validates that the manifest parses as JSON with non-empty name
// and version. Other manifest fields are not validated here — that's
// the runtime loader's job.
func Pack(srcDir, outDir string) (string, error) {
	manifestPath := filepath.Join(srcDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("parse manifest: %w", err)
	}
	if m.Name == "" || m.Version == "" {
		return "", errors.New("manifest must have non-empty name and version")
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", fmt.Errorf("ensure outDir: %w", err)
	}

	outPath := filepath.Join(outDir, fmt.Sprintf("%s-%s.rltp", m.Name, m.Version))
	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		// Force forward slashes in archive entries (zip convention,
		// portable across OSes).
		rel = strings.ReplaceAll(rel, string(filepath.Separator), "/")

		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk %s: %w", srcDir, walkErr)
	}

	return outPath, nil
}
