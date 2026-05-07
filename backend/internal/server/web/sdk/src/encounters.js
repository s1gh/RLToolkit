import { emitter } from './util.js';
import { storeGet, debouncedWriter } from './store.js';
import { isBotId } from './bot.js';

// Encounter ledger (shared across all plugins). Persists at
// _rlt:encounters; debounced writer flushes on change.

export const encounters = (function () {
  const ev = emitter();
  let map = {};
  let loaded = false;
  const persistShared = debouncedWriter('_rlt', 'encounters', () => map, 1500);

  async function load() {
    const fresh = await storeGet('_rlt', 'encounters');
    if (fresh) map = fresh;
    loaded = true;
    ev.emit('change', map);
  }
  load();

  function record(id, name, guid) {
    if (!id) return false;
    const now = new Date().toISOString();
    if (!map[id]) {
      map[id] = { names: [name], count: 1, first_seen: now, last_seen: now, matches: [guid] };
      ev.emit('change', map);
      persistShared();
      return true;
    }
    const e = map[id];
    if (!e.matches) e.matches = [];
    if (e.matches.includes(guid)) {
      e.last_seen = now;
      if (!e.names.includes(name)) e.names.push(name);
      persistShared();
      return false;
    }
    e.count++;
    e.last_seen = now;
    if (!e.names.includes(name)) e.names.push(name);
    e.matches.push(guid);
    if (e.matches.length > 50) e.matches = e.matches.slice(-50);
    ev.emit('change', map);
    persistShared();
    return true;
  }

  return {
    get(id) {
      return map[id] || null;
    },
    all() {
      return Object.assign({}, map);
    },
    isBotId,
    isReady() {
      return loaded;
    },
    onChange(fn) {
      return ev.on('change', fn);
    },
    _record: record,
  };
})();
