import { pluginName } from './env.js';

// Manifest fetch + cache. State here is mutable but module-private;
// other modules consume through the exported getters and the promise.
const subs = new Set();
let manifest = null;
let loaded = false;

function finalize(m) {
  manifest = m;
  loaded = true;
  if (!m && pluginName !== 'unknown') {
    console.warn('[RLT] no manifest for "' + pluginName + '" — using fallback metadata.');
  }
  for (const fn of subs) {
    try {
      fn(manifest);
    } catch (e) {
      console.error('[RLT] onManifest threw:', e);
    }
  }
  subs.clear();
}

export const manifestPromise = fetch('/api/plugins')
  .then((r) => (r.ok ? r.json() : []))
  .then((list) => {
    finalize((list || []).find((p) => p?.name === pluginName) || null);
    return manifest;
  })
  .catch((e) => {
    console.warn('[RLT] manifest fetch failed:', e);
    finalize(null);
    return null;
  });

export function getManifest() {
  return manifest;
}

export function isManifestLoaded() {
  return loaded;
}

export function onManifest(fn) {
  if (loaded) {
    try {
      fn(manifest);
    } catch (e) {
      console.error('[RLT] onManifest threw:', e);
    }
    return () => {};
  }
  subs.add(fn);
  return () => subs.delete(fn);
}
