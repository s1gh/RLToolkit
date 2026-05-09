// Package scaffold creates a working plugin from the minimum-viable
// templates. The result runs immediately — start the server, open the
// dashboard, and the new overlay shows up under "Plugins".
//
// The plugin is one HTML file plus a manifest. The author writes
// their logic inline in <script>; everything visual is handed to them
// by the SDK's overlay-mode handling and the shared design tokens in
// /sdk.css.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// namePattern restricts plugin identifiers to a safe character set.
// Mirrors backend.pluginNamePattern; kept independent so this package
// has no backend dependency.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Plugin scaffolds a new plugin in <pluginDir>/<name>/.
func Plugin(pluginDir, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("plugin name %q must match %s", name, namePattern)
	}
	dir := filepath.Join(pluginDir, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("plugin %q already exists at %s", name, dir)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create plugin dir: %w", err)
	}

	files := map[string]string{
		"manifest.json": render(manifestTemplate, name),
		"overlay.html":  render(overlayTemplate, name),
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

func render(tpl, name string) string {
	// A one-token replace; text/template is too noisy given the {{}}-
	// unfriendly CSS and JS content in the templates.
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
    "height": 180,
    "anchor": "top-right",
    "offset_x": 16,
    "offset_y": 16,
    "opacity": 1.0
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
    .row { display: flex; justify-content: space-between; align-items: baseline;
           font-size: 13px; color: var(--rlt-txt-2); margin-top: 4px; }
    .value { font-family: var(--rlt-mono); color: var(--rlt-cyan); }
  </style>
</head>
<body>
  <div class="card">
    <h2>__NAME__</h2>
    <div class="row"><span>Last goal by</span><span class="value" id="who">—</span></div>
    <div class="row"><span>Goals</span><span class="value" id="goals">0</span></div>
    <div class="row"><span>Saves</span><span class="value" id="saves">0</span></div>
    <div class="row"><span>Demos</span><span class="value" id="demos">0</span></div>
  </div>

  <script>
  'use strict';
  (function () {
    const who   = document.getElementById('who');
    const goals = document.getElementById('goals');
    const saves = document.getElementById('saves');
    const demos = document.getElementById('demos');
    const counts = { goals: 0, saves: 0, demos: 0 };

    function render() {
      goals.textContent = counts.goals;
      saves.textContent = counts.saves;
      demos.textContent = counts.demos;
    }

    function reset() {
      counts.goals = counts.saves = counts.demos = 0;
      who.textContent = '—';
      render();
    }

    // Discover what events you can handle: console.log(RLT.events.catalog)
    // or fetch /api/events. RLT.stats.* holds verified StatfeedEvent
    // eventName values (RLT.stats.DEMOLISH, .SAVE, .GOAL, …).
    RLT.plugin.register({
      name:    '__NAME__',
      version: '0.1.0',
      whilePhase: ['live', 'replay'],

      events: {
        GoalScored(g) {
          // g.scorer.name is always a string. g.scorer.player is the
          // enriched roster lookup and may be null in the brief window
          // before the match's first UpdateState arrives — don't depend
          // on it for the displayed name.
          counts.goals++;
          who.textContent = g.scorer.name;
          render();
        },
        StatfeedEvent(s) {
          // Use RLT.stats.* constants — typos surface as undefined
          // instead of silently never matching.
          if (s.eventName === RLT.stats.SAVE)     counts.saves++;
          if (s.eventName === RLT.stats.DEMOLISH) counts.demos++;
          render();
        },
      },

      // Reset counters whenever the player leaves a match. The server
      // flips match_active=false on MatchDestroyed, on connection drop,
      // OR after 5s of UpdateState silence — so this catches every exit
      // path including "back out to menu without MatchDestroyed".
      onMatchActive(active) {
        if (!active) reset();
      },
    });
  })();
  </script>
</body>
</html>
`
