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
	"sort"
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
	// Safety net: ensures f is closed on early returns from the walk.
	// The explicit f.Close() below is the one whose error is reported.
	defer f.Close()

	zw := zip.NewWriter(f)

	// addFile opens path, copies its contents into the zip as entry rel,
	// and closes the source file before returning — no fd leak across
	// WalkDir iterations.
	addFile := func(path, rel string) error {
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		// Stable mtime so two packs of the same source produce byte-identical archives (matters for sha256 integrity in the registry).
		hdr := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, src)
		return err
	}

	// S1: Collect all file paths first, then sort, so archive entry order
	// is deterministic regardless of filesystem ordering.
	var paths []string
	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk %s: %w", srcDir, walkErr)
	}

	sort.Strings(paths)

	for _, path := range paths {
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return "", fmt.Errorf("rel path %s: %w", path, err)
		}
		// Force forward slashes in archive entries (zip convention,
		// portable across OSes).
		rel = strings.ReplaceAll(rel, string(filepath.Separator), "/")
		if err := addFile(path, rel); err != nil {
			return "", fmt.Errorf("add file %s: %w", rel, err)
		}
	}

	// B1: Close zip writer explicitly (not deferred) so flush errors are
	// surfaced before closing the underlying file.
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("close zip: %w", err)
	}
	// B1: Close the file explicitly and report any error (e.g. disk-full
	// on the final flush). The deferred f.Close() above will call Close a
	// second time; *os.File returns ErrClosed on double-close, which is
	// harmless because we no longer check it.
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}

	return outPath, nil
}
