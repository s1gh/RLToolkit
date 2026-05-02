// RL Toolkit — Overlay Editor
//
// Loaded by web/overlay.html when ?edit=1 is present in the URL. The
// host page has already fetched /api/plugins and /api/overlay/overrides
// and merged them into window.__rltOverlayContext. This script takes over
// rendering: production-style iframes for the live preview, plus edit
// chrome (outlines, badges, control panel, drag handlers).
(function(){
  'use strict';
  const ctx = window.__rltOverlayContext;
  if (!ctx) {
    console.error('[overlay-editor] missing __rltOverlayContext');
    return;
  }
  console.log('[overlay-editor] loaded with', ctx.merged.length, 'plugin(s)');
})();
