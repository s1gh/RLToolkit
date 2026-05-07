// Settings panel bridge. The dashboard hosts the settings view in an
// iframe; the panel posts back here when it wants to close.

export const settings = {
  close() {
    if (window.parent && window.parent !== window) {
      try {
        window.parent.postMessage({ type: 'rlt:settings:close' }, location.origin);
      } catch {
        /* noop */
      }
    }
  },
};
