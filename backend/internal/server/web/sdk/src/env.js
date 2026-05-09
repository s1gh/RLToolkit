// Boot-time environment: read once from the loading <script> tag and
// the URL. Pure module — never mutated. Other modules import these
// constants directly.
//
// View detection: the plugin's HTML declares its view via
//   <script src="/sdk.js" data-plugin="my-plugin" data-view="overlay">
// Valid values: 'overlay', 'dashboard', 'settings', 'background'.
// Defaults to 'overlay' so legacy single-view plugins keep working
// with no migration. The view drives:
//   - body.overlay-mode auto-styling (only in 'overlay')
//   - RLT.settings.close() being meaningful (only in 'settings')
//   - which url-param contracts apply (none anymore)
//
// 'background' is a hidden, always-running iframe loaded by the
// launcher for plugins that need to react to events independent of
// any visible UI surface (e.g. a replay uploader that runs the
// moment a replay is saved, regardless of which tab the user has
// open).

const VALID_VIEWS = new Set(['overlay', 'dashboard', 'settings', 'background']);

export const urlParams = new URLSearchParams(location.search);
export const hostedBus = urlParams.has('__rlt_hosted');

const _script = (() => {
  try {
    return document.currentScript || null;
  } catch (_) {
    return null;
  }
})();

export const view = (function discover() {
  const decl = _script?.dataset?.view;
  if (decl && VALID_VIEWS.has(decl)) return decl;
  // Legacy fallback: undeclared view defaults to overlay so older
  // single-file plugins (or hand-written test pages) keep behaving
  // like overlays. The new manifest schema makes this explicit at
  // boot via data-view, so this branch is the soft-landing case.
  return 'overlay';
})();

export const isOverlay = view === 'overlay';
export const isDashboard = view === 'dashboard';
export const isSettingsView = view === 'settings';
export const isBackground = view === 'background';

export const pluginName = (function discover() {
  let name = 'unknown';
  try {
    if (_script?.dataset?.plugin) {
      name = _script.dataset.plugin;
    } else {
      const m = location.pathname.match(/\/plugins\/([^/]+)\//);
      if (m) name = m[1];
    }
  } catch (_) {
    /* noop */
  }
  return name;
})();
