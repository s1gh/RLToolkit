// RL Toolkit — Overlay Editor
//
// Loaded by web/overlay.html when ?edit=1 is present in the URL. The host
// page has already fetched /api/plugins and /api/overlay/overrides and
// merged them into window.__rltOverlayContext. This script takes over
// rendering: production-style iframes for the live preview, plus edit
// chrome (outlines, badges, capture divs that block iframe mouse events).
(function(){
  'use strict';
  const ctx = window.__rltOverlayContext;
  if (!ctx) {
    console.error('[overlay-editor] missing __rltOverlayContext');
    return;
  }

  // Make the edit page non-transparent so widget chrome is readable
  // against something. The production overlay is intentionally see-
  // through; the editor is its own thing.
  document.documentElement.style.background = '#0a0c14';
  document.body.style.background = '#0a0c14';

  // Per-widget state. `el` is the wrapper div that owns positioning;
  // `iframe` is the live preview; `capture` is the transparent overlay
  // that intercepts mouse events so iframe content never sees them.
  // `overlay` is the live override values (with manifest defaults filled
  // in) — mutated by drag/resize/anchor/opacity changes.
  const widgets = ctx.merged.map(({ plugin, overlay }) => buildWidget(plugin, overlay));

  function buildWidget(plugin, overlay) {
    const a = overlay.anchor || 'top-right';
    const el = document.createElement('div');
    el.style.position = 'absolute';
    el.style.width  = overlay.width  + 'px';
    el.style.height = overlay.height + 'px';
    el.style.outline = '1px solid rgba(34, 211, 238, 0.4)';
    el.style.outlineOffset = '0';
    el.style.boxSizing = 'content-box';
    applyAnchor(el, a, overlay.offset_x | 0, overlay.offset_y | 0);

    const iframe = document.createElement('iframe');
    iframe.style.width  = '100%';
    iframe.style.height = '100%';
    iframe.style.border = 'none';
    iframe.style.background = 'transparent';
    iframe.style.opacity = overlay.opacity == null ? 1 : overlay.opacity;
    iframe.setAttribute('allowtransparency', 'true');
    iframe.setAttribute('frameborder', '0');
    iframe.src = '/plugins/' + plugin.name + '/' + overlay.file +
                 '?overlay=1&anchor=' + encodeURIComponent(a);
    el.appendChild(iframe);

    // Transparent capture div on top of the iframe — intercepts every
    // pointer event so plugin code never sees clicks during edit mode,
    // and so we can implement drag/select on the wrapper.
    const capture = document.createElement('div');
    capture.style.position = 'absolute';
    capture.style.inset = '0';
    capture.style.cursor = 'move';
    el.appendChild(capture);

    // Plugin-name badge in the top-left corner of the widget.
    const badge = document.createElement('div');
    badge.textContent = plugin.title || plugin.name;
    badge.style.position = 'absolute';
    badge.style.top = '0';
    badge.style.left = '0';
    badge.style.padding = '2px 6px';
    badge.style.font = '600 11px Inter,system-ui,sans-serif';
    badge.style.color = '#0a0c14';
    badge.style.background = 'rgba(34, 211, 238, 0.85)';
    badge.style.letterSpacing = '0.04em';
    badge.style.pointerEvents = 'none';
    el.appendChild(badge);

    document.body.appendChild(el);
    return { plugin, overlay, el, iframe, capture, badge };
  }

  // applyAnchor sets the four positional CSS properties on `el` so that
  // the widget sits `ox`/`oy` pixels away from the corner named by
  // `anchor`. The opposite corners are cleared so the new anchor wins
  // when switching from one corner to another.
  function applyAnchor(el, anchor, ox, oy) {
    el.style.top = el.style.bottom = el.style.left = el.style.right = '';
    if (anchor.indexOf('top')    === 0) el.style.top    = oy + 'px';
    else                                el.style.bottom = oy + 'px';
    if (anchor.indexOf('-left')  >= 0)  el.style.left   = ox + 'px';
    else                                el.style.right  = ox + 'px';
  }

  console.log('[overlay-editor] rendered', widgets.length, 'widget(s)');
})();
