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

  // ─── Selection ────────────────────────────────────────────
  // Single-select: clicking a widget's capture div selects it; clicking
  // outside any widget deselects. Selected widget gets a brighter outline.
  let selected = null;

  function select(w) {
    if (selected === w) return;
    if (selected) selected.el.style.outline = '1px solid rgba(34, 211, 238, 0.4)';
    selected = w;
    if (selected) selected.el.style.outline = '2px solid rgba(34, 211, 238, 1)';
  }

  for (const w of widgets) {
    w.capture.addEventListener('mousedown', (e) => {
      e.preventDefault();
      select(w);
      startDrag(w, e);
    });
  }
  document.addEventListener('mousedown', (e) => {
    // Click outside any widget capture (e.g., on the body) → deselect.
    if (e.target === document.body || e.target === document.documentElement) {
      select(null);
    }
  });

  // ─── Drag to move ─────────────────────────────────────────
  // Mouse-down on a widget's capture starts a drag. We mutate `w.overlay`
  // in place during drag so the on-screen position tracks the cursor; on
  // mouse-up we snap to the 8px grid (Shift to bypass) and PUT to the API.
  const SNAP = 8;

  function startDrag(w, downEv) {
    const startX = downEv.clientX;
    const startY = downEv.clientY;
    const startOX = w.overlay.offset_x | 0;
    const startOY = w.overlay.offset_y | 0;
    const anchor = w.overlay.anchor || 'top-right';
    // x grows toward right; for right-anchored widgets, dragging right
    // increases offset_x is wrong — it should DECREASE offset_x. Same
    // for the y axis with bottom anchors. Sign multipliers handle that.
    const sx = (anchor.indexOf('-left') >= 0) ? +1 : -1;
    const sy = (anchor.indexOf('top')   === 0) ? +1 : -1;

    function move(ev) {
      ev.preventDefault();
      const nx = clamp(startOX + sx * (ev.clientX - startX), 0);
      const ny = clamp(startOY + sy * (ev.clientY - startY), 0);
      w.overlay.offset_x = nx;
      w.overlay.offset_y = ny;
      applyAnchor(w.el, anchor, nx, ny);
    }
    function up(ev) {
      document.removeEventListener('mousemove', move);
      document.removeEventListener('mouseup', up);
      // Snap on release (unless Shift held), then persist.
      if (!ev.shiftKey) {
        w.overlay.offset_x = Math.round(w.overlay.offset_x / SNAP) * SNAP;
        w.overlay.offset_y = Math.round(w.overlay.offset_y / SNAP) * SNAP;
        applyAnchor(w.el, anchor, w.overlay.offset_x, w.overlay.offset_y);
      }
      saveOverride(w, {
        offset_x: w.overlay.offset_x,
        offset_y: w.overlay.offset_y,
      }).catch((err) => {
        // Roll back on failure: restore start position and surface the error.
        w.overlay.offset_x = startOX;
        w.overlay.offset_y = startOY;
        applyAnchor(w.el, anchor, startOX, startOY);
        toast('Save failed: ' + err.message);
      });
    }
    document.addEventListener('mousemove', move);
    document.addEventListener('mouseup', up);
  }

  function clamp(v, min) { return v < min ? min : v; }

  // ─── Persistence ──────────────────────────────────────────
  async function saveOverride(w, partial) {
    const r = await fetch('/api/overlay/overrides/' + encodeURIComponent(w.plugin.name), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(partial),
    });
    if (!r.ok) throw new Error('HTTP ' + r.status + ': ' + (await r.text()).trim());
  }

  // ─── Toast ────────────────────────────────────────────────
  // Tiny ephemeral notification at the top of the screen for save failures.
  function toast(msg) {
    let t = document.getElementById('__rlt_editor_toast');
    if (!t) {
      t = document.createElement('div');
      t.id = '__rlt_editor_toast';
      t.style.cssText =
        'position:fixed;top:48px;left:50%;transform:translateX(-50%);' +
        'background:#7f1d1d;color:#fee2e2;padding:8px 14px;border-radius:6px;' +
        'font:600 12px Inter,system-ui,sans-serif;letter-spacing:.04em;' +
        'box-shadow:0 8px 30px rgba(0,0,0,.4);z-index:9999;opacity:0;' +
        'transition:opacity .15s';
      document.body.appendChild(t);
    }
    t.textContent = msg;
    requestAnimationFrame(() => { t.style.opacity = '1'; });
    clearTimeout(t._timer);
    t._timer = setTimeout(() => { t.style.opacity = '0'; }, 3000);
  }

  console.log('[overlay-editor] rendered', widgets.length, 'widget(s)');
})();
