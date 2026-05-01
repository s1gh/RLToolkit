// Déjà Vu bootstrap — wires the SDK to the per-section views via the
// declarative plugin registration API.
//
// Each view is a small singleton on `DV.<name>` exposing { render, [bind] }.
// app.js owns:
//   - which view to mount (control page vs. transparent overlay)
//   - rAF-batched render scheduling
//   - mapping SDK events to render() / invalidate() calls
'use strict';

(function () {
  // The SDK auto-adds body.overlay-mode whenever ?overlay=1, so we only
  // read the flag here to pick which views to mount.
  const isOverlay = new URLSearchParams(location.search).has('overlay');
  const views = isOverlay
    ? [DV.overlay]
    : [DV.identity, DV.match, DV.leaderboard];

  // ─── rAF-batched render ────────────────────────────────────
  // Multiple SDK callbacks may fire on the same frame (a tick triggers
  // both onTick and an encounter recording → onEncounters). Coalesce them
  // into one paint per frame.
  let rafScheduled = false;
  function scheduleRender() {
    if (rafScheduled) return;
    rafScheduled = true;
    requestAnimationFrame(() => {
      rafScheduled = false;
      for (const v of views) v.render();
    });
  }

  RLT.plugin.register({
    name:    'dejavu',
    version: '1.0.0',
    author:  'rl-toolkit',

    init() {
      // Connection-status pill is dejavu chrome, not match data, so it
      // lives outside the per-view render path.
      RLT.onStatus((s) => {
        const c = DV.dom.$('conn');
        if (!c) return;
        c.dataset.status = s;
        c.textContent = s === 'connected' ? 'live' : s;
      });

      // Grow the widget's width to fit a long player name. fitWidth is
      // monotonic — it only widens, never shrinks — so the manifest's
      // 320px is treated as a minimum. The 'extra' covers the right-side
      // glow padding on returning rows; the 600px cap stops a pathological
      // name from blowing up the surface.
      //
      // (We tried RLT.widget.autoSize earlier for full content tracking.
      // The layer-shell ↔ webview ↔ GTK chain shrunk-then-clipped instead
      // of shrunk-then-redrew, so we picked grow-only fitWidth as the
      // narrower contract that actually behaves.)
      if (isOverlay && RLT.widget.isHosted()) {
        RLT.widget.fitWidth({
          target: '.ov',
          maxWidth: 600,
          extra: 8,
        });
      }
    },

    ready() {
      // Once the encounter ledger and identity have loaded, do the first
      // paint so the page isn't blank before the first SSE event lands.
      window.addEventListener('DOMContentLoaded', () => {
        for (const v of views) v.bind?.();
        scheduleRender();
      }, { once: true });
      // If DOM is already ready (script ran late), bind + render now.
      if (document.readyState !== 'loading') {
        for (const v of views) v.bind?.();
        scheduleRender();
      }
    },

    // Identity changes invalidate the match scaffold so player rows re-class
    // for the YOU tag. Other view changes can ride a normal render.
    onIdentity() {
      DV.match?.invalidate?.();
      scheduleRender();
    },
    onEncounters: scheduleRender,
    onMatch:      scheduleRender,
    onTick:       scheduleRender,
  });
})();
