// RL Toolkit — Overlay Editor
//
// Loaded by web/overlay.html when ?edit=1 is present in the URL. The host
// page has already fetched /api/plugins and /api/overlay/overrides and
// merged them into window.__rltOverlayContext. This script takes over
// rendering: production-style iframes for the live preview, plus edit
// chrome (outlines, badges, capture divs that block iframe mouse events).
(async function () {
  // Loaded as a classic <script>, not an ES module — strict mode is opt-in.
  // biome-ignore lint/suspicious/noRedundantUseStrict: classic script
  'use strict';
  const ctx = window.__rltOverlayContext;
  if (!ctx) {
    console.error('[overlay-editor] missing __rltOverlayContext');
    return;
  }

  // ─── Target surface ──────────────────────────────────────
  // Represents the production overlay's pixel dimensions. Configured
  // server-side (dashboard) with a fallback to rl-widget's reported
  // monitor size or 1920×1080. The editor renders a canvas of exactly
  // these dimensions and scales it to fit the viewport — so a widget at
  // (0, 0) in the editor lands at (0, 0) on the production overlay
  // regardless of which window/monitor either is in.
  let surface = {
    configured: null,
    detected: null,
    effective: { width: 1920, height: 1080 },
  };
  let canvasScale = 1;

  // Defensive: validate the server's response shape so a backend
  // regression or partial deploy can't NaN-poison drag math. Returns
  // the input unchanged when valid; otherwise the fallback surface.
  function normalizeSurface(s) {
    const e = s?.effective;
    if (
      !e ||
      !Number.isFinite(e.width) ||
      !Number.isFinite(e.height) ||
      e.width <= 0 ||
      e.height <= 0
    ) {
      return { configured: null, detected: null, effective: { width: 1920, height: 1080 } };
    }
    return s;
  }

  async function loadSurface() {
    try {
      const r = await fetch('/api/overlay/surface');
      if (r.ok) {
        surface = normalizeSurface(await r.json());
      }
    } catch (err) {
      console.warn('[overlay-editor] surface fetch failed; using fallback', err);
    }
  }

  await loadSurface();

  // Make the edit page non-transparent so widget chrome is readable
  // against something. The production overlay is intentionally see-
  // through; the editor is its own thing.
  document.documentElement.style.background = '#0a0c14';
  document.body.style.background = '#0a0c14';

  // ─── Top bar ──────────────────────────────────────────────
  // Sticky bar across the viewport. Overlays the canvas (z-index 100)
  // rather than displacing it, so edit-mode widget positions stay
  // visually identical to production — top-anchored widgets that
  // happen to sit under the bar can be dragged out of the way.
  const topbar = document.createElement('div');
  topbar.style.cssText =
    'position:fixed;top:0;left:0;right:0;height:32px;' +
    'display:flex;align-items:center;gap:12px;padding:0 14px;' +
    'background:rgba(15,19,32,0.95);border-bottom:1px solid #232a44;' +
    'color:#a9b0cf;font:600 11px Inter,system-ui,sans-serif;' +
    'letter-spacing:.05em;z-index:100;transition:opacity .1s';
  topbar.innerHTML =
    '<span style="color:#22d3ee;text-transform:uppercase">Overlay editor</span>' +
    '<span style="opacity:.6">Drag to position · Drop to save</span>' +
    '<span style="flex:1"></span>' +
    '<span style="opacity:.6;text-transform:none;letter-spacing:.02em;font-weight:500">8px grid · hold Shift for fine adjust</span>' +
    '<span data-role="target-label" style="opacity:.7;text-transform:none;letter-spacing:.02em;font-weight:500;margin-left:12px"></span>' +
    '<button data-role="reset-all" style="' +
    'padding:6px 12px;background:#1d2238;color:#a9b0cf;' +
    'border:1px solid #232a44;border-radius:6px;cursor:pointer;' +
    'font:600 10px Inter,sans-serif;letter-spacing:.05em;text-transform:uppercase' +
    '">Reset all</button>';
  document.body.appendChild(topbar);
  const topbarTarget = topbar.querySelector('[data-role="target-label"]');

  // Resolve manifest defaults for a plugin so reset paths can send a
  // deterministic PUT body. ctx.plugins is the raw /api/plugins array;
  // each entry's `overlay` block holds the manifest values before any
  // user override is layered on. Falls back to safe defaults so a
  // manifest missing fields doesn't crash the reset flow.
  function manifestDefaultsFor(plugin) {
    const p = ctx.plugins.find((p) => p.name === plugin.name);
    const o = p?.overlay || {};
    return {
      anchor: o.anchor || 'top-right',
      offset_x: o.offset_x | 0,
      offset_y: o.offset_y | 0,
      width: o.width | 0 || 320,
      height: o.height | 0 || 120,
      opacity: o.opacity == null ? 1 : o.opacity,
    };
  }

  topbar.querySelector('[data-role="reset-all"]').addEventListener('click', async () => {
    const ok = await confirmModal({
      title: 'Reset all widgets?',
      message:
        'Every widget returns to the position, size, anchor, and opacity declared in its manifest. ' +
        'Plugin-level enabled/disabled state is preserved.',
      confirmLabel: 'Reset all',
      destructive: true,
    });
    if (!ok) return;
    try {
      // Sequential PUTs — N is small (one per plugin) so a parallel
      // burst gains nothing and would muddle error reporting. PUT
      // (not DELETE) so production /overlay still sees enabled:true
      // and keeps each iframe mounted.
      for (const w of widgets) {
        const m = manifestDefaultsFor(w.plugin);
        const body = {
          enabled: true,
          anchor: m.anchor,
          offset_x: m.offset_x,
          offset_y: m.offset_y,
          width: m.width,
          height: m.height,
          opacity: m.opacity,
        };
        const r = await fetch('/api/overlay/overrides/' + encodeURIComponent(w.plugin.name), {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) throw new Error(w.plugin.name + ' → HTTP ' + r.status);
      }
      location.reload();
    } catch (err) {
      toast('Reset all failed: ' + err.message);
    }
  });

  // ─── Canvas ──────────────────────────────────────────────
  // The canvas div is sized in target-surface pixels (Wt × Ht) and CSS-
  // scaled to fit the viewport. Every widget element is parented here,
  // not on body, so a single transform on the canvas scales the entire
  // edit world. The transform doesn't reflow surrounding layout, so we
  // also offset top/left manually to keep the scaled canvas centered
  // beneath the topbar.
  const canvas = document.createElement('div');
  canvas.id = '__rlt_canvas';
  canvas.style.cssText =
    'position:absolute;top:0;left:0;' +
    'background:rgba(15,19,32,0.35);' +
    'outline:1px solid rgba(34,211,238,0.5);' +
    'transform-origin:top left';
  document.body.appendChild(canvas);

  function applyCanvasLayout() {
    const W = surface.effective.width;
    const H = surface.effective.height;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    // Reserve 32px for the topbar and 32px of breathing room at the
    // bottom so the canvas's outline stays visible on both edges. Without
    // a bottom margin a height-limited canvas lands flush against the
    // viewport edge and the 1px bottom outline gets clipped.
    const TOP_MARGIN = 32;
    const BOTTOM_MARGIN = 32;
    const availW = window.innerWidth;
    const availH = Math.max(0, window.innerHeight - TOP_MARGIN - BOTTOM_MARGIN);
    const k = Math.min(availW / W, availH / H, 1);
    canvasScale = k;
    canvas.style.transform = 'scale(' + k + ')';
    // Center the (scaled) canvas in the available viewport area.
    const scaledW = W * k;
    const scaledH = H * k;
    const offX = Math.max(0, Math.round((availW - scaledW) / 2));
    const offY = Math.max(0, Math.round((availH - scaledH) / 2)) + TOP_MARGIN;
    canvas.style.left = offX + 'px';
    canvas.style.top = offY + 'px';
    updateTopbarLabel();
    flagOffCanvasWidgets();
  }

  function updateTopbarLabel() {
    const W = surface.effective.width;
    const H = surface.effective.height;
    const pct = Math.round(canvasScale * 100);
    topbarTarget.textContent = 'Target: ' + W + ' × ' + H + ' @ ' + pct + '%';
  }

  // Highlight widgets whose current offset+size puts them outside the
  // canvas. Common after the user shrinks the target — silent auto-move
  // would be worse than the visible warning. The clamp on drag still
  // applies, so any user-initiated move pulls the widget back into bounds.
  function flagOffCanvasWidgets() {
    const W = surface.effective.width;
    const H = surface.effective.height;
    for (const w of widgets) {
      const ox = w.overlay.offset_x | 0;
      const oy = w.overlay.offset_y | 0;
      const wW = w.overlay.width | 0;
      const wH = w.overlay.height | 0;
      const off = ox < 0 || oy < 0 || ox + wW > W || oy + wH > H;
      // Use a different outline color for off-canvas widgets, persisting
      // across the selected/unselected outline updates by checking here
      // whenever we re-layout.
      if (off) {
        w.el.style.outline = '2px dashed #f97316'; // orange
      } else if (w === selected) {
        w.el.style.outline = '2px solid rgba(34, 211, 238, 1)';
      } else {
        w.el.style.outline = '1px solid rgba(34, 211, 238, 0.4)';
      }
    }
  }

  // Per-widget state. `el` is the wrapper div that owns positioning;
  // `iframe` is the live preview; `capture` is the transparent overlay
  // that intercepts mouse events so iframe content never sees them.
  // `overlay` is the live override values (with manifest defaults filled
  // in) — mutated by drag/resize/anchor/opacity changes.
  // Skip plugins the user has disabled — they're managed from the
  // dashboard, not the editor. Re-enabling on the dashboard reloads
  // the editor naturally (the SSE-driven reflow only affects the
  // production /overlay page, not this editor session).
  const widgets = ctx.merged
    .filter(({ overlay }) => overlay.enabled === true)
    .map(({ plugin, overlay }) => buildWidget(plugin, overlay));

  // Declared before the first applyCanvasLayout() so flagOffCanvasWidgets
  // (which closes over `selected`) doesn't trip the TDZ on boot. The
  // matching select() function lives further down where the rest of
  // selection-handling code lives.
  let selected = null;

  applyCanvasLayout();
  window.addEventListener('resize', applyCanvasLayout);

  function buildWidget(plugin, overlay) {
    const a = overlay.anchor || 'top-right';
    const el = document.createElement('div');
    el.style.position = 'absolute';
    el.style.width = overlay.width + 'px';
    el.style.height = overlay.height + 'px';
    el.style.outline = '1px solid rgba(34, 211, 238, 0.4)';
    el.style.outlineOffset = '0';
    el.style.boxSizing = 'content-box';
    applyAnchor(el, a, overlay.offset_x | 0, overlay.offset_y | 0);

    const iframe = document.createElement('iframe');
    iframe.style.width = '100%';
    iframe.style.height = '100%';
    iframe.style.border = 'none';
    iframe.style.background = 'transparent';
    iframe.style.opacity = overlay.opacity == null ? 1 : overlay.opacity;
    iframe.setAttribute('allowtransparency', 'true');
    iframe.setAttribute('frameborder', '0');
    iframe.src =
      '/plugins/' + plugin.name + '/' + overlay.file + '?overlay=1&anchor=' + encodeURIComponent(a);
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

    // Resize handle on the corner opposite the anchor — i.e., the corner
    // that's free to move when the widget grows. For a top-right widget
    // the right edge is pinned, so the handle goes on the LEFT-bottom;
    // for bottom-right both right and bottom are pinned, handle goes
    // top-left; etc. Re-positioned by positionResizeHandle whenever the
    // anchor changes. Cursor is anchor-aware too.
    const resize = document.createElement('div');
    resize.style.position = 'absolute';
    resize.style.width = '12px';
    resize.style.height = '12px';
    resize.style.background = 'rgba(34, 211, 238, 1)';
    resize.style.display = 'none';
    el.appendChild(resize);
    positionResizeHandle(resize, a);

    canvas.appendChild(el);
    return { plugin, overlay, el, iframe, capture, badge, resize };
  }

  // applyAnchor sets the four positional CSS properties on `el` so that
  // the widget sits `ox`/`oy` pixels away from the corner named by
  // `anchor`. The opposite corners are cleared so the new anchor wins
  // when switching from one corner to another.
  function applyAnchor(el, anchor, ox, oy) {
    el.style.top = el.style.bottom = el.style.left = el.style.right = '';
    if (anchor.indexOf('top') === 0) el.style.top = oy + 'px';
    else el.style.bottom = oy + 'px';
    if (anchor.indexOf('-left') >= 0) el.style.left = ox + 'px';
    else el.style.right = ox + 'px';
  }

  // positionResizeHandle places the handle on the corner opposite the
  // anchor — the only corner that can actually grow when the widget
  // resizes. Also picks the appropriate diagonal-resize cursor.
  function positionResizeHandle(handle, anchor) {
    handle.style.top = handle.style.bottom = handle.style.left = handle.style.right = '';
    const onTop = anchor.indexOf('top') === 0;
    const onLeft = anchor.indexOf('-left') >= 0;
    if (onTop) handle.style.bottom = '0';
    else handle.style.top = '0';
    if (onLeft) handle.style.right = '0';
    else handle.style.left = '0';
    // Cursor matches the diagonal the handle sits on:
    //   top-left + handle on bottom-right     → nwse-resize
    //   top-right + handle on bottom-left     → nesw-resize
    //   bottom-left + handle on top-right     → nesw-resize
    //   bottom-right + handle on top-left     → nwse-resize
    const cursor = onTop === onLeft ? 'nwse-resize' : 'nesw-resize';
    handle.style.cursor = cursor;
  }

  // ─── Selection ────────────────────────────────────────────
  // Single-select: clicking a widget's capture div selects it; clicking
  // outside any widget deselects. Selected widget gets a brighter outline.
  // (`selected` is declared higher up, before applyCanvasLayout's first
  // call, so flagOffCanvasWidgets can read it on boot without TDZ.)

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
    flagOffCanvasWidgets();
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
    const sx = anchor.indexOf('-left') >= 0 ? +1 : -1;
    const sy = anchor.indexOf('top') === 0 ? +1 : -1;

    // Capture the pointer on the widget itself so subsequent pointermove /
    // pointerup events route here regardless of where the cursor goes —
    // off-window, over the iframe, anywhere. Without this, releasing the
    // mouse outside the browser viewport leaks the move listener and the
    // drag becomes "stuck" until the next click.
    const target = downEv.currentTarget;
    const pointerId = downEv.pointerId;
    try {
      target.setPointerCapture(pointerId);
    } catch (_) {}
    setChromeFaded(true);

    // Token used to detect a stale rollback: if a second drag starts on
    // this widget before this drag's save resolves, w._dragToken changes,
    // and our save .catch ignores its own rollback.
    w._dragToken = (w._dragToken | 0) + 1;
    const token = w._dragToken;

    function move(ev) {
      if (ev.pointerId !== pointerId) return;
      ev.preventDefault();
      // Pointer deltas are in client (CSS) pixels. The widget lives inside
      // a canvas scaled by canvasScale, so 1 client pixel = 1/k canvas px.
      const dxCanvas = (ev.clientX - startX) / canvasScale;
      const dyCanvas = (ev.clientY - startY) / canvasScale;
      const W = surface.effective.width;
      const H = surface.effective.height;
      const wW = w.el.offsetWidth;
      const wH = w.el.offsetHeight;
      const nx = clampRange(startOX + sx * dxCanvas, 0, Math.max(0, W - wW));
      const ny = clampRange(startOY + sy * dyCanvas, 0, Math.max(0, H - wH));
      w.overlay.offset_x = nx;
      w.overlay.offset_y = ny;
      applyAnchor(w.el, anchor, nx, ny);
    }
    function end(ev) {
      if (ev.pointerId !== pointerId) return;
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', end);
      target.removeEventListener('pointercancel', end);
      try {
        target.releasePointerCapture(pointerId);
      } catch (_) {}
      setChromeFaded(false);

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
      e.stopPropagation(); // don't also trigger the widget's drag handler
      select(w);
      startResize(w, e);
    });
  }

  function startResize(w, downEv) {
    const startX = downEv.clientX;
    const startY = downEv.clientY;
    const startW = w.el.offsetWidth;
    const startH = w.el.offsetHeight;
    const anchor = w.overlay.anchor || 'top-right';
    // The handle sits on the anchor-opposite corner, so the cursor's
    // direction-of-growth matches the anchor: a left-anchored widget
    // grows when the cursor moves +x (handle on right edge), a right-
    // anchored widget grows when the cursor moves -x (handle on left
    // edge). Same logic for the y axis. Same sign multipliers as
    // startDrag uses for offsets.
    const sx = anchor.indexOf('-left') >= 0 ? +1 : -1;
    const sy = anchor.indexOf('top') === 0 ? +1 : -1;
    const target = downEv.currentTarget;
    const pointerId = downEv.pointerId;
    try {
      target.setPointerCapture(pointerId);
    } catch (_) {}
    setChromeFaded(true);
    w._resizeToken = (w._resizeToken | 0) + 1;
    const token = w._resizeToken;

    function move(ev) {
      if (ev.pointerId !== pointerId) return;
      ev.preventDefault();
      const dxCanvas = (ev.clientX - startX) / canvasScale;
      const dyCanvas = (ev.clientY - startY) / canvasScale;
      const W = surface.effective.width;
      const H = surface.effective.height;
      // Resize bounds: widget can't grow past the canvas edge along the
      // anchor's free axis. offset_x is fixed during resize; max width is
      // W - offset_x along the free axis (and analogously for height).
      const ox = w.overlay.offset_x | 0;
      const oy = w.overlay.offset_y | 0;
      const nw = clampRange(startW + sx * dxCanvas, 16, Math.max(16, W - ox));
      const nh = clampRange(startH + sy * dyCanvas, 16, Math.max(16, H - oy));
      w.overlay.width = nw;
      w.overlay.height = nh;
      w.el.style.width = nw + 'px';
      w.el.style.height = nh + 'px';
    }
    function end(ev) {
      if (ev.pointerId !== pointerId) return;
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', end);
      target.removeEventListener('pointercancel', end);
      try {
        target.releasePointerCapture(pointerId);
      } catch (_) {}
      setChromeFaded(false);

      if (!ev.shiftKey) {
        w.overlay.width = Math.round(w.overlay.width / SNAP) * SNAP;
        w.overlay.height = Math.round(w.overlay.height / SNAP) * SNAP;
        w.el.style.width = w.overlay.width + 'px';
        w.el.style.height = w.overlay.height + 'px';
      }
      saveOverride(w, {
        width: w.overlay.width,
        height: w.overlay.height,
      }).catch((err) => {
        if (w._resizeToken !== token) return;
        w.overlay.width = startW;
        w.overlay.height = startH;
        w.el.style.width = startW + 'px';
        w.el.style.height = startH + 'px';
        toast('Save failed: ' + err.message);
      });
    }
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', end);
    target.addEventListener('pointercancel', end);
  }

  function clampRange(v, lo, hi) {
    if (v < lo) return lo;
    if (v > hi) return hi;
    return v;
  }

  // Fade chrome (topbar + properties panel) during drag/resize so they
  // don't block the canvas corners the user is trying to reach. Both
  // stop intercepting pointer events while faded so a drag-onto-them
  // can't be hijacked.
  function setChromeFaded(faded) {
    const op = faded ? '0.15' : '';
    const pe = faded ? 'none' : '';
    topbar.style.opacity = op;
    topbar.style.pointerEvents = pe;
    panel.style.opacity = op;
    panel.style.pointerEvents = pe;
  }

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
  // Promise-based confirm dialog matching the editor's cyan-on-dark
  // aesthetic. Native confirm() pops a chrome-bordered alert that looks
  // out of place against the overlay canvas. Esc cancels, Enter confirms,
  // backdrop click cancels. Cancel is focused by default (safer for
  // destructive actions).
  function confirmModal({ title, message, confirmLabel = 'Confirm', destructive = false } = {}) {
    return new Promise((resolve) => {
      const backdrop = document.createElement('div');
      backdrop.style.cssText =
        'position:fixed;inset:0;background:rgba(5,7,14,.72);' +
        'backdrop-filter:blur(2px);z-index:10000;' +
        'display:flex;align-items:center;justify-content:center;' +
        'opacity:0;transition:opacity .12s';

      const accent = destructive ? '#f472b6' : '#22d3ee';
      const accentGlow = destructive ? 'rgba(244,114,182,.35)' : 'rgba(34,211,238,.35)';

      const card = document.createElement('div');
      card.style.cssText =
        'min-width:320px;max-width:440px;padding:20px 22px 18px;' +
        'background:linear-gradient(180deg,#0f1320,#161b2c);' +
        'border:1px solid #232a44;border-radius:10px;' +
        'box-shadow:0 24px 60px rgba(0,0,0,.55),0 0 24px ' +
        accentGlow +
        ';' +
        'color:#e6e9f5;font:500 13px Inter,system-ui,sans-serif;' +
        'transform:translateY(6px);transition:transform .12s';

      const titleEl = document.createElement('div');
      titleEl.textContent = title;
      titleEl.style.cssText =
        'font:700 11px Inter,system-ui,sans-serif;' +
        'letter-spacing:.14em;text-transform:uppercase;' +
        'color:' +
        accent +
        ';margin-bottom:10px';

      const msgEl = document.createElement('div');
      msgEl.textContent = message;
      msgEl.style.cssText = 'line-height:1.55;color:#a9b0cf;margin-bottom:18px';

      const row = document.createElement('div');
      row.style.cssText = 'display:flex;gap:8px;justify-content:flex-end';

      const cancelBtn = document.createElement('button');
      cancelBtn.textContent = 'Cancel';
      cancelBtn.style.cssText =
        'padding:7px 14px;background:#1d2238;color:#a9b0cf;' +
        'border:1px solid #232a44;border-radius:6px;cursor:pointer;' +
        'font:600 11px Inter,sans-serif;letter-spacing:.06em;text-transform:uppercase';

      const confirmBtn = document.createElement('button');
      confirmBtn.textContent = confirmLabel;
      confirmBtn.style.cssText =
        'padding:7px 14px;background:transparent;color:' +
        accent +
        ';border:1px solid ' +
        accent +
        ';border-radius:6px;cursor:pointer;' +
        'font:600 11px Inter,sans-serif;letter-spacing:.06em;text-transform:uppercase';

      row.appendChild(cancelBtn);
      row.appendChild(confirmBtn);
      card.appendChild(titleEl);
      card.appendChild(msgEl);
      card.appendChild(row);
      backdrop.appendChild(card);
      document.body.appendChild(backdrop);

      requestAnimationFrame(() => {
        backdrop.style.opacity = '1';
        card.style.transform = 'translateY(0)';
      });

      const close = (result) => {
        document.removeEventListener('keydown', onKey);
        backdrop.style.opacity = '0';
        setTimeout(() => backdrop.remove(), 120);
        resolve(result);
      };
      const onKey = (e) => {
        if (e.key === 'Escape') {
          e.preventDefault();
          close(false);
        } else if (e.key === 'Enter') {
          e.preventDefault();
          close(true);
        }
      };

      cancelBtn.addEventListener('click', () => close(false));
      confirmBtn.addEventListener('click', () => close(true));
      backdrop.addEventListener('click', (e) => {
        if (e.target === backdrop) close(false);
      });
      document.addEventListener('keydown', onKey);
      cancelBtn.focus();
    });
  }

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
    requestAnimationFrame(() => {
      t.style.opacity = '1';
    });
    clearTimeout(t._timer);
    t._timer = setTimeout(() => {
      t.style.opacity = '0';
    }, 3000);
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
    'box-shadow:0 8px 30px rgba(0,0,0,.4);z-index:50;display:none;transition:opacity .1s';
  document.body.appendChild(panel);

  function renderPanel() {
    if (!selected) {
      panel.style.display = 'none';
      return;
    }
    panel.style.display = 'block';
    const o = selected.overlay;
    const a = o.anchor || 'top-right';
    panel.innerHTML =
      '<div style="font:700 11px Inter,sans-serif;letter-spacing:.08em;text-transform:uppercase;color:#22d3ee;margin-bottom:10px">' +
      escapeHtml(selected.plugin.title || selected.plugin.name) +
      '</div>' +
      '<div style="margin-bottom:10px">Anchor</div>' +
      '<div data-role="anchors" style="display:grid;grid-template-columns:1fr 1fr;gap:6px;margin-bottom:14px">' +
      anchorBtn('top-left', a) +
      anchorBtn('top-right', a) +
      anchorBtn('bottom-left', a) +
      anchorBtn('bottom-right', a) +
      '</div>' +
      '<div style="display:grid;grid-template-columns:auto 1fr;gap:8px;align-items:center;margin-bottom:14px">' +
      '<label>Width</label>' +
      numInput('width', o.width) +
      '<label>Height</label>' +
      numInput('height', o.height) +
      '</div>' +
      '<div style="margin-bottom:6px">Opacity <span data-role="opacity-val">' +
      (o.opacity == null ? 1 : o.opacity).toFixed(2) +
      '</span></div>' +
      '<input data-role="opacity" type="range" min="0" max="1" step="0.01" value="' +
      (o.opacity == null ? 1 : o.opacity) +
      '" style="width:100%;margin-bottom:14px">' +
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
    return (
      '<button data-anchor="' +
      name +
      '" style="' +
      'padding:6px;background:' +
      (active ? '#22d3ee' : '#1d2238') +
      ';' +
      'color:' +
      (active ? '#0a0c14' : '#a9b0cf') +
      ';' +
      'border:1px solid ' +
      (active ? '#22d3ee' : '#232a44') +
      ';' +
      'border-radius:6px;cursor:pointer;font:600 10px Inter,sans-serif;' +
      'letter-spacing:.05em;text-transform:uppercase' +
      '">' +
      name.replace('-', ' ') +
      '</button>'
    );
  }

  function numInput(role, value) {
    return (
      '<input data-role="' +
      role +
      '" type="number" min="0" value="' +
      (value | 0) +
      '" style="' +
      'width:100%;padding:6px 8px;background:#0f1320;color:#e6e9f5;' +
      'border:1px solid #232a44;border-radius:6px;font:500 12px JetBrains Mono,monospace">'
    );
  }

  function escapeHtml(s) {
    return String(s).replace(
      /[&<>"']/g,
      (c) =>
        ({
          '&': '&amp;',
          '<': '&lt;',
          '>': '&gt;',
          '"': '&quot;',
          "'": '&#39;',
        })[c],
    );
  }

  function wirePanel(w) {
    panel.querySelectorAll('[data-anchor]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const next = btn.getAttribute('data-anchor');
        const prev = w.overlay.anchor || 'top-right';
        if (next === prev) return;
        // Recompute offsets so the widget visually stays in place under
        // the new anchor. Use the widget's layout-coords (offsetLeft/Top
        // relative to the canvas parent) — these are already in canvas-
        // pixel space and unaffected by the canvas's CSS transform.
        const wL = w.el.offsetLeft;
        const wT = w.el.offsetTop;
        const wW = w.el.offsetWidth;
        const wH = w.el.offsetHeight;
        const W = surface.effective.width;
        const H = surface.effective.height;
        let nx, ny;
        if (next.indexOf('-left') >= 0) nx = Math.max(0, Math.round(wL));
        else nx = Math.max(0, Math.round(W - (wL + wW)));
        if (next.indexOf('top') === 0) ny = Math.max(0, Math.round(wT));
        else ny = Math.max(0, Math.round(H - (wT + wH)));
        w.overlay.anchor = next;
        w.overlay.offset_x = nx;
        w.overlay.offset_y = ny;
        applyAnchor(w.el, next, nx, ny);
        positionResizeHandle(w.resize, next);
        saveOverride(w, { anchor: next, offset_x: nx, offset_y: ny }).catch((err) =>
          toast('Save failed: ' + err.message),
        );
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
      saveOverride(w, { opacity: +op.value }).catch((err) => toast('Save failed: ' + err.message));
    });

    panel.querySelector('[data-role="reset"]').addEventListener('click', () => resetWidget(w));
  }

  function commitSize(w, width, height) {
    // Clamp to the same minimum the resize handle uses (16px). 0 would
    // produce an invisible widget that can't be re-selected. Snap to
    // the same 8px grid that drag-resize uses on release.
    width = Math.round(Math.max(16, width | 0) / SNAP) * SNAP;
    height = Math.round(Math.max(16, height | 0) / SNAP) * SNAP;
    w.overlay.width = width;
    w.overlay.height = height;
    w.el.style.width = width + 'px';
    w.el.style.height = height + 'px';
    saveOverride(w, { width, height }).catch((err) => toast('Save failed: ' + err.message));
  }

  async function resetWidget(w) {
    const m = manifestDefaultsFor(w.plugin);
    const body = {
      enabled: true,
      anchor: m.anchor,
      offset_x: m.offset_x,
      offset_y: m.offset_y,
      width: m.width,
      height: m.height,
      opacity: m.opacity,
    };
    try {
      const r = await fetch('/api/overlay/overrides/' + encodeURIComponent(w.plugin.name), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
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

  // Listen for live surface changes from the dashboard. /events filters by
  // the URL param; we ask for just _SurfaceChanged to avoid receiving the
  // full game-event firehose.
  const surfaceES = new EventSource('/events?events=_SurfaceChanged');
  surfaceES.onmessage = (e) => {
    let env;
    try {
      env = JSON.parse(e.data);
    } catch (_) {
      return;
    }
    if (!env || env.Event !== '_SurfaceChanged' || !env.Data) return;
    surface = normalizeSurface(env.Data);
    applyCanvasLayout();
  };
  surfaceES.onerror = (err) => console.warn('[overlay-editor] surface SSE lost', err);

  console.log('[overlay-editor] rendered', widgets.length, 'widget(s)');
})();
