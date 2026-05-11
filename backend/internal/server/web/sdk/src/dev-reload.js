// dev-reload.js
//
// Live-reload glue for two events, same payload shape ({ name }):
//   _DevPluginReload   — emitted by `rl-toolkit dev` when a dev-
//                        registered plugin's source folder changes.
//   _PluginUpdated     — emitted by the launcher after an in-place
//                        update installs new files for this plugin.
//
// In either case, if the name matches the plugin hosting this SDK
// instance, we reload the page so the new assets take effect. Events
// that don't fire in a given build (e.g. _DevPluginReload in prod)
// just never reach the handler; the cost of subscribing is trivial.
import { bus } from './bus.js';
import { pluginName } from './env.js';

export function installDevReload() {
  const reloadIfMine = (data, kind) => {
    if (!data || data.name !== pluginName) return;
    // Brief log so the developer sees in DevTools why the page reloaded.
    try { console.info('[RLT] ' + kind + ' reload:', pluginName); } catch (_) {}
    try {
      window.location.reload();
    } catch (_) {
      /* no-op in environments without a window (shouldn't happen in
         the SDK's contexts, but defensive). */
    }
  };
  bus.on('_DevPluginReload', (data) => reloadIfMine(data, 'dev'));
  bus.on('_PluginUpdated', (data) => reloadIfMine(data, 'update'));
}
