package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// scaffoldPlugin creates a working plugin in <pluginDir>/<name>/ from the
// minimum-viable templates. The result runs immediately — start the server,
// open the dashboard, and the new overlay shows up under "Plugins".
//
// The plugin is one HTML file plus a manifest. The author writes their
// logic inline in <script>; everything visual is handed to them by the
// SDK's overlay-mode handling and the shared design tokens in /sdk.css.
func scaffoldPlugin(pluginDir, name string) error {
	if !pluginNamePattern.MatchString(name) {
		return fmt.Errorf("plugin name %q must match %s", name, pluginNamePattern)
	}
	dir := filepath.Join(pluginDir, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("plugin %q already exists at %s", name, dir)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create plugin dir: %w", err)
	}

	files := map[string]string{
		"manifest.json": renderTemplate(manifestTemplate, name),
		"overlay.html":  renderTemplate(overlayTemplate, name),
	}
	for filename, content := range files {
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", filename, err)
		}
	}

	fmt.Printf("Created plugin %q at %s\n", name, dir)
	fmt.Printf("  - %s/manifest.json\n", name)
	fmt.Printf("  - %s/overlay.html\n", name)
	fmt.Printf("\nStart the server and open the dashboard to see it live.\n")
	return nil
}

func renderTemplate(tpl, name string) string {
	// Tiny templating — just a placeholder swap. We avoid text/template here
	// because the templates contain a lot of {{}}-unfriendly content
	// (CSS rules, JS object literals) and a one-token replace is plenty.
	return strings.ReplaceAll(tpl, "__NAME__", name)
}

const manifestTemplate = `{
  "name": "__NAME__",
  "title": "__NAME__",
  "version": "0.1.0",
  "author": "you",
  "description": "A new RL Toolkit plugin",
  "overlay": {
    "file": "overlay.html",
    "width": 320,
    "height": 120,
    "anchor": "top-right",
    "offset_x": 16,
    "offset_y": 16,
    "opacity": 1.0,
    "click_through": true
  }
}
`

// overlayTemplate is the absolute minimum a plugin author has to read.
// Everything cosmetic (transparent body in overlay mode, anchor pinning,
// fonts, design tokens) is handled by the SDK and /sdk.css. The author
// writes their event handlers in the <script> at the bottom.
const overlayTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>__NAME__</title>
  <link rel="icon" type="image/svg+xml" href="/favicon.ico">
  <link rel="stylesheet" href="/sdk.css">
  <script src="/sdk.js" data-plugin="__NAME__"></script>
  <style>
    /* Default look in standalone (dashboard) mode. */
    body { background: var(--rlt-bg-0); color: var(--rlt-txt); padding: 16px;
           font-family: var(--rlt-ui); margin: 0; }
    /* The SDK adds .overlay-mode automatically when ?overlay=1. */
    body.overlay-mode { background: transparent; padding: 0; }
    .card { background: var(--rlt-bg-1); border: 1px solid var(--rlt-line);
            border-radius: 12px; padding: 14px 16px; }
    h2 { font-family: var(--rlt-display); font-size: 14px; margin: 0 0 6px;
         text-transform: uppercase; letter-spacing: 0.18em; color: #fff; }
    .value { font-family: var(--rlt-mono); color: var(--rlt-cyan); }
  </style>
</head>
<body>
  <div class="card">
    <h2>__NAME__</h2>
    <div>Last goal by: <span class="value" id="who">—</span></div>
  </div>

  <script>
  'use strict';
  (function () {
    const who = document.getElementById('who');

    // Discover what events you can handle: console.log(RLT.events.catalog)
    // or fetch /api/events. See plugins/goalfeed for a richer example.
    RLT.plugin.register({
      name:    '__NAME__',
      version: '0.1.0',

      events: {
        GoalScored(g) {
          // g.scorer.name is always a string. g.scorer.player is the
          // enriched roster lookup and may be null in the brief window
          // before the match's first UpdateState arrives — don't depend
          // on it for the displayed name.
          who.textContent = g.scorer.name;
        },
      },

      ready() {
        who.textContent = 'waiting for a goal…';
      },
    });
  })();
  </script>
</body>
</html>
`
