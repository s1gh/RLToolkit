import { isOverlay, isSettingsView } from './env.js';

// K/V store wrappers backed by /api/data/<ns>[/<key>].
//
// The "read-only" gate matches the security model: only overlay /
// settings views can write to the backend store; dashboard views are
// read-only by default. Plugins opt their dashboard view into writes
// via { allowWrites: true } passed to register or makeNamespacedStore.

export async function storeGet(ns, key) {
  try {
    const r = await fetch('/api/data/' + ns + (key ? '/' + key : ''));
    if (!r.ok) return null;
    return await r.json();
  } catch {
    return null;
  }
}

export async function storeSet(ns, key, val) {
  try {
    await fetch('/api/data/' + ns + '/' + key, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(val),
    });
    return true;
  } catch {
    return false;
  }
}

export async function storeDelete(ns, key) {
  try {
    await fetch('/api/data/' + ns + '/' + key, { method: 'DELETE' });
    return true;
  } catch {
    return false;
  }
}

export function debouncedWriter(ns, key, getValue, ms) {
  let timer = null;
  return function flush() {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => storeSet(ns, key, getValue()), ms);
  };
}

const warnedReadOnlyStores = new Set();

export function makeNamespacedStore(ns, opts) {
  const allowWrites = !!(opts && opts.allowWrites) || isOverlay || isSettingsView;
  function readOnlyNoOp(action) {
    const key = ns + ':' + action;
    if (!warnedReadOnlyStores.has(key)) {
      warnedReadOnlyStores.add(key);
      console.warn('[RLT] store.' + action + ' ignored (read-only instance: ' + ns + ').');
    }
    return Promise.resolve(false);
  }
  return {
    get(key) {
      return storeGet(ns, key);
    },
    getAll() {
      return storeGet(ns, '');
    },
    set(key, val) {
      if (!allowWrites) return readOnlyNoOp('set');
      return storeSet(ns, key, val);
    },
    delete(key) {
      if (!allowWrites) return readOnlyNoOp('delete');
      return storeDelete(ns, key);
    },
  };
}
