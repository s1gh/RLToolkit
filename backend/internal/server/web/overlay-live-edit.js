// Live overlay edit mode -- Phase 2 stub.
//
// Loaded by overlay.html (lazy import) when the Tauri host activates
// edit mode. Mounts a 1px outline over each widget iframe so the user
// can see what's draggable. Phase 3 replaces this with actual drag
// handlers, pointer math, and POSTs to /api/overlay/overrides.
//
// The widget map is owned by overlay.html; mount/unmount only read it.

(function () {
  const WRAPPER_CLASS = 'rlt-le-wrapper';
  const wrappers = new Map(); // plugin name -> wrapper element

  function applyRect(wrap, iframe) {
    const cs = iframe.style;
    wrap.style.width = cs.width;
    wrap.style.height = cs.height;
    wrap.style.top = cs.top;
    wrap.style.bottom = cs.bottom;
    wrap.style.left = cs.left;
    wrap.style.right = cs.right;
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

  function mount(widgetsMap) {
    if (wrappers.size) return;
    for (const [name, w] of widgetsMap.entries()) {
      if (!w.iframe) continue;
      const wrap = makeWrapper(name, w.iframe);
      document.body.appendChild(wrap);
      wrappers.set(name, wrap);
    }
  }

  function unmount() {
    for (const wrap of wrappers.values()) {
      wrap.remove();
    }
    wrappers.clear();
  }

  window.__rlt_live_edit = { mount, unmount };
})();
