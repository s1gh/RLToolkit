// Déjà Vu bootstrap — wires the SDK to the per-section views via the
// declarative plugin registration API.
//
// Each view is a small singleton on `DV.<name>` exposing { render, [bind] }.
// app.js owns:
//   - which view to mount (control page vs. transparent overlay)
//   - rAF-batched render scheduling
//   - mapping SDK events to render() / invalidate() calls

// Loaded as a classic <script>, not an ES module — strict mode is opt-in.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  // The SDK auto-adds body.overlay-mode whenever ?overlay=1, so we only
  // read the flag here to pick which views to mount.
  const isOverlay = new URLSearchParams(location.search).has('overlay');
  const views = isOverlay ? [DV.overlay] : [DV.identity, DV.match, DV.leaderboard];

  // ─── rAF-batched render ────────────────────────────────────
  // Multiple SDK callbacks may fire on the same frame (an UpdateState
  // ingestion can trigger both onMatch and onEncounters in one tick).
  // Coalesce them into one paint per frame.
  let rafScheduled = false;
  function scheduleRender() {
    if (rafScheduled) return;
    rafScheduled = true;
    requestAnimationFrame(() => {
      rafScheduled = false;
      for (const v of views) v.render();
    });
  }

  // Plugin metadata (name, version, author) comes from manifest.json —
  // see RLT.plugin.register's docs. We only declare runtime behaviour
  // here.
  RLT.plugin.register({
    init() {
      // Connection-status pill is dejavu chrome, not match data, so it
      // lives outside the per-view render path. Subscribe to the SDK's
      // *stable* signal so the pill doesn't flicker through the toolkit's
      // 30s self-reconnect cycles — see RLT.onStatusStable for details.
      // Foreground-gating of the overlay widget itself is handled by the
      // SDK via the manifest's `hide_when_unfocused` flag — no init code
      // or onFocusChange handler needed in plugin code anymore.
      RLT.onStatusStable((s) => {
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
      window.addEventListener(
        'DOMContentLoaded',
        () => {
          for (const v of views) v.bind?.();
          scheduleRender();
        },
        { once: true },
      );
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
    // onRoster fires only when the player list itself moves (join,
    // leave, team-switch, match guid flip) — typically a handful of
    // events per match. dejavu reads roster identity (id, name, team,
    // platform, encounterCount), not per-frame physics state, so the
    // 60-120Hz UpdateState stream we'd pull via onMatch is wasted
    // bandwidth. onRoster is built on the toolkit's synthetic
    // _RosterChanged event (see backend/roster_tracker.go) and does
    // not subscribe to UpdateState.
    onRoster: scheduleRender,
    // The THIS MATCH pill label reflects lifecycle.phase
    // (idle/lobby/countdown/live/replay/podium/ended), so a phase
    // transition without a roster move (countdown → live, live →
    // replay, etc.) still needs a repaint. onLifecycle bypasses
    // whilePhase gating in the SDK by design — exactly what we want
    // here so the pill keeps tracking through phases the plugin
    // doesn't otherwise act on.
    onLifecycle: scheduleRender,
  });
})();
