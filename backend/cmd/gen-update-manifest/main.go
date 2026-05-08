// gen-update-manifest writes a Tauri-updater latest.json manifest.
//
// Usage:
//   gen-update-manifest \
//     -version 0.2.0 \
//     -sig release/windows/RLToolkit_0.2.0_x64-setup.exe.sig \
//     -url https://github.com/owner/RLToolkit/releases/download/v0.2.0/RLToolkit_0.2.0_x64-setup.exe \
//     [-notes "Bug fixes"] \
//     > release/windows/latest.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type platform struct {
	Signature string `json:"signature"`
	URL       string `json:"url"`
}

type manifest struct {
	Version   string              `json:"version"`
	Notes     string              `json:"notes"`
	PubDate   string              `json:"pub_date"`
	Platforms map[string]platform `json:"platforms"`
}

func main() {
	version := flag.String("version", "", "release version, no leading v (required)")
	sigPath := flag.String("sig", "", "path to .sig file produced by Tauri (required)")
	url := flag.String("url", "", "download URL of the signed installer (required)")
	notes := flag.String("notes", "", "release notes (optional)")
	flag.Parse()

	if *version == "" || *sigPath == "" || *url == "" {
		fmt.Fprintln(os.Stderr, "missing required flag")
		flag.Usage()
		os.Exit(2)
	}

	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read sig: %v\n", err)
		os.Exit(1)
	}

	m := manifest{
		Version: *version,
		Notes:   *notes,
		PubDate: time.Now().UTC().Format(time.RFC3339),
		Platforms: map[string]platform{
			"windows-x86_64": {
				Signature: string(sig),
				URL:       *url,
			},
		},
	}

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
