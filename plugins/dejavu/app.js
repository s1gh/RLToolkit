// Déjà Vu bootstrap — wires the SDK to the per-section views via the
// declarative plugin registration API.
//
// Each view is a small singleton on `DV.<name>` exposing { render, [bind] }.
// app.js owns:
//   - which view to mount (overlay vs. dashboard)
//   - rAF-batched render scheduling
//   - mapping SDK events to render() / invalidate() calls

// Loaded as a classic <script>, not an ES module — strict mode is opt-in.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  // RLT.view comes from the <script data-view="…"> tag on each entry
  // file. dashboard.html mounts match + leaderboard; overlay.html
  // mounts only the transparent overlay view.
  const isOverlay = RLT.isOverlay;
  const views = isOverlay ? [DV.overlay] : [DV.match, DV.leaderboard];

  const scheduleRender = RLT.util.rafBatcher(() => {
    for (const v of views) v.render();
  });

  RLT.plugin.register({
    init() {
      if (!isOverlay) RLT.ui.bindStatusPill('conn');

      // Grow the widget's width to fit a long player name. fitWidth is
      // monotonic — it only widens, never shrinks — so the manifest's
      // 320px is treated as a minimum. 'extra' covers the right-side
      // glow padding on returning rows; the 600px cap stops a
      // pathological name from blowing up the surface.
      if (isOverlay && RLT.widget.isHosted()) {
        RLT.widget.fitWidth({
          target: '.ov',
          maxWidth: 600,
          extra: 8,
        });
      }
    },

    ready() {
      // Once the encounter ledger and identity have loaded, do the
      // first paint so the page isn't blank before the first SSE
      // event lands.
      let booted = false;
      function bootstrap() {
        if (booted) return;
        booted = true;
        for (const v of views) v.bind?.();
        // "This is me" buttons on dashboard rows.
        if (!isOverlay) {
          document.addEventListener('click', async (e) => {
            const btn = e.target.closest('[data-claim-id]');
            if (!btn) return;
            e.preventDefault();
            await RLT.me.set(btn.getAttribute('data-claim-id'));
            RLT.ui.toast('Identity claimed');
          });
        }
        scheduleRender();
      }
      window.addEventListener('DOMContentLoaded', bootstrap, { once: true });
      if (document.readyState !== 'loading') bootstrap();
    },

    onIdentity() {
      DV.match?.invalidate?.();
      scheduleRender();
    },
    onEncounters: scheduleRender,
    // onRoster fires only when the player list itself moves (join,
    // leave, team-switch, match guid flip) — typically a handful of
    // events per match. dejavu reads roster identity, not per-frame
    // physics state, so the 60-120Hz UpdateState stream we'd pull via
    // onMatch is wasted bandwidth. Built on the toolkit's synthetic
    // _RosterChanged event.
    onRoster: scheduleRender,
    // The THIS MATCH pill label reflects state.phase, so a phase
    // transition without a roster move (countdown → live, live →
    // replay, etc.) still needs a repaint.
    onState: scheduleRender,
  });
})();
