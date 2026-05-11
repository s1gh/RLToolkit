// Command gen-plugin-catalog walks a directory of .rltp archives,
// reads each archive's manifest.json, and writes a schema-1
// plugins.json describing them. Used by the release-plugins CI
// workflow to publish the catalog the launcher fetches.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type entry struct {
	Name               string `json:"name"`
	Version            string `json:"version"`
	Title              string `json:"title,omitempty"`
	Author             string `json:"author,omitempty"`
	Description        string `json:"description,omitempty"`
	URL                string `json:"url"`
	SHA256             string `json:"sha256"`
	SizeBytes          int64  `json:"size_bytes"`
	MinLauncherVersion string `json:"min_launcher_version,omitempty"`
}

type doc struct {
	Schema      int     `json:"schema"`
	GeneratedAt string  `json:"generated_at"`
	Plugins     []entry `json:"plugins"`
}

func buildCatalog(distDir, baseURL string) (doc, error) {
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return doc{}, err
	}
	out := doc{Schema: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Plugins: []entry{}}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".rltp") {
			continue
		}
		full := filepath.Join(distDir, de.Name())
		e, err := scanArchive(full)
		if err != nil {
			return doc{}, fmt.Errorf("%s: %w", de.Name(), err)
		}
		e.URL = baseURL + de.Name()
		out.Plugins = append(out.Plugins, e)
	}
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].Name < out.Plugins[j].Name })
	return out, nil
}

func scanArchive(path string) (entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return entry{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return entry{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return entry{}, err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return entry{}, err
	}
	defer zr.Close()
	var mfBytes []byte
	for _, zf := range zr.File {
		if zf.Name != "manifest.json" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return entry{}, err
		}
		mfBytes, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return entry{}, err
		}
		break
	}
	if mfBytes == nil {
		return entry{}, fmt.Errorf("manifest.json missing")
	}
	var mf struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Title       string `json:"title"`
		Author      string `json:"author"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(mfBytes, &mf); err != nil {
		return entry{}, err
	}
	return entry{
		Name:        mf.Name,
		Version:     mf.Version,
		Title:       mf.Title,
		Author:      mf.Author,
		Description: mf.Description,
		SHA256:      hex.EncodeToString(h.Sum(nil)),
		SizeBytes:   info.Size(),
	}, nil
}

func main() {
	in := flag.String("in", "dist", "Directory of .rltp files")
	base := flag.String("base-url", "", "URL prefix for downloads (release asset base)")
	out := flag.String("out", "dist/plugins.json", "Output path for plugins.json")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "gen-plugin-catalog: -base-url is required")
		os.Exit(2)
	}
	cat, err := buildCatalog(*in, *base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-plugin-catalog:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(cat, "", "  ")
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen-plugin-catalog: write:", err)
		os.Exit(1)
	}
}
