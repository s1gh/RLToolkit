// gen-update-manifest writes a Tauri-updater latest.json manifest.
//
// Usage:
//
//	gen-update-manifest \
//	  -version 0.2.0 \
//	  -windows-sig release/windows/RLToolkit_0.2.0_x64-setup.exe.sig \
//	  -windows-url https://github.com/owner/RLToolkit/releases/download/v0.2.0/RLToolkit_0.2.0_x64-setup.exe \
//	  -linux-sig   release/linux/RLToolkit_0.2.0_x86_64.AppImage.sig \
//	  -linux-url   https://github.com/owner/RLToolkit/releases/download/v0.2.0/RLToolkit_0.2.0_x86_64.AppImage \
//	  [-notes "Bug fixes"] \
//	  -out latest.json
//
// At least one platform pair (-windows-sig/-url or -linux-sig/-url)
// must be provided. Both sides of any provided pair must be set.
//
// Writes plain UTF-8 (no BOM). Prefer -out to redirection — Windows
// PowerShell 5.x rewrites stdout redirects with a BOM, which some
// strict JSON parsers refuse.
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
	winSig := flag.String("windows-sig", "", "path to the Windows .exe.sig (optional; pair with -windows-url)")
	winURL := flag.String("windows-url", "", "download URL of the signed Windows .exe (optional; pair with -windows-sig)")
	linSig := flag.String("linux-sig", "", "path to the Linux .AppImage.sig (optional; pair with -linux-url)")
	linURL := flag.String("linux-url", "", "download URL of the signed Linux .AppImage (optional; pair with -linux-sig)")
	notes := flag.String("notes", "", "release notes (optional)")
	outPath := flag.String("out", "", "destination file (optional; stdout when empty)")
	flag.Parse()

	if *version == "" {
		fmt.Fprintln(os.Stderr, "missing required flag: -version")
		flag.Usage()
		os.Exit(2)
	}

	platforms := map[string]platform{}

	addPlatform := func(name, sigPath, url string) {
		// Both sides of a pair must be set or both omitted.
		switch {
		case sigPath == "" && url == "":
			return
		case sigPath == "" || url == "":
			fmt.Fprintf(os.Stderr, "platform %s: -sig and -url must be paired (got sig=%q, url=%q)\n", name, sigPath, url)
			os.Exit(2)
		}
		sig, err := os.ReadFile(sigPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s sig: %v\n", name, err)
			os.Exit(1)
		}
		platforms[name] = platform{Signature: string(sig), URL: url}
	}

	addPlatform("windows-x86_64", *winSig, *winURL)
	addPlatform("linux-x86_64", *linSig, *linURL)

	if len(platforms) == 0 {
		fmt.Fprintln(os.Stderr, "no platform pairs provided (need at least one of -windows-sig/-url or -linux-sig/-url)")
		os.Exit(2)
	}

	m := manifest{
		Version:   *version,
		Notes:    *notes,
		PubDate:   time.Now().UTC().Format(time.RFC3339),
		Platforms: platforms,
	}

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	out = append(out, '\n')

	if *outPath != "" {
		if err := os.WriteFile(*outPath, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *outPath, err)
			os.Exit(1)
		}
		return
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintf(os.Stderr, "write stdout: %v\n", err)
		os.Exit(1)
	}
}
