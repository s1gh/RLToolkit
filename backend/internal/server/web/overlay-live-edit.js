// Live overlay edit mode.
//
// Loaded by overlay.html (lazy import) when the Tauri host activates
// edit mode. Wraps each plugin iframe with a transparent capture div,
// handles pointer-based dragging, snaps to an 8px grid on release
// (Shift bypasses snap), and PUTs the new offset_x/offset_y to
// /api/overlay/overrides/<name>.
//
// The wrapper rect mirrors its iframe's rect via a MutationObserver,
// so when the existing reflow pipeline updates the iframe position
// (after _OverridesChanged SSE), the wrapper follows.
//
// Anchor semantics: an iframe uses two of {top, bottom, left, right}
// based on its anchor (top-left, top-right, bottom-left, bottom-right).
// Drag deltas have to be sign-flipped for right- and bottom-anchored
// widgets so dragging visually right always increases the offset from
// the anchored edge.

(function () {
  const WRAPPER_CLASS = 'rlt-le-wrapper';
  const SNAP = 8;
  // pluginName -> { wrap, iframe, observer, anchor, dragToken }
  const wrappers = new Map();
  let escListener = null;

  function parsePx(value) {
    if (!value) return 0;
    const n = parseFloat(value);
    return Number.isFinite(n) ? n : 0;
  }

  function snapTo(value, step) {
    return Math.round(value / step) * step;
  }

  function anchorOf(iframe) {
    try {
      const u = new URL(iframe.src, location.origin);
      return u.searchParams.get('anchor') || 'top-right';
    } catch {
      return 'top-right';
    }
  }

  function currentOffsets(iframe, anchor) {
    // Read offset_x/offset_y from the iframe's current style. Positive
    // distance from the anchored edge.
    const top = parsePx(iframe.style.top);
    const bottom = parsePx(iframe.style.bottom);
    const left = parsePx(iframe.style.left);
    const right = parsePx(iframe.style.right);
    const x = anchor.indexOf('-left') >= 0 ? left : right;
    const y = anchor.indexOf('top') === 0 ? top : bottom;
    return { x, y };
  }

  function applyRect(wrap, iframe) {
    const cs = iframe.style;
    wrap.style.width = cs.width;
    wrap.style.height = cs.height;
    wrap.style.top = cs.top;
    wrap.style.bottom = cs.bottom;
    wrap.style.left = cs.left;
    wrap.style.right = cs.right;
  }

  function applyAnchoredOffset(wrap, anchor, x, y) {
    // Mirror the iframe's anchor encoding: write to the two
    // anchor-relevant sides only, clear the other two.
    wrap.style.top = wrap.style.bottom = wrap.style.left = wrap.style.right = '';
    if (anchor.indexOf('top') === 0) wrap.style.top = y + 'px';
    else wrap.style.bottom = y + 'px';
    if (anchor.indexOf('-left') >= 0) wrap.style.left = x + 'px';
    else wrap.style.right = x + 'px';
  }

  async function savePartial(name, partial) {
    const r = await fetch('/api/overlay/overrides/' + encodeURIComponent(name), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(partial),
    });
    if (!r.ok) {
      const text = (await r.text().catch(() => '')).trim();
      throw new Error('HTTP ' + r.status + (text ? ': ' + text : ''));
    }
  }

  function makeWrapper(name, iframe) {
    const wrap = document.createElement('div');
    wrap.className = WRAPPER_CLASS;
    wrap.dataset.pluginName = name;
    wrap.style.position = 'absolute';
    wrap.style.pointerEvents = 'auto';
    wrap.style.cursor = 'move';
    wrap.style.boxSizing = 'border-box';
    wrap.style.outline = '1px solid #22d3ee';
    wrap.style.background = 'rgba(34, 211, 238, 0.06)';
    wrap.style.zIndex = '2147483647';
    applyRect(wrap, iframe);
    return wrap;
  }

  function attachDrag(name, entry) {
    const { wrap, iframe } = entry;
    wrap.addEventListener('pointerdown', (ev) => {
      if (ev.button !== 0) return;
      ev.preventDefault();

      const anchor = anchorOf(iframe);
      entry.anchor = anchor;
      const start = currentOffsets(iframe, anchor);
      const startPointerX = ev.clientX;
      const startPointerY = ev.clientY;
      // Anchor sign flips so dragging visually-right grows the offset
      // from the right edge for right-anchored widgets (and likewise
      // for bottom anchors). Same convention as overlay-editor.js.
      const sx = anchor.indexOf('-left') >= 0 ? +1 : -1;
      const sy = anchor.indexOf('top') === 0 ? +1 : -1;
      let liveX = start.x;
      let liveY = start.y;

      const pointerId = ev.pointerId;
      try {
        wrap.setPointerCapture(pointerId);
      } catch (_) {}

      // Stale-rollback guard. A second drag bumps the token, so the
      // first drag's failure catch can detect it shouldn't roll back.
      entry.dragToken = (entry.dragToken | 0) + 1;
      const token = entry.dragToken;

      const onMove = (mv) => {
        if (mv.pointerId !== pointerId) return;
        mv.preventDefault();
        const dx = (mv.clientX - startPointerX) * sx;
        const dy = (mv.clientY - startPointerY) * sy;
        liveX = start.x + dx;
        liveY = start.y + dy;
        // Optimistic local update: move the wrapper to track the
        // cursor. The iframe stays at its previous position until
        // SSE reflow lands after the save.
        applyAnchoredOffset(wrap, anchor, liveX, liveY);
      };

      const onUp = async (upEv) => {
        if (upEv.pointerId !== pointerId) return;
        wrap.removeEventListener('pointermove', onMove);
        wrap.removeEventListener('pointerup', onUp);
        wrap.removeEventListener('pointercancel', onUp);
        try {
          wrap.releasePointerCapture(pointerId);
        } catch (_) {}

        const finalX = upEv.shiftKey ? Math.round(liveX) : snapTo(liveX, SNAP);
        const finalY = upEv.shiftKey ? Math.round(liveY) : snapTo(liveY, SNAP);
        // Reflect the snapped value locally before the save round-trip.
        applyAnchoredOffset(wrap, anchor, finalX, finalY);

        try {
          await savePartial(name, { offset_x: finalX, offset_y: finalY });
          // SSE _OverridesChanged -> reflow -> iframe.style mutates ->
          // MutationObserver re-syncs wrapper to iframe rect.
        } catch (err) {
          if (entry.dragToken !== token) return; // a newer drag is in flight
          applyRect(wrap, iframe); // restore from current iframe
          console.warn('[live-edit] save failed', err);
        }
      };

      wrap.addEventListener('pointermove', onMove);
      wrap.addEventListener('pointerup', onUp);
      wrap.addEventListener('pointercancel', onUp);
    });
  }

  function attachObserver(entry) {
    const { wrap, iframe } = entry;
    const observer = new MutationObserver(() => applyRect(wrap, iframe));
    observer.observe(iframe, { attributes: true, attributeFilter: ['style'] });
    entry.observer = observer;
  }

  function installEscListener() {
    if (escListener) return;
    escListener = (ev) => {
      if (ev.key !== 'Escape') return;
      ev.preventDefault();
      try {
        window.__TAURI_INTERNALS__?.invoke('overlay_edit_exit');
      } catch (_) {
        // Outside Tauri (e.g. browser test page); unmount locally.
        unmount();
      }
    };
    document.addEventListener('keydown', escListener);
  }

  function removeEscListener() {
    if (!escListener) return;
    document.removeEventListener('keydown', escListener);
    escListener = null;
  }

  function mount(widgetsMap) {
    if (wrappers.size) return;
    for (const [name, w] of widgetsMap.entries()) {
      if (!w.iframe) continue;
      const wrap = makeWrapper(name, w.iframe);
      document.body.appendChild(wrap);
      const entry = {
        wrap,
        iframe: w.iframe,
        observer: null,
        anchor: anchorOf(w.iframe),
        dragToken: 0,
      };
      attachDrag(name, entry);
      attachObserver(entry);
      wrappers.set(name, entry);
    }
    installEscListener();
  }

  function unmount() {
    removeEscListener();
    for (const entry of wrappers.values()) {
      if (entry.observer) entry.observer.disconnect();
      entry.wrap.remove();
    }
    wrappers.clear();
  }

  window.__rlt_live_edit = { mount, unmount };
})();
