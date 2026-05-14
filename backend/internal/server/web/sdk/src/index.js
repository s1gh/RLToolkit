/**
 * RL Toolkit — Plugin SDK (v1)
 *
 *   <script src="/sdk.js" data-plugin="my-plugin"></script>
 *
 * Exposes `window.RLT`. The event catalog (names, shapes, phase
 * gating, stability) is the canonical reference: read it via
 * RLT.events.catalog or GET /api/events.
 *
 * This file is the entry point of the modular SDK. Source files live
 * under sdk/src/ and are bundled by esbuild into sdk/dist/sdk.js. The
 * Go binary embeds the bundled output. The Makefile banner wraps the
 * whole IIFE in `if (!window.RLT) { … }` so a duplicate include is a
 * no-op.
 */

// Import order matters. Modules with module-top-level side effects
// (HTTP fetches, bus.on subscriptions, observers) execute as soon as
// they're imported; the order here mirrors the legacy monolith's
// top-down evaluation so subscription order is preserved.
import { pluginName, view, isOverlay, isDashboard, isSettingsView, isBackground, hostedBus, urlParams } from './env.js';
import './overlay-mode.js';
import { bus, addEvent, getStatus, closeEventSource, clearReconnectWatchdog, connect, dispatchEnvelope, postToHost, subscribedEvents } from './bus.js';
import { identity } from './identity.js';
import { encounters } from './encounters.js';
import { state } from './state.js';
import { events } from './events.js';
import { match } from './match.js';
import './catalog.js';
import { stats } from './stats.js';
import { focus } from './focus.js';
import './visibility.js';
import { statusStableState } from './status-stable.js';
import { widget, teardownWatchers } from './widget.js';
import { plugin } from './register.js';
import { store } from './pluginstore.js';
import { ui } from './ui.js';
import { util } from './util.js';
import { settings } from './settings.js';
import { getManifest, onManifest } from './manifest.js';
import { setStampDeps } from './stamps.js';
import { setStoreBusDeps } from './store.js';
import { installDevReload } from './dev-reload.js';

// Wire late-bound deps that would otherwise create import cycles.
// Must run before any SSE event is dispatched (we kick off connect()
// at the end of this file, which is after these singletons exist).
setStampDeps(identity, encounters);
setStoreBusDeps(bus.on.bind(bus), addEvent);
installDevReload();

// ─── Public API ────────────────────────────────────────────
window.RLT = {
  plugin,
  pluginName,
  pluginManifest: () => getManifest(),
  onManifest,
  version: 1,

  on: (ev, fn) => {
    if (ev && ev !== '*') addEvent(ev);
    return bus.on(ev, fn);
  },
  off: (ev, fn) => bus.off(ev, fn),
  status: () => getStatus(),
  onStatus: (fn) => bus.on('_status', fn),
  statusStable: () => statusStableState.get(),
  onStatusStable: (fn) => statusStableState.onChange(fn),
  match,
  me: identity,
  // RLT.identity is an alias for RLT.me — kept for plugins from the
  // pre-convergence era when the two referred to different stores.
  identity,
  onIdentityChange: (fn) => bus.on('_IdentityChanged', (snap) => fn(snap)),
  encounters,
  store,
  ui,
  util,
  events,
  stats,
  widget,
  focus,

  // Render-context discriminators. `view` is the canonical one
  // ('overlay' | 'dashboard' | 'settings' | 'background'); the
  // booleans are shorthands for the common branches.
  view,
  isOverlay,
  isDashboard,
  isSettingsView,
  isBackground,
  settings,
};

window.RLT._reconnect = function () {
  closeEventSource();
  connect();
};

Object.freeze(window.RLT);

// Pagehide: close SSE, cancel watchdog, tear down widget observers so
// we don't leak when the page navigates away.
window.addEventListener('pagehide', () => {
  closeEventSource();
  clearReconnectWatchdog();
  teardownWatchers();
});

if (hostedBus) {
  window.addEventListener('message', (e) => {
    const data = e?.data;
    if (!data || data.__rlt_bus__ !== true || !data.msg) return;
    if (e.source !== window.parent) return;
    dispatchEnvelope(data.msg);
  });
  Promise.resolve().then(() => {
    postToHost({ __rlt_bus_hello__: true, events: Array.from(subscribedEvents) });
  });
} else {
  Promise.resolve().then(connect);
}

// Manifest-driven auto-size opt-in. The host (overlay.html) appends
// auto_width=1 / auto_height=1 to the iframe URL when the manifest
// declares width / height: "auto", plus optional min_/max_ caps. The
// plugin doesn't have to call RLT.widget.autoSize itself — having
// "auto" in the manifest is the opt-in. Plugins that want finer
// control (specific target element, shorter debounce) can still call
// widget.autoSize explicitly and override this default.
if (isOverlay && (urlParams.has('auto_width') || urlParams.has('auto_height'))) {
  const numFlag = (k) => {
    const v = parseInt(urlParams.get(k) || '0', 10);
    return Number.isFinite(v) && v > 0 ? v : 0;
  };
  const opts = {
    minWidth:  numFlag('min_width')  || 1,
    minHeight: numFlag('min_height') || 1,
    maxWidth:  numFlag('max_width')  || 4096,
    maxHeight: numFlag('max_height') || 4096,
  };
  // Defer to DOMContentLoaded so document.body exists. widget.autoSize
  // measures body by default; running before DOM-ready would observe
  // an empty document and report 0.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => widget.autoSize(true, opts), { once: true });
  } else {
    widget.autoSize(true, opts);
  }
}
