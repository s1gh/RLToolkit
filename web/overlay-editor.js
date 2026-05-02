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

    // Resize handle in the bottom-right corner. Only visible when
    // selected — handled by toggling its display in select().
    const resize = document.createElement('div');
    resize.style.position = 'absolute';
    resize.style.right = '0';
    resize.style.bottom = '0';
    resize.style.width = '12px';
    resize.style.height = '12px';
    resize.style.background = 'rgba(34, 211, 238, 1)';
    resize.style.cursor = 'nwse-resize';
    resize.style.display = 'none';
    el.appendChild(resize);

    document.body.appendChild(el);
    return { plugin, overlay, el, iframe, capture, badge, resize };
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
    if (selected) {
      selected.el.style.outline = '1px solid rgba(34, 211, 238, 0.4)';
      selected.resize.style.display = 'none';
    }
    selected = w;
    if (selected) {
      selected.el.style.outline = '2px solid rgba(34, 211, 238, 1)';
      selected.resize.style.display = 'block';
    }
    renderPanel();
  }

  for (const w of widgets) {
    w.capture.addEventListener('pointerdown', (e) => {
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
    // should DECREASE offset_x (offset is measured from the right edge).
    // Same for the y axis with bottom anchors.
    const sx = (anchor.indexOf('-left') >= 0) ? +1 : -1;
    const sy = (anchor.indexOf('top')   === 0) ? +1 : -1;

    // Capture the pointer on the widget itself so subsequent pointermove /
    // pointerup events route here regardless of where the cursor goes —
    // off-window, over the iframe, anywhere. Without this, releasing the
    // mouse outside the browser viewport leaks the move listener and the
    // drag becomes "stuck" until the next click.
    const target = downEv.currentTarget;
    const pointerId = downEv.pointerId;
    try { target.setPointerCapture(pointerId); } catch (_) {}

    // Token used to detect a stale rollback: if a second drag starts on
    // this widget before this drag's save resolves, w._dragToken changes,
    // and our save .catch ignores its own rollback.
    const token = (w._dragToken = (w._dragToken | 0) + 1);

    function move(ev) {
      if (ev.pointerId !== pointerId) return;
      ev.preventDefault();
      const nx = clamp(startOX + sx * (ev.clientX - startX), 0);
      const ny = clamp(startOY + sy * (ev.clientY - startY), 0);
      w.overlay.offset_x = nx;
      w.overlay.offset_y = ny;
      applyAnchor(w.el, anchor, nx, ny);
    }
    function end(ev) {
      if (ev.pointerId !== pointerId) return;
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', end);
      target.removeEventListener('pointercancel', end);
      try { target.releasePointerCapture(pointerId); } catch (_) {}

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
        // Skip the rollback if the user already started another drag on
        // this widget — applying our stale start values would clobber
        // their in-progress work.
        if (w._dragToken !== token) return;
        w.overlay.offset_x = startOX;
        w.overlay.offset_y = startOY;
        applyAnchor(w.el, anchor, startOX, startOY);
        toast('Save failed: ' + err.message);
      });
    }
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', end);
    target.addEventListener('pointercancel', end);
  }

  // ─── Drag to resize ──────────────────────────────────────
  for (const w of widgets) {
    w.resize.addEventListener('pointerdown', (e) => {
      e.preventDefault();
      e.stopPropagation();   // don't also trigger the widget's drag handler
      select(w);
      startResize(w, e);
    });
  }

  function startResize(w, downEv) {
    const startX = downEv.clientX;
    const startY = downEv.clientY;
    const startW = w.el.offsetWidth;
    const startH = w.el.offsetHeight;
    const target = downEv.currentTarget;
    const pointerId = downEv.pointerId;
    try { target.setPointerCapture(pointerId); } catch (_) {}
    const token = (w._resizeToken = (w._resizeToken | 0) + 1);

    function move(ev) {
      if (ev.pointerId !== pointerId) return;
      ev.preventDefault();
      const nw = clamp(startW + (ev.clientX - startX), 16);
      const nh = clamp(startH + (ev.clientY - startY), 16);
      w.overlay.width  = nw;
      w.overlay.height = nh;
      w.el.style.width  = nw + 'px';
      w.el.style.height = nh + 'px';
    }
    function end(ev) {
      if (ev.pointerId !== pointerId) return;
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', end);
      target.removeEventListener('pointercancel', end);
      try { target.releasePointerCapture(pointerId); } catch (_) {}

      if (!ev.shiftKey) {
        w.overlay.width  = Math.round(w.overlay.width  / SNAP) * SNAP;
        w.overlay.height = Math.round(w.overlay.height / SNAP) * SNAP;
        w.el.style.width  = w.overlay.width  + 'px';
        w.el.style.height = w.overlay.height + 'px';
      }
      saveOverride(w, {
        width:  w.overlay.width,
        height: w.overlay.height,
      }).catch((err) => {
        if (w._resizeToken !== token) return;
        w.overlay.width  = startW;
        w.overlay.height = startH;
        w.el.style.width  = startW + 'px';
        w.el.style.height = startH + 'px';
        toast('Save failed: ' + err.message);
      });
    }
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', end);
    target.addEventListener('pointercancel', end);
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

  // ─── Floating control panel ──────────────────────────────
  // Single panel that rebinds itself to the selected widget. Lives in
  // the top-right of the viewport so it never overlaps a widget that's
  // anchored bottom-left.
  const panel = document.createElement('div');
  panel.style.cssText =
    'position:fixed;top:48px;right:16px;width:240px;' +
    'background:#161b2c;border:1px solid #232a44;border-radius:10px;' +
    'padding:14px;color:#e6e9f5;font:500 12px Inter,system-ui,sans-serif;' +
    'box-shadow:0 8px 30px rgba(0,0,0,.4);z-index:50;display:none';
  document.body.appendChild(panel);

  function renderPanel() {
    if (!selected) { panel.style.display = 'none'; return; }
    panel.style.display = 'block';
    const o = selected.overlay;
    const a = o.anchor || 'top-right';
    panel.innerHTML =
      '<div style="font:700 11px Inter,sans-serif;letter-spacing:.08em;text-transform:uppercase;color:#22d3ee;margin-bottom:10px">' +
        escapeHtml(selected.plugin.title || selected.plugin.name) +
      '</div>' +

      '<div style="margin-bottom:10px">Anchor</div>' +
      '<div data-role="anchors" style="display:grid;grid-template-columns:1fr 1fr;gap:6px;margin-bottom:14px">' +
        anchorBtn('top-left',     a) +
        anchorBtn('top-right',    a) +
        anchorBtn('bottom-left',  a) +
        anchorBtn('bottom-right', a) +
      '</div>' +

      '<div style="display:grid;grid-template-columns:auto 1fr;gap:8px;align-items:center;margin-bottom:14px">' +
        '<label>Width</label>'  + numInput('width',  o.width)  +
        '<label>Height</label>' + numInput('height', o.height) +
      '</div>' +

      '<div style="margin-bottom:6px">Opacity <span data-role="opacity-val">' + (o.opacity == null ? 1 : o.opacity).toFixed(2) + '</span></div>' +
      '<input data-role="opacity" type="range" min="0" max="1" step="0.01" value="' +
        (o.opacity == null ? 1 : o.opacity) + '" style="width:100%;margin-bottom:14px">' +

      '<button data-role="reset" style="' +
        'width:100%;padding:8px;background:#1d2238;color:#a9b0cf;' +
        'border:1px solid #232a44;border-radius:6px;cursor:pointer;' +
        'font:600 11px Inter,sans-serif;letter-spacing:.05em;text-transform:uppercase">' +
        'Reset to manifest' +
      '</button>';

    wirePanel(selected);
  }

  function anchorBtn(name, current) {
    const active = name === current;
    return '<button data-anchor="' + name + '" style="' +
      'padding:6px;background:' + (active ? '#22d3ee' : '#1d2238') + ';' +
      'color:' + (active ? '#0a0c14' : '#a9b0cf') + ';' +
      'border:1px solid ' + (active ? '#22d3ee' : '#232a44') + ';' +
      'border-radius:6px;cursor:pointer;font:600 10px Inter,sans-serif;' +
      'letter-spacing:.05em;text-transform:uppercase' +
    '">' + name.replace('-', ' ') + '</button>';
  }

  function numInput(role, value) {
    return '<input data-role="' + role + '" type="number" min="0" value="' + (value | 0) + '" style="' +
      'width:100%;padding:6px 8px;background:#0f1320;color:#e6e9f5;' +
      'border:1px solid #232a44;border-radius:6px;font:500 12px JetBrains Mono,monospace">';
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
  }

  function wirePanel(w) {
    panel.querySelectorAll('[data-anchor]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const next = btn.getAttribute('data-anchor');
        const prev = w.overlay.anchor || 'top-right';
        if (next === prev) return;
        // Recompute offsets so the widget visually stays in place under
        // the new anchor. Read the live viewport so the math works
        // regardless of editor browser size.
        const r = w.el.getBoundingClientRect();
        let nx, ny;
        if (next.indexOf('-left') >= 0) nx = Math.max(0, Math.round(r.left));
        else                            nx = Math.max(0, Math.round(window.innerWidth  - r.right));
        if (next.indexOf('top')   === 0) ny = Math.max(0, Math.round(r.top));
        else                             ny = Math.max(0, Math.round(window.innerHeight - r.bottom));
        w.overlay.anchor   = next;
        w.overlay.offset_x = nx;
        w.overlay.offset_y = ny;
        applyAnchor(w.el, next, nx, ny);
        saveOverride(w, { anchor: next, offset_x: nx, offset_y: ny })
          .catch((err) => toast('Save failed: ' + err.message));
        renderPanel();
      });
    });

    const wInput = panel.querySelector('[data-role="width"]');
    const hInput = panel.querySelector('[data-role="height"]');
    wInput.addEventListener('change', () => commitSize(w, +wInput.value, w.overlay.height));
    hInput.addEventListener('change', () => commitSize(w, w.overlay.width, +hInput.value));

    const op = panel.querySelector('[data-role="opacity"]');
    const opVal = panel.querySelector('[data-role="opacity-val"]');
    op.addEventListener('input', () => {
      // Live preview while sliding; persist on release (change).
      const v = +op.value;
      w.overlay.opacity = v;
      w.iframe.style.opacity = v;
      opVal.textContent = v.toFixed(2);
    });
    op.addEventListener('change', () => {
      saveOverride(w, { opacity: +op.value })
        .catch((err) => toast('Save failed: ' + err.message));
    });

    panel.querySelector('[data-role="reset"]').addEventListener('click', () => resetWidget(w));
  }

  function commitSize(w, width, height) {
    width  = Math.max(0, width  | 0);
    height = Math.max(0, height | 0);
    w.overlay.width  = width;
    w.overlay.height = height;
    w.el.style.width  = width + 'px';
    w.el.style.height = height + 'px';
    saveOverride(w, { width, height })
      .catch((err) => toast('Save failed: ' + err.message));
  }

  async function resetWidget(w) {
    try {
      const r = await fetch('/api/overlay/overrides/' + encodeURIComponent(w.plugin.name), {
        method: 'DELETE',
      });
      if (!r.ok) throw new Error('HTTP ' + r.status);
    } catch (err) {
      toast('Reset failed: ' + err.message);
      return;
    }
    // Reload the page so the manifest defaults flow back through the
    // normal merge path; avoids us re-implementing the merge logic
    // client-side and duplicating drift risk.
    location.reload();
  }

  console.log('[overlay-editor] rendered', widgets.length, 'widget(s)');
})();
