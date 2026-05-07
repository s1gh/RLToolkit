import { pluginName } from './env.js';
import { makeNamespacedStore } from './store.js';

// The default per-plugin K/V store, namespaced to the plugin name
// derived from <script data-plugin> or the URL path.
export const store = makeNamespacedStore(pluginName);
