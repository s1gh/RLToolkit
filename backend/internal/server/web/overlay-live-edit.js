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
  let focusListener = null;

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

  async function manifestDefaultsFor(name) {
    const r = await fetch('/api/plugins');
    if (!r.ok) throw new Error('HTTP ' + r.status + ' fetching /api/plugins');
    const list = await r.json();
    const p = list.find((x) => x.name === name);
    if (!p || !p.overlay) throw new Error('plugin ' + name + ' not in catalog');
    const o = p.overlay;
    return {
      anchor: o.anchor || 'top-right',
      offset_x: o.offset_x | 0,
      offset_y: o.offset_y | 0,
      width: o.width | 0,
      height: o.height | 0,
      opacity: typeof o.opacity === 'number' ? o.opacity : 1,
    };
  }

  async function resetToManifest(name) {
    // PUT manifest defaults rather than DELETE. DELETE would also wipe
    // `enabled: true`, leaving the aggregator's strict overlay.enabled
    // === true check to unmount the iframe entirely. PUT preserves
    // the plugin's enabled state and resets only the visual fields.
    const defaults = await manifestDefaultsFor(name);
    const body = {
      enabled: true,
      anchor: defaults.anchor,
      offset_x: defaults.offset_x,
      offset_y: defaults.offset_y,
      width: defaults.width,
      height: defaults.height,
      opacity: defaults.opacity,
    };
    const r = await fetch('/api/overlay/overrides/' + encodeURIComponent(name), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      const text = (await r.text().catch(() => '')).trim();
      throw new Error('HTTP ' + r.status + (text ? ': ' + text : ''));
    }
    return defaults;
  }

  function makeWrapper(name, iframe, anchor) {
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

    // Chrome placement: keep the label and reset button on the anchor
    // edge, put the resize handle on the opposite (free) corner. This
    // means the handle always lives on the corner that visually moves
    // when the widget grows, matching the standard convention.
    const onTop = anchor.indexOf('top') === 0;
    const onLeft = anchor.indexOf('-left') >= 0;
    const chromeVert = onTop ? 'top' : 'bottom';
    const chromeHoriz = onLeft ? 'left' : 'right';
    const handleVert = onTop ? 'bottom' : 'top';
    const handleHoriz = onLeft ? 'right' : 'left';

    // Inline label so the user can identify each rectangle even when
    // the plugin's actual content isn't rendering (phase-gated plugins
    // during menu/lobby, plugins with hide_when_unfocused that are off,
    // etc.). pointer-events: none so the drag handler still receives
    // the press through the label.
    const label = document.createElement('div');
    label.textContent = name;
    label.style.position = 'absolute';
    label.style[chromeVert] = '4px';
    label.style[chromeHoriz] = '6px';
    label.style.padding = '2px 6px';
    label.style.font = '11px/1.2 system-ui, sans-serif';
    label.style.color = '#22d3ee';
    label.style.background = 'rgba(5, 7, 14, 0.72)';
    label.style.border = '1px solid rgba(34, 211, 238, 0.35)';
    label.style.borderRadius = '3px';
    label.style.pointerEvents = 'none';
    label.style.userSelect = 'none';
    wrap.appendChild(label);

    // Reset button on the anchor edge, opposite end from the label.
    // PUTs the manifest defaults (preserving enabled: true) so the
    // widget snaps back to its declared position and size. mount()
    // wires the click handler so it can close over the live entry.
    const reset = document.createElement('button');
    reset.type = 'button';
    reset.title = 'Reset to manifest defaults';
    reset.textContent = 'Reset';
    reset.style.position = 'absolute';
    reset.style[chromeVert] = '4px';
    // Opposite horizontal end from the label.
    reset.style[onLeft ? 'right' : 'left'] = '4px';
    reset.style.padding = '2px 8px';
    reset.style.font = '11px/1.2 system-ui, sans-serif';
    reset.style.color = '#0a0c14';
    reset.style.background = '#22d3ee';
    reset.style.border = 'none';
    reset.style.borderRadius = '3px';
    reset.style.cursor = 'pointer';
    reset.style.pointerEvents = 'auto';
    reset.style.userSelect = 'none';
    reset.addEventListener('pointerdown', (ev) => ev.stopPropagation());
    wrap.appendChild(reset);

    // Resize handle on the anchor-opposite corner. The drag math runs
    // in attachResize; this just builds the element so attachResize can
    // wire it up.
    const handle = document.createElement('div');
    handle.className = 'rlt-le-resize';
    handle.style.position = 'absolute';
    handle.style[handleVert] = '0';
    handle.style[handleHoriz] = '0';
    handle.style.width = '14px';
    handle.style.height = '14px';
    handle.style.background = '#22d3ee';
    handle.style.border = '1px solid rgba(5, 7, 14, 0.5)';
    handle.style.cursor = 'nwse-resize';
    handle.style.pointerEvents = 'auto';
    handle.style.userSelect = 'none';
    wrap.appendChild(handle);

    return { wrap, handle, reset };
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

  function attachResize(name, entry) {
    const { handle, wrap, iframe } = entry;
    if (!handle) return;
    handle.addEventListener('pointerdown', (ev) => {
      if (ev.button !== 0) return;
      ev.preventDefault();
      ev.stopPropagation(); // don't let the wrapper start a drag

      const anchor = entry.anchor;
      const startW = parsePx(iframe.style.width) || iframe.offsetWidth;
      const startH = parsePx(iframe.style.height) || iframe.offsetHeight;
      const startPointerX = ev.clientX;
      const startPointerY = ev.clientY;
      // Sign flips matching the anchor: the handle sits on the
      // opposite corner, so the cursor's direction-of-growth matches
      // the anchor. left-anchored: +x grows width. right-anchored: -x
      // grows width. Same logic for y.
      const sx = anchor.indexOf('-left') >= 0 ? +1 : -1;
      const sy = anchor.indexOf('top') === 0 ? +1 : -1;
      let liveW = startW;
      let liveH = startH;

      const pointerId = ev.pointerId;
      try {
        handle.setPointerCapture(pointerId);
      } catch (_) {}

      entry.resizeToken = (entry.resizeToken | 0) + 1;
      const token = entry.resizeToken;

      const onMove = (mv) => {
        if (mv.pointerId !== pointerId) return;
        mv.preventDefault();
        const dw = (mv.clientX - startPointerX) * sx;
        const dh = (mv.clientY - startPointerY) * sy;
        // Minimum size: 16px. The wrapper grows visually since its
        // anchored edge stays put.
        liveW = Math.max(16, startW + dw);
        liveH = Math.max(16, startH + dh);
        wrap.style.width = liveW + 'px';
        wrap.style.height = liveH + 'px';
      };

      const onUp = async (upEv) => {
        if (upEv.pointerId !== pointerId) return;
        handle.removeEventListener('pointermove', onMove);
        handle.removeEventListener('pointerup', onUp);
        handle.removeEventListener('pointercancel', onUp);
        try {
          handle.releasePointerCapture(pointerId);
        } catch (_) {}

        const finalW = upEv.shiftKey
          ? Math.max(16, Math.round(liveW))
          : Math.max(16, snapTo(liveW, SNAP));
        const finalH = upEv.shiftKey
          ? Math.max(16, Math.round(liveH))
          : Math.max(16, snapTo(liveH, SNAP));
        wrap.style.width = finalW + 'px';
        wrap.style.height = finalH + 'px';

        try {
          await savePartial(name, { width: finalW, height: finalH });
        } catch (err) {
          if (entry.resizeToken !== token) return;
          applyRect(wrap, iframe);
          console.warn('[live-edit] resize save failed', err);
        }
      };

      handle.addEventListener('pointermove', onMove);
      handle.addEventListener('pointerup', onUp);
      handle.addEventListener('pointercancel', onUp);
    });
  }

  function attachObserver(entry) {
    const { wrap, iframe } = entry;
    const observer = new MutationObserver(() => applyRect(wrap, iframe));
    observer.observe(iframe, { attributes: true, attributeFilter: ['style'] });
    entry.observer = observer;
  }

  function setWrappersVisible(visible) {
    for (const entry of wrappers.values()) {
      entry.wrap.style.display = visible ? '' : 'none';
    }
  }

  function installFocusListener() {
    if (focusListener) return;
    focusListener = (ev) => {
      const data = ev?.data;
      if (!data || data.__rlt_focus__ !== true) return;
      // Hide wrappers when RL loses focus so the cyan outlines don't
      // bleed onto other workspaces. Edit mode itself stays armed,
      // so wrappers reappear when RL regains focus. The focus signal
      // is edge-triggered: the aggregator only emits on transition.
      setWrappersVisible(!!data.active);
    };
    window.addEventListener('message', focusListener);
  }

  function removeFocusListener() {
    if (!focusListener) return;
    window.removeEventListener('message', focusListener);
    focusListener = null;
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
      const anchor = anchorOf(w.iframe);
      const { wrap, handle, reset } = makeWrapper(name, w.iframe, anchor);
      document.body.appendChild(wrap);
      const entry = {
        wrap,
        handle,
        iframe: w.iframe,
        observer: null,
        anchor,
        dragToken: 0,
        resizeToken: 0,
      };
      attachDrag(name, entry);
      attachResize(name, entry);
      attachObserver(entry);
      // Reset click handler. Optimistically paint the manifest rect
      // onto the wrapper immediately so the user sees the change
      // without waiting for the network round-trip; the SSE reflow
      // confirms by mutating iframe.style (which the MutationObserver
      // re-syncs onto the wrapper).
      reset.addEventListener('click', async (ev) => {
        ev.stopPropagation();
        try {
          const defaults = await resetToManifest(name);
          // Update entry.anchor in case the manifest's anchor differs
          // from the override that was just wiped.
          entry.anchor = defaults.anchor;
          // Paint the manifest rect onto the wrapper now. Match the
          // iframe's encoding: width/height plus the two anchored
          // sides only, clear the other two.
          wrap.style.width = defaults.width + 'px';
          wrap.style.height = defaults.height + 'px';
          applyAnchoredOffset(wrap, defaults.anchor, defaults.offset_x, defaults.offset_y);
        } catch (err) {
          console.warn('[live-edit] reset failed', err);
        }
      });
      wrappers.set(name, entry);
    }
    installEscListener();
    installFocusListener();
    // Sync initial visibility against the most recent focus transition
    // the aggregator has seen. If the user activated edit mode via the
    // tray or CLI while RL wasn't focused, this hides the wrappers up
    // front so they don't bleed onto whatever workspace the user is on.
    // null means no transition has been emitted yet; default to visible
    // (the activation pathway implies the user wants to see them).
    const initialFocus = window.__rlt_last_focus;
    if (initialFocus === false) setWrappersVisible(false);
  }

  function unmount() {
    removeEscListener();
    removeFocusListener();
    for (const entry of wrappers.values()) {
      if (entry.observer) entry.observer.disconnect();
      entry.wrap.remove();
    }
    wrappers.clear();
  }

  window.__rlt_live_edit = { mount, unmount };
})();
