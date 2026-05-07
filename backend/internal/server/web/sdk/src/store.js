import { isOverlay, isSettingsView } from './env.js';

// K/V store wrappers backed by /api/data/<ns>[/<key>].
//
// The "read-only" gate matches the security model: only overlay /
// settings views can write to the backend store; dashboard views are
// read-only by default. Plugins opt their dashboard view into writes
// via { allowWrites: true } passed to register or makeNamespacedStore.
//
// onChange subscribes to _StoreChanged (broadcast by the backend on
// every successful write/delete), filtered to a specific namespace.
// Bus + addEvent are wired in via setStoreBusDeps from index.js to
// avoid a static import cycle (bus.js → stamps.js → identity.js →
// store.js would reorder evaluation and break boot).

let _busOn = null;
let _addEvent = null;

export function setStoreBusDeps(busOn, addEvent) {
  _busOn = busOn;
  _addEvent = addEvent;
}

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
    // Subscribe to writes that hit this namespace from any context
    // (overlay, dashboard preview, settings panel — all share the
    // backend store). Handler is called with {key, op: 'set'|'delete'}.
    // If `key` is given, only fires for that key. Returns an
    // unsubscribe function. No-op if the bus deps weren't wired (e.g.
    // pre-init unit tests).
    onChange(filterKey, fn) {
      if (typeof filterKey === 'function') {
        fn = filterKey;
        filterKey = null;
      }
      if (!_busOn || typeof fn !== 'function') return () => {};
      _addEvent('_StoreChanged');
      return _busOn('_StoreChanged', (payload) => {
        if (!payload || payload.namespace !== ns) return;
        if (filterKey && payload.key !== filterKey) return;
        try {
          fn({ key: payload.key, op: payload.op });
        } catch (e) {
          console.error('[RLT] store.onChange handler threw:', e);
        }
      });
    },
  };
}
