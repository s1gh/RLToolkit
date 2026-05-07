// dev-reload.js
//
// Live-reload glue for `rl-toolkit dev`. The backend broadcasts a
// `_DevPluginReload` event with `{ name }` whenever a dev-registered
// plugin's source folder changes. If the name matches the plugin
// hosting this SDK instance, we reload the page so the new assets
// take effect immediately — closing the dev loop without manual
// browser refreshes.
//
// In production builds (no dev-registered plugin matching this view's
// name), the event simply never fires. The handler is cheap and safe
// to install unconditionally.
import { bus } from './bus.js';
import { pluginName } from './env.js';

export function installDevReload() {
  bus.on('_DevPluginReload', (data) => {
    if (!data || data.name !== pluginName) return;
    // Brief log so the developer sees in DevTools why the page reloaded.
    try { console.info('[RLT] dev reload:', pluginName); } catch (_) {}
    try {
      window.location.reload();
    } catch (_) {
      /* no-op in environments without a window (shouldn't happen in
         the SDK's contexts, but defensive). */
    }
  });
}
